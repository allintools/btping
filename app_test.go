package main

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"btping/internal/scanner"
)

func listenLoopback(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestStartScanStreamsRowsAndSummary(t *testing.T) {
	port := listenLoopback(t)
	app := NewApp()
	app.ctx = context.Background()

	var mu sync.Mutex
	var rowEvents []scanner.Row
	done := make(chan map[string]any, 1)
	app.emit = func(event string, payload any) {
		switch event {
		case "row":
			mu.Lock()
			rowEvents = append(rowEvents, payload.(scanner.Row))
			mu.Unlock()
		case "done":
			done <- payload.(map[string]any)
		}
	}

	started, err := app.StartScan(ScanRequest{
		Targets:     "127.0.0.1:" + strconv.Itoa(port),
		Ports:       "80",
		TimeoutMs:   1000,
		Concurrency: 4,
		Rounds:      2,
		IntervalMs:  10,
	})
	if err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	if started.Targets != 1 {
		t.Fatalf("targets = %d, want 1", started.Targets)
	}

	var payload map[string]any
	select {
	case payload = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not finish in time")
	}

	summary, ok := payload["summary"].(scanner.Summary)
	if !ok {
		t.Fatalf("summary payload has type %T", payload["summary"])
	}
	rows, ok := payload["rows"].([]scanner.Row)
	if !ok {
		t.Fatalf("rows payload has type %T", payload["rows"])
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Status != scanner.StatusOpen || rows[0].Success != 2 {
		t.Fatalf("row = %+v", rows[0])
	}
	if summary.Success != 2 || summary.Reachable != 1 || summary.Cancelled {
		t.Fatalf("summary = %+v", summary)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rowEvents) < 3 {
		t.Fatalf("expected pending plus per-probe row events, got %d", len(rowEvents))
	}
	if rowEvents[0].Status != scanner.StatusPending {
		t.Fatalf("first row event status = %q, want pending", rowEvents[0].Status)
	}
}

func TestStartScanRejectsInvalidInput(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()

	_, err := app.StartScan(ScanRequest{Targets: "   ", Ports: "80"})
	if err == nil {
		t.Fatal("expected an error for empty targets")
	}

	app.mu.Lock()
	running := app.running
	app.mu.Unlock()
	if running {
		t.Fatal("app should not be marked running after a parse error")
	}

	// A valid request must still work once the bad one was rejected.
	port := listenLoopback(t)
	done := make(chan struct{}, 1)
	app.emit = func(event string, payload any) {
		if event == "done" {
			done <- struct{}{}
		}
	}
	if _, err := app.StartScan(ScanRequest{Targets: "127.0.0.1:" + strconv.Itoa(port), Rounds: 1, TimeoutMs: 1000}); err != nil {
		t.Fatalf("StartScan after rejection: %v", err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("second run did not finish")
	}
}

func TestStartScanRefusesConcurrentRun(t *testing.T) {
	port := listenLoopback(t)
	app := NewApp()
	app.ctx = context.Background()
	finished := make(chan struct{}, 1)
	app.emit = func(event string, payload any) {
		if event == "done" {
			finished <- struct{}{}
		}
	}

	request := ScanRequest{
		Targets:    "127.0.0.1:" + strconv.Itoa(port),
		Rounds:     40,
		TimeoutMs:  500,
		IntervalMs: 100,
	}
	if _, err := app.StartScan(request); err != nil {
		t.Fatalf("first StartScan: %v", err)
	}
	if _, err := app.StartScan(request); err == nil {
		t.Fatal("second StartScan should fail while a run is active")
	} else if !strings.Contains(err.Error(), "已有检测任务") {
		t.Fatalf("unexpected error: %v", err)
	}

	app.StopScan()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("stopped run did not report completion")
	}
}

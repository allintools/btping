package scanner

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu      sync.Mutex
	last    map[string]Row
	history []Row
}

func newRecorder() *recorder {
	return &recorder{last: make(map[string]Row)}
}

func (r *recorder) hook(row Row) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[row.Key] = row
	r.history = append(r.history, row)
}

func (r *recorder) get(key string) Row {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last[key]
}

func startListener(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	return port, func() {
		_ = ln.Close()
		<-done
	}
}

func TestRunDetectsOpenPort(t *testing.T) {
	port, stop := startListener(t)
	defer stop()

	rec := newRecorder()
	targets := []Target{{Host: "127.0.0.1", Port: port}}
	opts := Options{TimeoutMs: 2000, Concurrency: 4, Rounds: 3, IntervalMs: 10}

	summary := Run(context.Background(), targets, opts, Hooks{Row: rec.hook})
	row := rec.get(targets[0].Key())

	if row.Status != StatusOpen {
		t.Fatalf("status = %q, want %q (error: %s)", row.Status, StatusOpen, row.Error)
	}
	if row.Success != 3 || row.Total != 3 {
		t.Fatalf("success/total = %d/%d, want 3/3", row.Success, row.Total)
	}
	if row.Loss != 0 {
		t.Fatalf("loss = %v, want 0", row.Loss)
	}
	// Loopback connects can be fast enough to round to zero, so the invariant
	// to check is that every latency stays ordered and that timing happened.
	if row.AvgMs <= 0 || row.MaxMs <= 0 {
		t.Fatalf("latency not measured: min=%v avg=%v max=%v", row.MinMs, row.AvgMs, row.MaxMs)
	}
	if row.MinMs < 0 || row.MinMs > row.AvgMs || row.AvgMs > row.MaxMs {
		t.Fatalf("latency out of order: min=%v avg=%v max=%v", row.MinMs, row.AvgMs, row.MaxMs)
	}
	if row.IP != "127.0.0.1" {
		t.Fatalf("ip = %q, want 127.0.0.1", row.IP)
	}
	if summary.Success != 3 || summary.Reachable != 1 || summary.Targets != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Planned != 3 || summary.Executed != 3 || summary.Failed != 0 {
		t.Fatalf("summary counters = %+v", summary)
	}
}

func TestRunMarksUnreachablePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	rec := newRecorder()
	targets := []Target{{Host: "127.0.0.1", Port: port}}
	opts := Options{TimeoutMs: 800, Concurrency: 2, Rounds: 2, IntervalMs: 10}

	summary := Run(context.Background(), targets, opts, Hooks{Row: rec.hook})
	row := rec.get(targets[0].Key())

	if row.Status == StatusOpen {
		t.Fatalf("closed port reported as open")
	}
	if row.Success != 0 || row.Total != 2 {
		t.Fatalf("success/total = %d/%d, want 0/2", row.Success, row.Total)
	}
	if row.Loss != 100 {
		t.Fatalf("loss = %v, want 100", row.Loss)
	}
	if summary.Success != 0 || summary.Reachable != 0 || summary.Failed != 2 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestRunReportsUnresolvedHost(t *testing.T) {
	rec := newRecorder()
	targets := []Target{{Host: "host.invalid", Port: 80}}
	opts := Options{TimeoutMs: 800, Concurrency: 2, Rounds: 2, IntervalMs: 10}

	summary := Run(context.Background(), targets, opts, Hooks{Row: rec.hook})
	row := rec.get(targets[0].Key())

	if row.Status != StatusUnresolved {
		t.Fatalf("status = %q, want %q", row.Status, StatusUnresolved)
	}
	if row.Total != 2 || row.Success != 0 {
		t.Fatalf("success/total = %d/%d, want 0/2", row.Success, row.Total)
	}
	if summary.Failed != 2 {
		t.Fatalf("summary failed = %d, want 2", summary.Failed)
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	rec := newRecorder()
	targets := []Target{{Host: "127.0.0.1", Port: 9}}
	opts := Options{TimeoutMs: 300, Concurrency: 2, Rounds: 5, IntervalMs: 50}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	summary := Run(ctx, targets, opts, Hooks{Row: rec.hook})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancelled run took %v, want it to stop promptly", elapsed)
	}
	if !summary.Cancelled {
		t.Fatal("summary.Cancelled = false, want true")
	}
	row := rec.get(targets[0].Key())
	if row.Status != StatusError || row.Error != "已取消" {
		t.Fatalf("row = %+v, want cancelled marker", row)
	}
}

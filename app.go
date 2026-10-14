package main

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"btping/internal/scanner"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ScanRequest is the payload sent from the UI when a detection run starts.
type ScanRequest struct {
	Targets     string `json:"targets"`
	Ports       string `json:"ports"`
	TimeoutMs   int    `json:"timeoutMs"`
	Concurrency int    `json:"concurrency"`
	Rounds      int    `json:"rounds"`
	IntervalMs  int    `json:"intervalMs"`
}

// ScanStarted is returned as soon as a run has been accepted.
type ScanStarted struct {
	Targets  int      `json:"targets"`
	Warnings []string `json:"warnings"`
}

// App exposes the detection engine to the frontend.
type App struct {
	ctx context.Context

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	rows    []scanner.Row
	index   map[string]int
	summary scanner.Summary

	// emit is swapped in tests; production runs use the Wails event bridge.
	emit func(event string, payload any)
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{index: make(map[string]int)}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown stops any run that is still in flight when the window closes.
func (a *App) shutdown(ctx context.Context) {
	a.StopScan()
}

// StartScan parses the request and probes every target in the background.
func (a *App) StartScan(req ScanRequest) (ScanStarted, error) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return ScanStarted{}, errors.New("已有检测任务在运行，请先停止")
	}
	targets, warnings, err := scanner.ParseTargets(req.Targets, req.Ports)
	if err != nil {
		a.mu.Unlock()
		return ScanStarted{}, err
	}

	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	a.running = true
	a.rows = nil
	a.index = make(map[string]int, len(targets))
	a.summary = scanner.Summary{}
	a.mu.Unlock()

	opts := scanner.Options{
		TimeoutMs:   req.TimeoutMs,
		Concurrency: req.Concurrency,
		Rounds:      req.Rounds,
		IntervalMs:  req.IntervalMs,
	}
	go a.run(ctx, targets, opts)

	return ScanStarted{Targets: len(targets), Warnings: warnings}, nil
}

func (a *App) run(ctx context.Context, targets []scanner.Target, opts scanner.Options) {
	hooks := scanner.Hooks{
		Row: func(row scanner.Row) {
			a.mu.Lock()
			if i, ok := a.index[row.Key]; ok {
				a.rows[i] = row
			} else {
				a.index[row.Key] = len(a.rows)
				a.rows = append(a.rows, row)
			}
			a.mu.Unlock()
			a.emitEvent("row", row)
		},
		Progress: func(executed, planned int, elapsed time.Duration) {
			a.emitEvent("progress", map[string]any{
				"executed":  executed,
				"planned":   planned,
				"elapsedMs": elapsed.Milliseconds(),
			})
		},
	}

	summary := scanner.Run(ctx, targets, opts, hooks)

	a.mu.Lock()
	a.running = false
	a.cancel = nil
	a.summary = summary
	rows := append([]scanner.Row(nil), a.rows...)
	a.mu.Unlock()

	a.emitEvent("done", map[string]any{
		"summary": summary,
		"rows":    rows,
	})
}

func (a *App) emitEvent(event string, payload any) {
	if a.emit != nil {
		a.emit(event, payload)
		return
	}
	runtime.EventsEmit(a.ctx, event, payload)
}

// StopScan cancels the run in progress.
func (a *App) StopScan() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ExportCSV writes the current result table to a file chosen by the user.
func (a *App) ExportCSV() (string, error) {
	a.mu.Lock()
	rows := append([]scanner.Row(nil), a.rows...)
	a.mu.Unlock()
	if len(rows) == 0 {
		return "", errors.New("还没有可导出的结果")
	}

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "导出检测结果",
		DefaultFilename: "btping_" + time.Now().Format("20060102_150405") + ".csv",
		Filters: []runtime.FileFilter{
			{DisplayName: "CSV 文件 (*.csv)", Pattern: "*.csv"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// The BOM keeps Excel from mangling Chinese headers.
	if _, err := file.WriteString("\uFEFF"); err != nil {
		return "", err
	}

	writer := csv.NewWriter(file)
	header := []string{"目标", "端口", "IP", "状态", "最近延迟(ms)", "最小延迟(ms)", "平均延迟(ms)", "最大延迟(ms)", "成功次数", "总次数", "丢包率(%)", "错误信息"}
	if err := writer.Write(header); err != nil {
		return "", err
	}
	for _, row := range rows {
		record := []string{
			row.Host,
			strconv.Itoa(row.Port),
			row.IP,
			statusLabel(row.Status),
			formatFloat(row.LastMs),
			formatFloat(row.MinMs),
			formatFloat(row.AvgMs),
			formatFloat(row.MaxMs),
			strconv.Itoa(row.Success),
			strconv.Itoa(row.Total),
			formatFloat(row.Loss),
			row.Error,
		}
		if err := writer.Write(record); err != nil {
			return "", err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}

	return path, nil
}

func formatFloat(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func statusLabel(status string) string {
	switch status {
	case scanner.StatusOpen:
		return "开放"
	case scanner.StatusRefused:
		return "关闭"
	case scanner.StatusTimeout:
		return "超时"
	case scanner.StatusUnresolved:
		return "解析失败"
	case scanner.StatusError:
		return "错误"
	case scanner.StatusResolving:
		return "解析中"
	case scanner.StatusPending:
		return "等待中"
	}
	return status
}

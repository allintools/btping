package scanner

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Probe status values reported to the UI.
const (
	StatusPending    = "pending"
	StatusResolving  = "resolving"
	StatusOpen       = "open"
	StatusRefused    = "refused"
	StatusTimeout    = "timeout"
	StatusError      = "error"
	StatusUnresolved = "unresolved"
)

const (
	defaultTimeoutMs   = 1500
	minTimeoutMs       = 100
	maxTimeoutMs       = 60000
	defaultConcurrency = 50
	minConcurrency     = 1
	maxConcurrency     = 1024
	defaultRounds      = 4
	maxRounds          = 1000
	defaultIntervalMs  = 200
	maxIntervalMs      = 60000
	maxResolvedIPs     = 8
)

// Options controls one detection run.
type Options struct {
	TimeoutMs   int `json:"timeoutMs"`
	Concurrency int `json:"concurrency"`
	Rounds      int `json:"rounds"`
	IntervalMs  int `json:"intervalMs"`
}

func (o Options) normalized() Options {
	o.TimeoutMs = clampInt(o.TimeoutMs, minTimeoutMs, maxTimeoutMs, defaultTimeoutMs)
	o.Concurrency = clampInt(o.Concurrency, minConcurrency, maxConcurrency, defaultConcurrency)
	o.Rounds = clampInt(o.Rounds, 1, maxRounds, defaultRounds)
	o.IntervalMs = clampInt(o.IntervalMs, 0, maxIntervalMs, defaultIntervalMs)
	return o
}

func clampInt(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Row is the live state of a single host:port pair.
type Row struct {
	Key       string  `json:"key"`
	Host      string  `json:"host"`
	Port      int     `json:"port"`
	IP        string  `json:"ip"`
	Status    string  `json:"status"`
	Error     string  `json:"error"`
	LastMs    float64 `json:"lastMs"`
	MinMs     float64 `json:"minMs"`
	AvgMs     float64 `json:"avgMs"`
	MaxMs     float64 `json:"maxMs"`
	Success   int     `json:"success"`
	Total     int     `json:"total"`
	Loss      float64 `json:"loss"`
	UpdatedAt int64   `json:"updatedAt"`
}

// Summary aggregates a finished run.
type Summary struct {
	Targets   int     `json:"targets"`
	Planned   int     `json:"planned"`
	Executed  int     `json:"executed"`
	Success   int     `json:"success"`
	Failed    int     `json:"failed"`
	Reachable int     `json:"reachable"`
	MinMs     float64 `json:"minMs"`
	AvgMs     float64 `json:"avgMs"`
	MaxMs     float64 `json:"maxMs"`
	ElapsedMs int64   `json:"elapsedMs"`
	Cancelled bool    `json:"cancelled"`
}

// Hooks receives streaming updates. Both callbacks are optional.
type Hooks struct {
	Row      func(Row)
	Progress func(executed, planned int, elapsed time.Duration)
}

type resolvedTarget struct {
	target Target
	ips    []string
}

type rowState struct {
	row Row
	sum float64
}

type engine struct {
	opts  Options
	hooks Hooks
	mu    sync.Mutex
	rows  map[string]*rowState
	order []string
}

// Run probes every target, streaming updates through hooks, and returns a summary.
func Run(ctx context.Context, targets []Target, opts Options, hooks Hooks) Summary {
	opts = opts.normalized()
	startedAt := time.Now()
	timeout := time.Duration(opts.TimeoutMs) * time.Millisecond
	planned := len(targets) * opts.Rounds

	eng := &engine{opts: opts, hooks: hooks, rows: make(map[string]*rowState, len(targets))}
	for _, t := range targets {
		eng.add(t)
	}
	eng.flush()

	var executed atomic.Int64
	progressStop := make(chan struct{})
	var progressWG sync.WaitGroup
	if hooks.Progress != nil {
		progressWG.Add(1)
		go func() {
			defer progressWG.Done()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-progressStop:
					return
				case <-ticker.C:
					hooks.Progress(int(executed.Load()), planned, time.Since(startedAt))
				}
			}
		}()
	}

	resolved := eng.resolveAll(ctx, targets, timeout, &executed)
	eng.probeAll(ctx, resolved, timeout, &executed)
	eng.finalize()

	close(progressStop)
	progressWG.Wait()
	if hooks.Progress != nil {
		hooks.Progress(int(executed.Load()), planned, time.Since(startedAt))
	}

	return eng.summary(planned, time.Since(startedAt), ctx.Err() != nil)
}

func (e *engine) add(t Target) {
	key := t.Key()
	if _, ok := e.rows[key]; ok {
		return
	}
	e.rows[key] = &rowState{row: Row{Key: key, Host: t.Host, Port: t.Port, Status: StatusPending, UpdatedAt: time.Now().UnixMilli()}}
	e.order = append(e.order, key)
}

func (e *engine) flush() {
	for _, key := range e.order {
		e.emit(key, nil)
	}
}

func (e *engine) emit(key string, mutate func(*rowState)) {
	e.mu.Lock()
	st, ok := e.rows[key]
	if !ok {
		e.mu.Unlock()
		return
	}
	if mutate != nil {
		mutate(st)
	}
	st.row.UpdatedAt = time.Now().UnixMilli()
	snapshot := st.row
	e.mu.Unlock()
	if e.hooks.Row != nil {
		e.hooks.Row(snapshot)
	}
}

func (e *engine) resolveAll(ctx context.Context, targets []Target, timeout time.Duration, executed *atomic.Int64) []resolvedTarget {
	limit := minInt(e.opts.Concurrency, 32)
	sem := make(chan struct{}, limit)
	var mu sync.Mutex
	var wg sync.WaitGroup
	byKey := make(map[string]resolvedTarget, len(targets))

	for _, t := range targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}

			e.emit(t.Key(), func(st *rowState) { st.row.Status = StatusResolving })

			ips, err := resolveHost(ctx, t.Host, timeout)
			if err != nil {
				e.emit(t.Key(), func(st *rowState) {
					st.row.Status = StatusUnresolved
					st.row.Error = friendlyResolveError(err)
					st.row.Total += e.opts.Rounds
					st.row.Loss = 100
				})
				executed.Add(int64(e.opts.Rounds))
				return
			}

			e.emit(t.Key(), func(st *rowState) { st.row.IP = ips[0] })
			mu.Lock()
			byKey[t.Key()] = resolvedTarget{target: t, ips: ips}
			mu.Unlock()
		}()
	}
	wg.Wait()

	out := make([]resolvedTarget, 0, len(byKey))
	for _, t := range targets {
		if rt, ok := byKey[t.Key()]; ok {
			out = append(out, rt)
		}
	}
	return out
}

func (e *engine) probeAll(ctx context.Context, targets []resolvedTarget, timeout time.Duration, executed *atomic.Int64) {
	if len(targets) == 0 {
		return
	}
	workers := minInt(e.opts.Concurrency, len(targets))
	jobs := make(chan resolvedTarget)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				for round := 0; round < e.opts.Rounds; round++ {
					if ctx.Err() != nil {
						return
					}
					e.probeOnce(ctx, target, timeout)
					executed.Add(1)
					if round < e.opts.Rounds-1 && e.opts.IntervalMs > 0 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(time.Duration(e.opts.IntervalMs) * time.Millisecond):
						}
					}
				}
			}
		}()
	}

sendLoop:
	for _, t := range targets {
		select {
		case jobs <- t:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
}

func (e *engine) probeOnce(ctx context.Context, target resolvedTarget, timeout time.Duration) {
	var (
		usedIP  string
		status  = StatusError
		message = "探测失败"
		lastMs  float64
		ok      bool
	)

	for i, ip := range target.ips {
		if ctx.Err() != nil {
			return
		}
		latency, err := dialOnce(ctx, ip, target.target.Port, timeout)
		if err == nil {
			usedIP, status, message, ok = ip, StatusOpen, "", true
			lastMs = toMillis(latency)
			break
		}
		if i == 0 {
			usedIP, lastMs = ip, toMillis(latency)
			status, message = classify(err)
		}
	}

	e.emit(target.target.Key(), func(st *rowState) {
		st.row.Total++
		st.row.IP = usedIP
		st.row.Status = status
		st.row.Error = message
		st.row.LastMs = lastMs
		if ok {
			st.row.Success++
			if st.row.Success == 1 || lastMs < st.row.MinMs {
				st.row.MinMs = lastMs
			}
			if lastMs > st.row.MaxMs {
				st.row.MaxMs = lastMs
			}
			st.sum += lastMs
			st.row.AvgMs = round3(st.sum / float64(st.row.Success))
		}
		if st.row.Total > 0 {
			st.row.Loss = round1(float64(st.row.Total-st.row.Success) / float64(st.row.Total) * 100)
		}
	})
}

func (e *engine) finalize() {
	var pending []string
	e.mu.Lock()
	for _, key := range e.order {
		st := e.rows[key]
		if st.row.Status == StatusPending || st.row.Status == StatusResolving {
			pending = append(pending, key)
		}
	}
	e.mu.Unlock()

	for _, key := range pending {
		e.emit(key, func(st *rowState) {
			st.row.Status = StatusError
			st.row.Error = "已取消"
		})
	}
}

func (e *engine) summary(planned int, elapsed time.Duration, cancelled bool) Summary {
	e.mu.Lock()
	defer e.mu.Unlock()

	s := Summary{Targets: len(e.order), Planned: planned, ElapsedMs: elapsed.Milliseconds(), Cancelled: cancelled}
	var sum float64
	var count int
	minSet := false
	for _, key := range e.order {
		st := e.rows[key]
		s.Executed += st.row.Total
		s.Success += st.row.Success
		if st.row.Success > 0 {
			s.Reachable++
		}
		if st.row.Success > 0 && st.row.MinMs > 0 {
			if !minSet || st.row.MinMs < s.MinMs {
				s.MinMs = st.row.MinMs
				minSet = true
			}
		}
		if st.row.MaxMs > s.MaxMs {
			s.MaxMs = st.row.MaxMs
		}
		sum += st.sum
		count += st.row.Success
	}
	if count > 0 {
		s.AvgMs = round3(sum / float64(count))
	}
	s.Failed = s.Executed - s.Success
	return s
}

func dialOnce(ctx context.Context, ip string, port int, timeout time.Duration) (time.Duration, error) {
	dialer := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	_ = conn.Close()
	return elapsed, nil
}

func resolveHost(ctx context.Context, host string, timeout time.Duration) ([]string, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []string{addr.String()}, nil
	}

	dnsTimeout := timeout
	if dnsTimeout < 2*time.Second {
		dnsTimeout = 2 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(addrs))
	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if !addr.IsValid() {
			continue
		}
		ip := addr.Unmap().String()
		if seen[ip] {
			continue
		}
		seen[ip] = true
		ips = append(ips, ip)
		if len(ips) >= maxResolvedIPs {
			break
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	return ips, nil
}

func classify(err error) (string, string) {
	if err == nil {
		return StatusOpen, ""
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return StatusRefused, "连接被拒绝"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return StatusRefused, "连接被重置"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return StatusTimeout, "超时"
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "refused"), strings.Contains(msg, "actively refused"):
		return StatusRefused, "连接被拒绝"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "timed out"):
		return StatusTimeout, "超时"
	case strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"), strings.Contains(msg, "network is down"):
		return StatusError, "网络不可达"
	case strings.Contains(msg, "no such host"):
		return StatusUnresolved, "域名解析失败"
	}
	return StatusError, shortenError(err.Error())
}

func friendlyResolveError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such host"):
		return "域名解析失败"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"):
		return "解析超时"
	}
	return "解析失败：" + shortenError(err.Error())
}

func shortenError(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		return msg[i+2:]
	}
	return msg
}

func toMillis(d time.Duration) float64 {
	return round3(float64(d) / float64(time.Millisecond))
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// round3 keeps microsecond precision so sub-millisecond LAN latency stays visible.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

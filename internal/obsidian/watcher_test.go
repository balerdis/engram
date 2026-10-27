package obsidian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Fake Exportable ─────────────────────────────────────────────────────────

// fakeExporter is a test-only Exportable implementation.
// It counts calls, records GraphConfig values, and can inject errors on specific cycles.
type fakeExporter struct {
	mu           sync.Mutex
	callCount    int
	callTimes    []time.Time
	graphConfigs []GraphConfigMode
	errOnCycle   map[int]error // 1-indexed: return this error on the Nth call
	result       *ExportResult // returned on success (defaults to zero ExportResult)
	config       ExportConfig  // mutable config for SetGraphConfig access
	onExport     func(n int)   // optional: called after each Export with its 1-indexed call number
}

func newFakeExporter() *fakeExporter {
	return &fakeExporter{
		errOnCycle: make(map[int]error),
		result:     &ExportResult{},
	}
}

func (f *fakeExporter) Export() (*ExportResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.callTimes = append(f.callTimes, time.Now())
	f.graphConfigs = append(f.graphConfigs, f.config.GraphConfig)
	if f.onExport != nil {
		defer f.onExport(f.callCount)
	}
	if err, ok := f.errOnCycle[f.callCount]; ok {
		return nil, err
	}
	r := *f.result
	return &r, nil
}

func (f *fakeExporter) SetGraphConfig(mode GraphConfigMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config.GraphConfig = mode
}

func (f *fakeExporter) GraphConfig() GraphConfigMode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.config.GraphConfig
}

func (f *fakeExporter) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func (f *fakeExporter) Times() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]time.Time, len(f.callTimes))
	copy(cp, f.callTimes)
	return cp
}

func (f *fakeExporter) GraphConfigs() []GraphConfigMode {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]GraphConfigMode, len(f.graphConfigs))
	copy(cp, f.graphConfigs)
	return cp
}

// ─── Task 3.1: TestWatcherRunsImmediatelyThenTicks ────────────────────────────

// TestWatcherRunsImmediatelyThenTicks verifies REQ-WATCH-03:
//   - the first cycle fires immediately, before any tick
//   - each later cycle fires on a tick, and only on a tick
//
// The ticker is driven by the test, so nothing depends on wall-clock timing.
func TestWatcherRunsImmediatelyThenTicks(t *testing.T) {
	const wait = 30 * time.Second // generous: only reached when the code is broken

	fe := newFakeExporter()
	fe.config.GraphConfig = GraphConfigForce
	cycles := make(chan int, 16)
	fe.onExport = func(n int) { cycles <- n }

	ticks := make(chan time.Time)
	var stopped atomic.Bool
	w := NewWatcher(WatcherConfig{Exporter: fe, Interval: time.Hour, Logf: func(string, ...any) {}})
	w.newTicker = func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() { stopped.Store(true) }
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitCycle := func(want int) {
		t.Helper()
		select {
		case got := <-cycles:
			if got != want {
				t.Fatalf("cycle %d ran, want %d", got, want)
			}
		case <-time.After(wait):
			t.Fatalf("cycle %d never ran", want)
		}
	}
	noCycle := func(msg string) {
		t.Helper()
		select {
		case got := <-cycles:
			t.Fatalf("%s: cycle %d ran without a tick", msg, got)
		default:
		}
	}

	// Immediate: no tick has been sent yet.
	waitCycle(1)
	noCycle("after the first cycle")

	// Then one cycle per tick (the send blocks until Run is back in its select).
	for want := 2; want <= 4; want++ {
		select {
		case ticks <- time.Now():
		case <-time.After(wait):
			t.Fatalf("Run never took tick %d", want-1)
		}
		waitCycle(want)
	}

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run() = %v, want context.Canceled", err)
		}
	case <-time.After(wait):
		t.Fatal("Run did not return after cancel")
	}
	if !stopped.Load() {
		t.Error("ticker was not stopped")
	}
	if n := fe.Count(); n != 4 {
		t.Errorf("exporter ran %d times, want 4", n)
	}
}

// ─── Task 3.3: TestWatcherLogsCycleResults ────────────────────────────────────

// TestWatcherLogsCycleResults verifies REQ-WATCH-04:
// each cycle must produce a log line with created=, updated=, deleted=, skipped=, hubs=
func TestWatcherLogsCycleResults(t *testing.T) {
	fe := newFakeExporter()
	fe.result = &ExportResult{
		Created:     5,
		Updated:     2,
		Deleted:     1,
		Skipped:     100,
		HubsCreated: 3,
	}

	var mu sync.Mutex
	var logLines []string
	captureLogf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		mu.Lock()
		logLines = append(logLines, line)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 10 * time.Millisecond,
		Logf:     captureLogf,
	})

	_ = w.Run(ctx)

	mu.Lock()
	lines := make([]string, len(logLines))
	copy(lines, logLines)
	mu.Unlock()

	if len(lines) == 0 {
		t.Fatal("no log lines captured")
	}

	for _, line := range lines {
		// Skip error lines
		if strings.Contains(line, "error") || strings.Contains(line, "Error") {
			continue
		}
		// Each success line must contain all required fields
		for _, field := range []string{"created=", "updated=", "deleted=", "skipped=", "hubs="} {
			if !strings.Contains(line, field) {
				t.Errorf("log line missing %q: %q", field, line)
			}
		}
	}

	t.Logf("captured %d log lines", len(lines))
	if len(lines) > 0 {
		t.Logf("sample log line: %q", lines[0])
	}
}

// ─── Task 3.5: TestWatcherContinuesOnError ────────────────────────────────────

// TestWatcherContinuesOnError verifies REQ-WATCH-04 error branch:
// when a cycle errors, the loop continues and the next cycle runs.
func TestWatcherContinuesOnError(t *testing.T) {
	fe := newFakeExporter()
	fe.errOnCycle[1] = errors.New("transient DB error")

	var mu sync.Mutex
	var logLines []string
	captureLogf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		mu.Lock()
		logLines = append(logLines, line)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 10 * time.Millisecond,
		Logf:     captureLogf,
	})

	_ = w.Run(ctx)

	count := fe.Count()
	if count < 2 {
		t.Errorf("expected at least 2 cycles (error on 1, continue to 2+), got %d", count)
	}

	// At least one log line must contain the error message
	mu.Lock()
	lines := make([]string, len(logLines))
	copy(lines, logLines)
	mu.Unlock()

	foundError := false
	for _, line := range lines {
		if strings.Contains(line, "transient DB error") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Errorf("expected error log line containing 'transient DB error', got lines: %v", lines)
	}

	t.Logf("total cycles after error-on-1: %d", count)
}

// ─── Task 3.7: TestWatcherGracefulShutdown ────────────────────────────────────

// TestWatcherGracefulShutdown verifies REQ-WATCH-05:
// canceling ctx mid-sleep causes Run() to return ctx.Err() without hanging.
func TestWatcherGracefulShutdown(t *testing.T) {
	fe := newFakeExporter()

	ctx, cancel := context.WithCancel(context.Background())

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 1 * time.Second, // long interval so we're in the sleep when canceled
	})

	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx)
	}()

	// Let the first cycle complete, then cancel
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Should return context.Canceled (or nil if impl returns nil on clean exit)
		if err != nil && err != context.Canceled {
			t.Errorf("Run() returned %v, want context.Canceled or nil", err)
		}
		t.Logf("Run() returned: %v", err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run() did not return after ctx cancel — potential goroutine leak")
	}
}

// ─── Task 3.9: TestWatcherFirstCycleGraphConfigOnly ──────────────────────────

// TestWatcherFirstCycleGraphConfigOnly verifies REQ-WATCH-06:
// - cycle 1 has the original GraphConfig value
// - cycle 2+ has GraphConfigSkip
func TestWatcherFirstCycleGraphConfigOnly(t *testing.T) {
	fe := newFakeExporter()
	fe.config.GraphConfig = GraphConfigForce // original value

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 10 * time.Millisecond,
	})

	_ = w.Run(ctx)

	configs := fe.GraphConfigs()
	if len(configs) < 2 {
		t.Fatalf("expected at least 2 cycles, got %d", len(configs))
	}

	// Cycle 1: must use original value (GraphConfigForce)
	if configs[0] != GraphConfigForce {
		t.Errorf("cycle 1 GraphConfig: got %q, want %q", configs[0], GraphConfigForce)
	}

	// Cycle 2+: must use GraphConfigSkip
	for i := 1; i < len(configs); i++ {
		if configs[i] != GraphConfigSkip {
			t.Errorf("cycle %d GraphConfig: got %q, want %q (GraphConfigSkip)", i+1, configs[i], GraphConfigSkip)
		}
	}

	t.Logf("GraphConfig per cycle: %v", configs)
}

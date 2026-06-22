// Package worker — feature #102 integration tests: arena-worker boot.
//
// These tests verify the three components added for the worker boot milestone:
//
//  1. OutboxBacklogPoller: ticker fires at least once, gauge is updated.
//  2. ShouldRunPlaceholderJob: function is registered and logs on invocation.
//  3. Graceful shutdown path: context cancel → poller exits cleanly.
//
// All tests run without a live database by injecting fakeBacklogQuerier.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------------------
// fakeBacklogQuerier — in-memory OutboxBacklogQuerier for tests.
// ---------------------------------------------------------------------------

type fakeBacklogQuerier struct {
	count    int64        // value returned by CountUndispatched
	calls    atomic.Int64 // number of times CountUndispatched has been called
	failOnce bool         // if true, first call returns error
	failed   atomic.Bool  // tracks whether failure has already been injected
}

func (f *fakeBacklogQuerier) CountUndispatched(_ context.Context) (int64, error) {
	f.calls.Add(1)
	if f.failOnce && !f.failed.Swap(true) {
		return 0, errors.New("transient db error")
	}
	return f.count, nil
}

// ---------------------------------------------------------------------------
// countingGauge — wraps a real prometheus.Gauge to capture Set() calls.
// ---------------------------------------------------------------------------

type countingGauge struct {
	prometheus.Gauge
	setCount atomic.Int64
	lastSet  atomic.Value // stores float64
}

func newCountingGauge() *countingGauge {
	return &countingGauge{
		Gauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "test_outbox_backlog",
			Help: "test gauge for OutboxBacklogPoller",
		}),
	}
}

func (c *countingGauge) Set(v float64) {
	c.lastSet.Store(v)
	c.setCount.Add(1)
	c.Gauge.Set(v)
}

func (c *countingGauge) getLastSet() float64 {
	if v := c.lastSet.Load(); v != nil {
		return v.(float64)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Step 1 + 5: Ticker fires at least once; gauge reflects querier result.
// ---------------------------------------------------------------------------

// TestOutboxBacklogPoller_FirstPollFiresImmediately verifies that Run polls
// once immediately without waiting for the first tick interval (step 2 of
// feature #102: "background cycle with ticker fires at least once").
func TestOutboxBacklogPoller_FirstPollFiresImmediately(t *testing.T) {
	t.Parallel()

	q := &fakeBacklogQuerier{count: 7}
	g := newCountingGauge()

	poller := NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier:      q,
		Gauge:        g,
		Logger:       slog.New(slog.NewTextHandler(nopWriter{}, nil)),
		PollInterval: 10 * time.Second, // very long; immediate poll must not depend on it
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = poller.Run(ctx)
		close(done)
	}()

	// Give it 200ms to fire the first poll synchronously in Run().
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if q.calls.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if q.calls.Load() < 1 {
		t.Fatal("step 2: expected at least one poll call within 200ms")
	}

	// Gauge must reflect the querier value.
	if got := g.getLastSet(); got != 7 {
		t.Fatalf("step 2: gauge expected 7, got %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("step 5: poller did not exit within 500ms after context cancel")
	}
}

// TestOutboxBacklogPoller_TickerFiresMultipleTimes verifies that after the
// first immediate poll, subsequent polls fire on the ticker interval.
func TestOutboxBacklogPoller_TickerFiresMultipleTimes(t *testing.T) {
	t.Parallel()

	q := &fakeBacklogQuerier{count: 3}
	g := newCountingGauge()

	poller := NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier:      q,
		Gauge:        g,
		Logger:       slog.New(slog.NewTextHandler(nopWriter{}, nil)),
		PollInterval: 20 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = poller.Run(ctx)
		close(done)
	}()

	// Wait for at least 3 calls (immediate + 2 ticks).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if q.calls.Load() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if q.calls.Load() < 3 {
		t.Fatalf("expected at least 3 poll calls within 500ms, got %d", q.calls.Load())
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Gauge receives real count from querier.
// ---------------------------------------------------------------------------

// TestOutboxBacklogPoller_GaugeReflectsCount verifies that after a poll the
// gauge records exactly the value returned by CountUndispatched (step 2).
func TestOutboxBacklogPoller_GaugeReflectsCount(t *testing.T) {
	t.Parallel()

	q := &fakeBacklogQuerier{count: 42}
	g := newCountingGauge()

	poller := NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier:      q,
		Gauge:        g,
		Logger:       slog.New(slog.NewTextHandler(nopWriter{}, nil)),
		PollInterval: 10 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = poller.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if g.setCount.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if g.setCount.Load() < 1 {
		t.Fatal("gauge was never updated")
	}
	if got := g.getLastSet(); got != 42 {
		t.Fatalf("gauge expected 42, got %v", got)
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Step 5: SIGTERM → clean exit (context cancel path).
// ---------------------------------------------------------------------------

// TestOutboxBacklogPoller_GracefulShutdown verifies that cancelling the
// context causes Run() to exit cleanly and return nil (step 5).
func TestOutboxBacklogPoller_GracefulShutdown(t *testing.T) {
	t.Parallel()

	q := &fakeBacklogQuerier{count: 0}
	g := newCountingGauge()

	poller := NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier:      q,
		Gauge:        g,
		Logger:       slog.New(slog.NewTextHandler(nopWriter{}, nil)),
		PollInterval: 50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- poller.Run(ctx) }()

	// Let it run for a couple of ticks.
	time.Sleep(120 * time.Millisecond)

	// Simulate SIGTERM via context cancel.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("step 5: Run() returned non-nil error on graceful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("step 5: Run() did not return within 2s after context cancel")
	}
}

// ---------------------------------------------------------------------------
// Transient DB error: gauge left unchanged, poller continues.
// ---------------------------------------------------------------------------

// TestOutboxBacklogPoller_TransientErrorKeepsRunning verifies that a single
// CountUndispatched failure does NOT abort the poll loop (step 2 resilience).
func TestOutboxBacklogPoller_TransientErrorKeepsRunning(t *testing.T) {
	t.Parallel()

	q := &fakeBacklogQuerier{count: 5, failOnce: true}
	g := newCountingGauge()

	poller := NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier:      q,
		Gauge:        g,
		Logger:       slog.New(slog.NewTextHandler(nopWriter{}, nil)),
		PollInterval: 20 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = poller.Run(ctx)
		close(done)
	}()

	// Wait for at least 3 calls (first call fails, subsequent succeed).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if q.calls.Load() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if q.calls.Load() < 3 {
		t.Fatalf("expected at least 3 calls (loop must survive failure), got %d", q.calls.Load())
	}

	// After the failure passes, the gauge should reflect the real count.
	if got := g.getLastSet(); got != 5 {
		t.Fatalf("gauge expected 5 after recovery, got %v", got)
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Step 3: ShouldRunPlaceholderJob function.
// ---------------------------------------------------------------------------

// TestShouldRunPlaceholderJob_Exists verifies that ShouldRunPlaceholderJob
// is exported from the worker package (step 3 of feature #102).
func TestShouldRunPlaceholderJob_Exists(t *testing.T) {
	t.Parallel()

	// ShouldRunPlaceholderJob must be callable and must always return true
	// (it is a stub that says "yes, run a placeholder job").
	if !ShouldRunPlaceholderJob() {
		t.Fatal("step 3: ShouldRunPlaceholderJob() must return true")
	}
}

// TestShouldRunPlaceholderJob_LogsWhenInvoked verifies that calling the
// placeholder log handler emits at least one log record (step 3).
func TestShouldRunPlaceholderJob_LogsWhenInvoked(t *testing.T) {
	t.Parallel()

	lc := &logCapture{} // reuse logCapture from worker_graceful_shutdown_test.go
	logger := slog.New(lc)

	handler := PlaceholderJobHandler(logger)
	err := handler(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("step 3: placeholder handler returned error: %v", err)
	}
	if len(lc.records) == 0 {
		t.Fatal("step 3: placeholder handler did not produce any log record")
	}
}

// ---------------------------------------------------------------------------
// Constructor guards.
// ---------------------------------------------------------------------------

// TestNewOutboxBacklogPoller_NilQuerierPanics ensures a nil Querier is
// caught early at construction time.
func TestNewOutboxBacklogPoller_NilQuerierPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil Querier")
		}
	}()

	NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier: nil,
		Gauge:   newCountingGauge(),
	})
}

// TestNewOutboxBacklogPoller_NilGaugePanics ensures a nil Gauge is caught.
func TestNewOutboxBacklogPoller_NilGaugePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil Gauge")
		}
	}()

	NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier: &fakeBacklogQuerier{},
		Gauge:   nil,
	})
}

// TestNewOutboxBacklogPoller_DefaultPollInterval verifies that a zero
// PollInterval is replaced by DefaultOutboxBacklogPollInterval.
func TestNewOutboxBacklogPoller_DefaultPollInterval(t *testing.T) {
	t.Parallel()

	p := NewOutboxBacklogPoller(OutboxBacklogPollerOptions{
		Querier: &fakeBacklogQuerier{},
		Gauge:   newCountingGauge(),
	})

	if p.opts.PollInterval != DefaultOutboxBacklogPollInterval {
		t.Fatalf("expected default poll interval %v, got %v",
			DefaultOutboxBacklogPollInterval, p.opts.PollInterval)
	}
}

// TestPGOutboxBacklogQuerier_CompileTimeInterfaceGuard confirms the production
// implementation satisfies the interface at compile time (no runtime test
// needed; if the file compiles this test passes automatically).
func TestPGOutboxBacklogQuerier_CompileTimeInterfaceGuard(t *testing.T) {
	t.Parallel()
	var _ OutboxBacklogQuerier = (*PGOutboxBacklogQuerier)(nil)
}

// ---------------------------------------------------------------------------
// nopWriter — discards all log output in tests.
// ---------------------------------------------------------------------------

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

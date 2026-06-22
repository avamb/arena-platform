// Package worker — feature #27: arena-worker graceful shutdown.
//
// These tests verify the graceful-shutdown contract of the Worker:
//
//  1. A slow in-flight job is allowed to complete before the worker exits.
//  2. The correct log messages appear in order:
//     "shutdown initiated, finishing 1 claimed job"  (while job still runs)
//     "shutdown complete"                            (after job finishes)
//  3. No new jobs are claimed after Stop() is called.
//  4. Stop() returns nil (exit-code-0 equivalent).
//  5. A job that overruns shutdownTimeout remains status='claimed' with
//     claimed_by set (a separate reaper resets it — step 8 of the spec).
//
// All tests use inMemoryQueue (defined in worker_jobs_persistence_test.go in
// the same package) so no live database is required.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// logCapture — slog.Handler that stores all log records for later assertions.
// ---------------------------------------------------------------------------

type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

// Compile-time guard: logCapture must satisfy slog.Handler.
var _ slog.Handler = (*logCapture)(nil)

func (lc *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (lc *logCapture) Handle(_ context.Context, r slog.Record) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.records = append(lc.records, r)
	return nil
}

func (lc *logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return lc }
func (lc *logCapture) WithGroup(_ string) slog.Handler      { return lc }

// hasMessage returns true if any captured record has exactly the given message.
func (lc *logCapture) hasMessage(msg string) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, r := range lc.records {
		if r.Message == msg {
			return true
		}
	}
	return false
}

// waitForMessage polls until the message appears in captured records or timeout elapses.
func (lc *logCapture) waitForMessage(t *testing.T, msg string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if lc.hasMessage(msg) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// Feature #27 — primary test: slow job completes, correct log messages
// ---------------------------------------------------------------------------

// TestGracefulShutdown_SlowJobCompletes covers steps 1-7 and 9 of feature #27.
//
//   Step 1: Enqueue a slow job (test.slow) and a second pending job (noop.test).
//   Step 2: Start worker; poll until slow job is claimed.
//   Step 3: Call Stop() to simulate SIGTERM.
//   Step 4: Verify log "shutdown initiated, finishing 1 claimed job".
//   Step 5: Signal slow handler to complete; verify status='done'.
//   Step 6: Verify second pending job was NOT claimed after Stop().
//   Step 7: Verify Stop() returns nil (exit code 0 equivalent).
//   Step 9: Verify log "shutdown complete" appears within shutdown_timeout.
func TestGracefulShutdown_SlowJobCompletes(t *testing.T) {
	t.Parallel()

	lc := &logCapture{}
	logger := slog.New(lc)

	q := newInMemoryQueue()
	reg := NewRegistry()

	// STEP 1: Register test.slow handler that blocks until signalled.
	handlerReady      := make(chan struct{})
	handlerFinish     := make(chan struct{})
	handlerReadyOnce  := sync.Once{}
	reg.Register("test.slow", func(_ context.Context, _ []byte) error {
		handlerReadyOnce.Do(func() { close(handlerReady) }) // signal entry
		<-handlerFinish                                      // block until told to finish
		return nil
	})

	// STEP 1: Enqueue slow job.
	slowJobID := q.insert("test.slow", []byte(`{}`), time.Time{}, 1)

	// STEP 6 setup: second pending job must NOT be claimed after SIGTERM.
	pendingJobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 1)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		Logger:          logger,
		InstanceID:      "test-graceful-01",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = w.Run(ctx) }()

	// STEP 2: Wait until slow job is claimed.
	if !pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		r := q.get(slowJobID)
		return r != nil && r.status == "claimed"
	}) {
		t.Fatal("step 2: slow job was not claimed within timeout")
	}

	// Wait for slow handler to enter the blocking receive.
	select {
	case <-handlerReady:
	case <-time.After(2 * time.Second):
		t.Fatal("step 2: slow handler did not start within timeout")
	}

	// STEP 3: Simulate SIGTERM by calling Stop() in a goroutine.
	stopErrCh := make(chan error, 1)
	go func() { stopErrCh <- w.Stop() }()

	// STEP 4: Verify "shutdown initiated, finishing 1 claimed job" is logged.
	if !lc.waitForMessage(t, "shutdown initiated, finishing 1 claimed job", 2*time.Second) {
		t.Fatal("step 4: expected log 'shutdown initiated, finishing 1 claimed job'")
	}

	// STEP 5: Signal the slow handler to complete, then verify status='done'.
	close(handlerFinish)

	if !pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		r := q.get(slowJobID)
		return r != nil && r.status == "done"
	}) {
		t.Fatal("step 5: slow job did not reach status=done after handler was signalled")
	}

	// STEP 7: Verify Stop() returns nil (exit code 0 equivalent).
	select {
	case err := <-stopErrCh:
		if err != nil {
			t.Fatalf("step 7: Stop() returned error (expected nil): %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("step 7: Stop() did not return within timeout")
	}

	// STEP 6: Verify pending job was NOT claimed after SIGTERM.
	pending := q.get(pendingJobID)
	if pending == nil {
		t.Fatal("step 6: second pending job row disappeared unexpectedly")
	}
	if pending.status != "pending" {
		t.Fatalf("step 6: expected second job to remain pending after shutdown; got status=%s", pending.status)
	}

	// STEP 9: Verify "shutdown complete" appears in logs.
	if !lc.hasMessage("shutdown complete") {
		t.Fatal("step 9: expected log 'shutdown complete'")
	}
}

// ---------------------------------------------------------------------------
// Context-cancel path (main.go SIGTERM via signal.NotifyContext)
// ---------------------------------------------------------------------------

// TestGracefulShutdown_CtxCancelWhileJobRunning verifies the same graceful
// behaviour when the parent context is cancelled (the production SIGTERM path
// goes through signal.NotifyContext, which cancels rootCtx).
func TestGracefulShutdown_CtxCancelWhileJobRunning(t *testing.T) {
	t.Parallel()

	lc := &logCapture{}
	logger := slog.New(lc)

	q := newInMemoryQueue()
	reg := NewRegistry()

	handlerReady     := make(chan struct{})
	handlerFinish    := make(chan struct{})
	handlerReadyOnce := sync.Once{}
	reg.Register("test.slow", func(_ context.Context, _ []byte) error {
		handlerReadyOnce.Do(func() { close(handlerReady) })
		<-handlerFinish
		return nil
	})

	slowJobID := q.insert("test.slow", []byte(`{}`), time.Time{}, 1)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		Logger:          logger,
		InstanceID:      "test-graceful-ctx-cancel",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- w.Run(ctx) }()

	// Wait until slow job is claimed.
	if !pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		r := q.get(slowJobID)
		return r != nil && r.status == "claimed"
	}) {
		t.Fatal("slow job was not claimed within timeout")
	}

	select {
	case <-handlerReady:
	case <-time.After(2 * time.Second):
		t.Fatal("slow handler did not start")
	}

	// Simulate SIGTERM: cancel the parent context (as signal.NotifyContext does).
	cancel()

	// Verify "shutdown initiated" log fires while handler is still blocking.
	if !lc.waitForMessage(t, "shutdown initiated, finishing 1 claimed job", 2*time.Second) {
		t.Fatal("expected log 'shutdown initiated, finishing 1 claimed job'")
	}

	// Signal slow handler to finish.
	close(handlerFinish)

	// Verify job completes.
	if !pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		r := q.get(slowJobID)
		return r != nil && r.status == "done"
	}) {
		t.Fatal("slow job did not reach status=done")
	}

	// Run() should exit cleanly.
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not exit within timeout after context cancel")
	}

	// Verify "shutdown complete" logged.
	if !lc.hasMessage("shutdown complete") {
		t.Fatal("expected log 'shutdown complete'")
	}

	q.remove(slowJobID)
}

// ---------------------------------------------------------------------------
// Idle worker — no job in-flight at shutdown
// ---------------------------------------------------------------------------

// TestGracefulShutdown_NoJobInFlight verifies that Stop() on an idle worker
// logs "shutdown complete" but does NOT log "shutdown initiated" (there is
// nothing to drain).
func TestGracefulShutdown_NoJobInFlight(t *testing.T) {
	t.Parallel()

	lc := &logCapture{}
	logger := slog.New(lc)

	q := newInMemoryQueue() // empty queue
	reg := NewRegistry()

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		Logger:          logger,
		InstanceID:      "test-graceful-idle",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Let the worker loop a few empty-queue cycles.
	time.Sleep(50 * time.Millisecond)

	if err := w.Stop(); err != nil {
		t.Fatalf("Stop() returned error: %v", err)
	}

	// "shutdown initiated" must NOT appear — no job was in-flight.
	if lc.hasMessage("shutdown initiated, finishing 1 claimed job") {
		t.Fatal("expected no 'shutdown initiated' log when no job is in-flight")
	}

	// "shutdown complete" MUST appear.
	if !lc.hasMessage("shutdown complete") {
		t.Fatal("expected 'shutdown complete' log even when worker was idle")
	}
}

// ---------------------------------------------------------------------------
// Timeout scenario — step 8: unfinished job stays 'claimed'
// ---------------------------------------------------------------------------

// TestGracefulShutdown_UnfinishedJobStaysClaimedOnTimeout covers step 8:
// when a job overruns shutdownTimeout, Stop() returns context.DeadlineExceeded
// and the job row remains status='claimed' with claimed_by set (a separate
// reaper process is responsible for resetting such rows).
func TestGracefulShutdown_UnfinishedJobStaysClaimedOnTimeout(t *testing.T) {
	t.Parallel()

	lc := &logCapture{}
	logger := slog.New(lc)

	q := newInMemoryQueue()
	reg := NewRegistry()

	// handlerCtx is cancelled by t.Cleanup so the goroutine exits when the
	// test ends, preventing a goroutine leak.
	handlerCtx, handlerCancel := context.WithCancel(context.Background())
	t.Cleanup(handlerCancel)

	handlerReady     := make(chan struct{})
	handlerReadyOnce := sync.Once{}
	reg.Register("test.slow", func(_ context.Context, _ []byte) error {
		handlerReadyOnce.Do(func() { close(handlerReady) })
		// Block until test cleanup — simulates a job that overruns shutdown_timeout.
		<-handlerCtx.Done()
		return nil
	})

	slowJobID := q.insert("test.slow", []byte(`{}`), time.Time{}, 1)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		Logger:          logger,
		InstanceID:      "test-graceful-timeout",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 100 * time.Millisecond, // very short for this test
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()

	// Wait for job to be claimed.
	if !pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		r := q.get(slowJobID)
		return r != nil && r.status == "claimed"
	}) {
		t.Fatal("slow job was not claimed")
	}

	select {
	case <-handlerReady:
	case <-time.After(2 * time.Second):
		t.Fatal("slow handler did not start")
	}

	// Stop() should return context.DeadlineExceeded after the 100ms timeout.
	stopErr := w.Stop()
	if stopErr == nil {
		t.Fatal("step 8: expected Stop() to return an error when job overruns timeout")
	}

	// STEP 8: Verify job remains status='claimed' with claimed_by set.
	row := q.get(slowJobID)
	if row == nil {
		t.Fatal("step 8: job row disappeared")
	}
	if row.status != "claimed" {
		t.Fatalf("step 8: expected status=claimed for overrunning job, got %s", row.status)
	}
	if row.claimedBy == "" {
		t.Fatal("step 8: claimed_by must be non-empty on an overrunning job")
	}
	if row.claimedBy != "test-graceful-timeout" {
		t.Fatalf("step 8: expected claimed_by=%q, got %q", "test-graceful-timeout", row.claimedBy)
	}

	_ = lc
}

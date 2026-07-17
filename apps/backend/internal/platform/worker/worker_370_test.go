// Package worker — feature #370: stale-claim reaper and retry backoff.
//
// These tests verify:
//  1. ReclaimStale resets status='claimed' rows whose claimed_at is beyond the
//     visibility timeout back to status='pending'.
//  2. ReclaimStale does NOT touch rows within the timeout window.
//  3. MarkRetry sets scheduled_at to the supplied backoff time (the row is not
//     immediately re-claimable).
//  4. A simulated crash (job left in status='claimed') is reclaimed by the
//     reaper and eventually completes when a new worker starts.
//  5. Worker.Options.StaleClaimTimeout and StaleClaimInterval defaults are sane.
//
// All tests use inMemoryQueue (defined in worker_jobs_persistence_test.go) so
// no live PostgreSQL connection is needed.
package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Step 1: ReclaimStale resets a stale claimed row to pending
// ---------------------------------------------------------------------------

// TestReclaimStale_ResetsStaleClaimedToPending verifies that a row with
// status='claimed' and claimed_at older than the visibility timeout is
// reset to status='pending' by ReclaimStale.
func TestReclaimStale_ResetsStaleClaimedToPending(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()

	// Insert a job and manually force it into a stale-claimed state.
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	// Simulate a worker claiming the job (sets status='claimed', claimedAt=now).
	q.mu.Lock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "claimed"
			// Back-date claimed_at to simulate a crash 10 minutes ago.
			staleClaim := time.Now().Add(-10 * time.Minute)
			r.claimedAt = &staleClaim
			r.claimedBy = "crashed-worker"
			r.attempts = 1
		}
	}
	q.mu.Unlock()

	// Row must be claimed before reaping.
	row := q.get(jobID)
	if row == nil || row.status != "claimed" {
		t.Fatalf("pre-condition: expected status=claimed, got %v", row)
	}

	// Reclaim with a 5-minute timeout: the 10-minute-old claim is stale.
	n, err := q.ReclaimStale(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row reclaimed, got %d", n)
	}

	row = q.get(jobID)
	if row == nil {
		t.Fatal("job row not found after reclaim")
	}
	if row.status != "pending" {
		t.Fatalf("expected status=pending after reclaim, got %s", row.status)
	}
	if row.claimedAt != nil {
		t.Fatalf("expected claimed_at=nil after reclaim, got %v", row.claimedAt)
	}
	if row.claimedBy != "" {
		t.Fatalf("expected claimed_by='' after reclaim, got %q", row.claimedBy)
	}
}

// ---------------------------------------------------------------------------
// Step 2: ReclaimStale does not touch rows within the window
// ---------------------------------------------------------------------------

// TestReclaimStale_DoesNotTouchFreshClaims verifies that a row with
// status='claimed' and claimed_at within the visibility timeout is left
// untouched by ReclaimStale.
func TestReclaimStale_DoesNotTouchFreshClaims(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()

	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	// Mark as claimed NOW (fresh).
	q.mu.Lock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "claimed"
			now := time.Now()
			r.claimedAt = &now
			r.claimedBy = "active-worker"
			r.attempts = 1
		}
	}
	q.mu.Unlock()

	// Reclaim with a 5-minute timeout: a fresh claim must NOT be touched.
	n, err := q.ReclaimStale(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows reclaimed for fresh claim, got %d", n)
	}

	row := q.get(jobID)
	if row == nil || row.status != "claimed" {
		t.Fatalf("fresh-claimed row must remain claimed; got %v", row)
	}
}

// ---------------------------------------------------------------------------
// Step 3: MarkRetry sets scheduled_at to the backoff time
// ---------------------------------------------------------------------------

// TestMarkRetry_SetsScheduledAtForBackoff verifies that after MarkRetry the
// row's scheduled_at is set to the supplied scheduledAt value, so the worker
// only re-claims the job after the backoff window has elapsed.
func TestMarkRetry_SetsScheduledAtForBackoff(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	// Advance job to claimed state.
	q.mu.Lock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "claimed"
			now := time.Now()
			r.claimedAt = &now
			r.attempts = 1
		}
	}
	q.mu.Unlock()

	// MarkRetry with a 30-second backoff.
	backoff := 30 * time.Second
	scheduledAt := time.Now().Add(backoff)
	if err := q.MarkRetry(context.Background(), jobID, "test error", scheduledAt); err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job not found after MarkRetry")
	}
	if row.status != "pending" {
		t.Fatalf("expected status=pending, got %s", row.status)
	}
	// scheduled_at must be at or after the requested backoff target.
	if row.scheduledAt.Before(scheduledAt.Add(-time.Second)) {
		t.Fatalf("expected scheduled_at >= %v, got %v", scheduledAt, row.scheduledAt)
	}
	// And the job must NOT be claimable yet (scheduled_at is in the future).
	claimed, err := q.ClaimNext(context.Background(), "test-worker")
	if err != nil {
		t.Fatalf("ClaimNext after MarkRetry: %v", err)
	}
	if claimed != nil {
		t.Fatalf("expected no claimable job during backoff window; got %+v", claimed)
	}
}

// ---------------------------------------------------------------------------
// Step 4: Simulated crash is recovered by the reaper and job completes
// ---------------------------------------------------------------------------

// TestStaleClaimReaper_RecoveredJobEventuallyCompletes simulates a worker
// crash by inserting a pre-claimed row with an old claimed_at, then starts a
// Worker with a short StaleClaimTimeout and StaleClaimInterval. The reaper
// goroutine must notice the stale row, reset it to 'pending', and the active
// worker must then pick it up and complete it.
func TestStaleClaimReaper_RecoveredJobEventuallyCompletes(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()

	var handlerCalls atomic.Int32
	reg.Register("noop.test", func(_ context.Context, _ []byte) error {
		handlerCalls.Add(1)
		return nil
	})

	// Insert a job.
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	// Simulate a crashed worker: set the job as stale-claimed.
	q.mu.Lock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "claimed"
			// Back-date by 2× the visibility timeout to guarantee reclaim.
			stale := time.Now().Add(-200 * time.Millisecond)
			r.claimedAt = &stale
			r.claimedBy = "crashed-worker/pid-99"
			r.attempts = 1
		}
	}
	q.mu.Unlock()

	// Verify pre-condition: job is stuck in claimed state.
	if row := q.get(jobID); row == nil || row.status != "claimed" {
		t.Fatalf("pre-condition: expected status=claimed")
	}

	// Start a worker with a very short stale-claim window (50ms timeout,
	// 25ms interval) so the reaper fires quickly in the test.
	w, err := New(Options{
		Queue:              q,
		Registry:           reg,
		InstanceID:         "worker-reaper-test",
		PollInterval:       5 * time.Millisecond,
		StaleClaimTimeout:  50 * time.Millisecond,
		StaleClaimInterval: 25 * time.Millisecond,
		ShutdownTimeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for the job to transition from 'claimed' (stale) to 'done'
	// via the reaper path. Allow up to 5 seconds.
	if !waitForStatus(q, jobID, "done", 1, 5*time.Second) {
		row := q.get(jobID)
		t.Fatalf("stale-claimed job did not reach done; row=%+v handler_calls=%d",
			row, handlerCalls.Load())
	}

	// The handler must have been called at least once after reclaim.
	if handlerCalls.Load() == 0 {
		t.Fatal("handler was never called after stale-claim reclaim")
	}
}

// ---------------------------------------------------------------------------
// Step 5: Worker defaults for stale-claim options are sane
// ---------------------------------------------------------------------------

// TestWorkerOptions_StaleClaimDefaults verifies that when StaleClaimTimeout
// and StaleClaimInterval are not set, the Worker fills in sensible defaults:
// 5m timeout and 2.5m interval (half of timeout).
func TestWorkerOptions_StaleClaimDefaults(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	w, err := New(Options{
		Queue:    q,
		Registry: reg,
		// Intentionally omit StaleClaimTimeout and StaleClaimInterval.
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	if w.staleClaimTimeout != 5*time.Minute {
		t.Errorf("expected staleClaimTimeout=5m, got %v", w.staleClaimTimeout)
	}
	// Interval defaults to half the timeout.
	if w.staleClaimInterval != 2*time.Minute+30*time.Second {
		t.Errorf("expected staleClaimInterval=2m30s, got %v", w.staleClaimInterval)
	}
}

// ---------------------------------------------------------------------------
// Step 5b: Worker correctly propagates explicit StaleClaimTimeout/Interval
// ---------------------------------------------------------------------------

// TestWorkerOptions_StaleClaimExplicitValues verifies that explicitly provided
// StaleClaimTimeout and StaleClaimInterval are respected by the Worker.
func TestWorkerOptions_StaleClaimExplicitValues(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	w, err := New(Options{
		Queue:              q,
		Registry:           reg,
		StaleClaimTimeout:  3 * time.Minute,
		StaleClaimInterval: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	if w.staleClaimTimeout != 3*time.Minute {
		t.Errorf("expected staleClaimTimeout=3m, got %v", w.staleClaimTimeout)
	}
	if w.staleClaimInterval != 30*time.Second {
		t.Errorf("expected staleClaimInterval=30s, got %v", w.staleClaimInterval)
	}
}

// ---------------------------------------------------------------------------
// Full scenario: crash + reaper + retry backoff + eventual completion
// ---------------------------------------------------------------------------

// TestWorker_CrashRecoveryWithRetryBackoff is the canonical end-to-end test
// for feature #370. It exercises:
//
//  1. A job is inserted and a "crashed worker" is simulated by forcing the
//     row into a stale-claimed state.
//  2. A new Worker starts with stale-claim reaper enabled.
//  3. The reaper detects the stranded row and resets it to pending.
//  4. The Worker claims the job, the handler fails once.
//  5. MarkRetry sets scheduled_at to now+retryBackoff (job not immediately
//     re-claimable).
//  6. After the backoff elapses the Worker claims and completes the job.
func TestWorker_CrashRecoveryWithRetryBackoff(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()

	// Handler fails on the first call, succeeds on the second.
	var callCount atomic.Int32
	reg.Register("flaky.test", func(_ context.Context, _ []byte) error {
		n := callCount.Add(1)
		if n == 1 {
			return errors.New("transient error on first attempt after reclaim")
		}
		return nil
	})

	// Insert job.
	jobID := q.insert("flaky.test", []byte(`{}`), time.Time{}, 5)

	// Simulate crash: force stale-claimed state.
	q.mu.Lock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "claimed"
			stale := time.Now().Add(-200 * time.Millisecond)
			r.claimedAt = &stale
			r.claimedBy = "dead-worker/pid-1"
			r.attempts = 1
		}
	}
	q.mu.Unlock()

	// Worker with short stale-claim window and short retry backoff.
	w, err := New(Options{
		Queue:              q,
		Registry:           reg,
		InstanceID:         "worker-crash-recovery",
		PollInterval:       5 * time.Millisecond,
		RetryBackoff:       30 * time.Millisecond,
		StaleClaimTimeout:  50 * time.Millisecond,
		StaleClaimInterval: 25 * time.Millisecond,
		ShutdownTimeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Job must eventually reach status='done'.
	if !waitForStatus(q, jobID, "done", 2, 10*time.Second) {
		row := q.get(jobID)
		t.Fatalf("job did not reach done after crash-recovery cycle; row=%+v calls=%d",
			row, callCount.Load())
	}

	// Handler must have been called at least twice: once after reclaim
	// (fails) and once after the retry backoff (succeeds).
	if callCount.Load() < 2 {
		t.Fatalf("expected handler called >=2 times, got %d", callCount.Load())
	}

	cancel()
	_ = w.Stop()
}

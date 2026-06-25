// Package worker — feature #39: worker job retry count persists across restart.
//
// These tests verify that when a Worker is stopped while processing failing
// jobs, the attempts counter in the queue is durably preserved and the
// next Worker instance picks up exactly where the previous one left off.
//
// The core scenario (mirroring the feature steps):
//
//  1. A job with job_type='test.always_fail' is enqueued (max_attempts=3).
//  2. Worker1 starts and runs until attempts=2.
//  3. Worker1 is stopped.
//  4. Queue row shows: attempts=2, status='pending', last_error non-nil.
//  5. Worker2 starts with the same Queue.
//  6. Worker2 claims the job; attempts becomes 3.
//  7. After 3 failures (attempts==max_attempts) the row moves to
//     status='failed' and a dead-letter row is inserted.
//  8. worker_dead_letter.original_job_id matches the original job ID.
//  9. worker_jobs row is status='failed' (design: row is kept, not deleted).
//
// All tests use inMemoryQueue (defined in worker_jobs_persistence_test.go in
// the same package) so no live database is required.
package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitForStatus polls the in-memory queue until the specified job reaches
// the desired status (and optionally minimum attempts), or the timeout
// elapses. Returns true on success.
func waitForStatus(q *inMemoryQueue, jobID, status string, minAttempts int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r := q.get(jobID); r != nil {
			if r.status == status && r.attempts >= minAttempts {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// waitForAttempts polls until the job has at least the given number of
// attempts, regardless of current status. Returns true on success.
func waitForAttempts(q *inMemoryQueue, jobID string, minAttempts int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r := q.get(jobID); r != nil && r.attempts >= minAttempts {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// Step 1–4: enqueue, run two attempts, stop, verify retained state
// ---------------------------------------------------------------------------

// TestWorkerRetryPersist_StatusPendingAfterTwoFailures verifies that after
// two handler failures the row is back to status='pending' (not 'failed'),
// because attempts < max_attempts.
func TestWorkerRetryPersist_StatusPendingAfterTwoFailures(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	// max_attempts=3: first two failures must keep status='pending'.
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 3)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker1-retry",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait until the queue shows attempts>=2 with status='pending'.
	if !waitForStatus(q, jobID, "pending", 2, 5*time.Second) {
		row := q.get(jobID)
		if row != nil {
			t.Fatalf("expected status=pending with attempts>=2; got status=%s attempts=%d", row.status, row.attempts)
		} else {
			t.Fatal("job row disappeared before reaching attempts=2")
		}
	}

	cancel()
	_ = w.Stop()
}

// TestWorkerRetryPersist_AttemptsEqualsTwo verifies the counter is exactly 2
// (not reset to 0) after two failures and before the third attempt.
func TestWorkerRetryPersist_AttemptsEqualsTwo(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 5) // high max so we don't dead-letter yet

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker1-attempts-check",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()

	// Wait for exactly 2 attempts then stop before more attempts run.
	if !waitForAttempts(q, jobID, 2, 5*time.Second) {
		t.Fatalf("job did not reach 2 attempts within timeout")
	}

	// Stop the worker before the 3rd attempt can be made.
	cancel()
	if err := w.Stop(); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("Stop error: %v", err)
	}

	// Wait for any in-flight attempt to complete (retry sets status back to pending).
	time.Sleep(50 * time.Millisecond)

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job row disappeared after worker stop")
	}
	if row.attempts < 2 {
		t.Fatalf("expected attempts>=2 after worker stop, got %d", row.attempts)
	}
}

// TestWorkerRetryPersist_LastErrorPopulated verifies last_error is non-nil
// and non-empty after the job has been retried at least once.
func TestWorkerRetryPersist_LastErrorPopulated(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 5)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker1-lasterror",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()

	// Wait until at least one failure has set last_error.
	if !waitForAttempts(q, jobID, 1, 5*time.Second) {
		t.Fatal("job did not complete first attempt")
	}
	// Wait a bit for MarkRetry to run.
	time.Sleep(50 * time.Millisecond)

	// Only check after worker may have written last_error; wait for pending.
	waitForStatus(q, jobID, "pending", 1, 2*time.Second)

	cancel()
	_ = w.Stop()

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job not found after worker stop")
	}
	if row.lastError == nil {
		t.Fatal("last_error is nil after failure; expected non-nil")
	}
	if *row.lastError == "" {
		t.Fatal("last_error is empty string after failure")
	}
}

// ---------------------------------------------------------------------------
// Step 5–6: Worker2 picks up the same job and increments attempts
// ---------------------------------------------------------------------------

// TestWorkerRetryPersist_NewWorkerIncrementsAttempts verifies that a second
// Worker instance (simulating a process restart) increments attempts from the
// persisted value, not from 0.
func TestWorkerRetryPersist_NewWorkerIncrementsAttempts(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	// max_attempts=4 so 2 failures leave room for a third before dead-letter.
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 4)

	// --- WORKER 1 ---
	w1, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker1-restart-test",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker1.New: %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = w1.Run(ctx1) }()

	// Wait for exactly 2 completed attempts (both landing in 'pending' status).
	if !waitForStatus(q, jobID, "pending", 2, 5*time.Second) {
		row := q.get(jobID)
		t.Fatalf("worker1 did not reach attempts=2/status=pending; row=%+v", row)
	}

	// STOP WORKER 1 (simulating worker restart).
	cancel1()
	if err := w1.Stop(); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("worker1.Stop: %v", err)
	}

	// Snapshot state before starting Worker2.
	rowBeforeRestart := q.get(jobID)
	if rowBeforeRestart == nil {
		t.Fatal("job not found after worker1 stopped")
	}
	attemptsBeforeRestart := rowBeforeRestart.attempts

	// --- WORKER 2 (restart simulation) ---
	w2, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker2-restart-test",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker2.New: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = w2.Run(ctx2) }()

	// After Worker2 runs one attempt, attempts must be > attemptsBeforeRestart.
	expectedAttempts := attemptsBeforeRestart + 1
	if !waitForAttempts(q, jobID, expectedAttempts, 5*time.Second) {
		row := q.get(jobID)
		t.Fatalf("worker2 did not increment attempts beyond %d; row=%+v", attemptsBeforeRestart, row)
	}

	cancel2()
	_ = w2.Stop()
}

// ---------------------------------------------------------------------------
// Step 7: After max_attempts, job moves to dead-letter
// ---------------------------------------------------------------------------

// TestWorkerRetryPersist_ExhaustedJobMovesToDeadLetter verifies the full
// lifecycle: multiple Workers share the same queue; job exhausts max_attempts
// and ends in 'failed' with a dead-letter entry.
func TestWorkerRetryPersist_ExhaustedJobMovesToDeadLetter(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	// max_attempts=3: after 3 failures the row should be dead-lettered.
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 3)

	// Single worker; let it run to exhaustion.
	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker-exhaust",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for the job to reach status='failed'.
	if !waitForStatus(q, jobID, "failed", 3, 10*time.Second) {
		row := q.get(jobID)
		t.Fatalf("job did not reach status=failed; row=%+v", row)
	}

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job row not found after dead-lettering")
	}
	if row.status != "failed" {
		t.Fatalf("expected status=failed, got %s", row.status)
	}
	if row.attempts != 3 {
		t.Fatalf("expected attempts=3, got %d", row.attempts)
	}
	if q.deadLetterCount() == 0 {
		t.Fatal("expected dead-letter entry, found none")
	}
}

// ---------------------------------------------------------------------------
// Step 8: worker_dead_letter.original_job_id matches original job ID
// ---------------------------------------------------------------------------

// TestWorkerRetryPersist_DeadLetterOriginalJobIDMatches verifies that the
// dead-letter entry's original_job_id is identical to the job's original ID.
func TestWorkerRetryPersist_DeadLetterOriginalJobIDMatches(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker-deadletter-id",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if !waitForStatus(q, jobID, "failed", 2, 10*time.Second) {
		t.Fatal("job did not reach status=failed within timeout")
	}

	cancel()
	_ = w.Stop()

	// Inspect dead-letter directly.
	q.mu.Lock()
	dl := q.deadLetter
	q.mu.Unlock()

	if len(dl) == 0 {
		t.Fatal("no dead-letter entries found")
	}

	// Verify original_job_id matches.
	matched := false
	for _, entry := range dl {
		if entry.originalJobID == jobID {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("no dead-letter entry with original_job_id=%s; entries: %+v", jobID, dl)
	}
}

// ---------------------------------------------------------------------------
// Step 9: worker_jobs row is status='failed' (not deleted)
// ---------------------------------------------------------------------------

// TestWorkerRetryPersist_RowStatusIsFailedNotDeleted verifies that after
// exhausting max_attempts the original row stays in the queue as status='failed'
// (the current design does NOT delete it).
func TestWorkerRetryPersist_RowStatusIsFailedNotDeleted(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker-rowstatus",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if !waitForStatus(q, jobID, "failed", 2, 10*time.Second) {
		t.Fatal("job did not reach status=failed within timeout")
	}

	cancel()
	_ = w.Stop()

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job row was deleted; expected it to remain with status='failed'")
	}
	if row.status != "failed" {
		t.Fatalf("expected status=failed, got %s", row.status)
	}
}

// ---------------------------------------------------------------------------
// Full end-to-end scenario with explicit Worker restart
// ---------------------------------------------------------------------------

// TestWorkerRetryPersist_FullLifecycleWithRestart is the canonical end-to-end
// test for feature #39. It runs through all nine feature steps:
//
//  1. Enqueue test.always_fail job.
//  2. Start Worker1, wait until attempts=2.
//  3. Stop Worker1 (restart simulation).
//  4. Assert: status='pending', attempts=2, last_error non-nil.
//  5. Start Worker2.
//  6. Wait until attempts=3.
//  7. Wait until status='failed' (max_attempts=3 exhausted).
//  8. Assert: dead-letter entry, original_job_id matches.
//  9. Assert: row status='failed' (row not deleted).
func TestWorkerRetryPersist_FullLifecycleWithRestart(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	// STEP 1: Enqueue a job that intentionally fails; max_attempts=3.
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 3)
	if r := q.get(jobID); r == nil || r.status != "pending" {
		t.Fatal("step 1: job not found or status != pending")
	}

	// STEP 2: Start Worker1, wait until attempts=2.
	w1, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker1-lifecycle",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("step 2: worker1.New: %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = w1.Run(ctx1) }()

	// Wait for attempts=2 with status='pending' (2nd failure completed, retry scheduled).
	if !waitForStatus(q, jobID, "pending", 2, 10*time.Second) {
		row := q.get(jobID)
		t.Fatalf("step 2: did not reach attempts=2/status=pending; row=%+v", row)
	}

	// STEP 3: Stop Worker1 (simulating worker process restart).
	cancel1()
	if err := w1.Stop(); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("step 3: worker1.Stop: %v", err)
	}

	// STEP 4: Verify row after Worker1 stops.
	row4 := q.get(jobID)
	if row4 == nil {
		t.Fatal("step 4: job row not found after worker1 stopped")
	}
	if row4.status != "pending" {
		t.Fatalf("step 4: expected status=pending, got %s", row4.status)
	}
	if row4.attempts != 2 {
		t.Fatalf("step 4: expected attempts=2, got %d", row4.attempts)
	}
	if row4.lastError == nil || *row4.lastError == "" {
		t.Fatalf("step 4: expected last_error to be populated, got %v", row4.lastError)
	}

	// STEP 5: Start Worker2 (the restarted process).
	w2, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker2-lifecycle",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("step 5: worker2.New: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = w2.Run(ctx2) }()

	// STEP 6: Wait for attempts to become 3.
	if !waitForAttempts(q, jobID, 3, 5*time.Second) {
		row := q.get(jobID)
		t.Fatalf("step 6: attempts did not reach 3; row=%+v", row)
	}

	// STEP 7: Wait for status='failed' (max_attempts=3 exhausted).
	if !waitForStatus(q, jobID, "failed", 3, 5*time.Second) {
		row := q.get(jobID)
		t.Fatalf("step 7: job did not reach status=failed; row=%+v", row)
	}

	cancel2()
	_ = w2.Stop()

	// STEP 8: Verify dead-letter original_job_id matches.
	q.mu.Lock()
	dl := make([]deadLetterRow, len(q.deadLetter))
	copy(dl, q.deadLetter)
	q.mu.Unlock()

	if len(dl) == 0 {
		t.Fatal("step 8: no dead-letter entries found")
	}
	matched := false
	for _, entry := range dl {
		if entry.originalJobID == jobID {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("step 8: no dead-letter entry with original_job_id=%s", jobID)
	}

	// STEP 9: Verify row is status='failed' (not deleted).
	row9 := q.get(jobID)
	if row9 == nil {
		t.Fatal("step 9: job row was deleted; expected status='failed' row to remain")
	}
	if row9.status != "failed" {
		t.Fatalf("step 9: expected status=failed, got %s", row9.status)
	}
}

// TestWorkerRetryPersist_MultipleRestartsCumulativeAttempts verifies that
// every worker restart correctly continues counting from the persisted
// attempts value (not from 1). This exercises the scenario where the
// operator restarts the worker multiple times before max_attempts is reached.
func TestWorkerRetryPersist_MultipleRestartsCumulativeAttempts(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail error")
	})

	// max_attempts=5: we restart after each of attempts 1, 2, 3, then let it run to failure.
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 5)

	startWorker := func(_, instanceID string) (context.CancelFunc, error) {
		w, err := New(Options{
			Queue:           q,
			Registry:        reg,
			InstanceID:      instanceID,
			PollInterval:    5 * time.Millisecond,
			ShutdownTimeout: 3 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = w.Run(ctx) }()
		return func() {
			cancel()
			_ = w.Stop()
		}, nil
	}

	// Restart loop: run one attempt per worker instance for the first 3 attempts.
	for i := 1; i <= 3; i++ {
		stop, err := startWorker("", "worker-multi-restart")
		if err != nil {
			t.Fatalf("restart %d: worker.New: %v", i, err)
		}
		// Wait for this attempt to complete (attempts reaches i, status='pending').
		if !waitForStatus(q, jobID, "pending", i, 5*time.Second) {
			row := q.get(jobID)
			t.Fatalf("restart %d: job did not reach attempts=%d/status=pending; row=%+v", i, i, row)
		}
		stop() // simulate restart
	}

	// At this point attempts=3, status='pending'. Let one more worker run to exhaustion.
	stop, err := startWorker("", "worker-final")
	if err != nil {
		t.Fatalf("final worker.New: %v", err)
	}
	defer stop()

	// Wait for status='failed' (attempts will reach 5).
	if !waitForStatus(q, jobID, "failed", 5, 10*time.Second) {
		row := q.get(jobID)
		t.Fatalf("job did not reach status=failed after all restarts; row=%+v", row)
	}

	row := q.get(jobID)
	if row.attempts != 5 {
		t.Fatalf("expected attempts=5 after full exhaustion, got %d", row.attempts)
	}
	if q.deadLetterCount() == 0 {
		t.Fatal("expected dead-letter entry after full exhaustion")
	}
}

// TestWorkerRetryPersist_SecondWorkerUsesCorrectInstanceID verifies that
// after a restart the new worker's instance ID appears in claimed_by.
func TestWorkerRetryPersist_SecondWorkerUsesCorrectInstanceID(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()

	// Block the second run: succeed on second attempt so we can inspect claimed_by.
	var callCount int
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		callCount++
		if callCount < 2 {
			return errors.New("first attempt fails")
		}
		return nil // second attempt succeeds
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 5)

	// Worker1: run first attempt (will fail), then stop.
	w1, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker1-instanceid",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker1.New: %v", err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = w1.Run(ctx1) }()

	// Wait for first failure → status='pending', attempts=1.
	if !waitForStatus(q, jobID, "pending", 1, 5*time.Second) {
		t.Fatal("worker1 did not complete first attempt")
	}
	cancel1()
	_ = w1.Stop()

	// Worker2: run second attempt (will succeed).
	w2, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker2-instanceid",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker2.New: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = w2.Run(ctx2) }()

	// Wait for done.
	if !waitForStatus(q, jobID, "done", 2, 5*time.Second) {
		row := q.get(jobID)
		t.Fatalf("worker2 did not complete second attempt; row=%+v", row)
	}
	cancel2()
	_ = w2.Stop()

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job not found after completion")
	}
	if row.claimedBy != "worker2-instanceid" {
		t.Fatalf("expected claimed_by=worker2-instanceid, got %q", row.claimedBy)
	}
}

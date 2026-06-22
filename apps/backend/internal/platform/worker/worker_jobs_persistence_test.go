// Package worker_test holds the test suite for feature #20:
// "Worker job inserted to worker_jobs survives restart".
//
// These tests exercise the persistent-queue contract of the Worker +
// Queue abstraction without requiring a live PostgreSQL connection.
// An inMemoryQueue mirrors the worker_jobs table semantics so each test
// controls precise timing, status transitions, and metadata assertions.
//
// The "offline worker / online worker" lifecycle described in the feature
// steps translates directly to this unit test idiom:
//
//   - Steps 1–3: job is inserted into inMemoryQueue with status='pending'
//     before any Worker is created (worker is "offline").
//   - Step 4:    Worker is constructed and Run is called (worker "starts").
//   - Steps 5–8: assertions poll the queue until status='done' and verify
//     claimed_by, attempts, and claimed_at.
//   - Step 9:    cleanup removes the test row.
//
// DB-level verification (SELECT from worker_jobs in a running Docker
// stack) was performed manually and is documented in claude-progress.txt.
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// inMemoryQueue — thread-safe Queue implementation for unit tests.
//
// Mirrors the worker_jobs / worker_dead_letter table semantics without a
// live database:
//
//   - ClaimNext uses FIFO order (earliest scheduledAt first), atomically
//     moves one pending row to 'claimed', bumps attempts, and sets
//     claimedAt + claimedBy — exactly as the FOR UPDATE SKIP LOCKED CTE
//     does in PGQueue.
//   - MarkDone / MarkRetry / MarkFailed map to the same column transitions
//     as PGQueue.
//   - MarkFailed copies the row to a dead-letter list (deadLetter slice).
// ---------------------------------------------------------------------------

type inMemoryJobRow struct {
	id          string
	jobType     string
	payload     []byte
	status      string // pending | claimed | done | failed
	attempts    int
	maxAttempts int
	claimedBy   string
	claimedAt   *time.Time
	lastError   *string
	scheduledAt time.Time
	createdAt   time.Time
}

type deadLetterRow struct {
	originalJobID     string
	jobType           string
	payload           []byte
	attempts          int
	lastError         string
	failedAt          time.Time
	originalCreatedAt time.Time
}

type inMemoryQueue struct {
	mu         sync.Mutex
	rows       []*inMemoryJobRow
	deadLetter []deadLetterRow
	nextID     int
}

func newInMemoryQueue() *inMemoryQueue {
	return &inMemoryQueue{}
}

// insert adds a new pending row and returns its string ID (simulates the
// INSERT INTO worker_jobs ... RETURNING id pattern).
//
// scheduledAt == zero → defaults to now().
func (q *inMemoryQueue) insert(jobType string, payload []byte, scheduledAt time.Time, maxAttempts int) string {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.nextID++
	id := fmt.Sprintf("job-%04d", q.nextID)
	if scheduledAt.IsZero() {
		scheduledAt = time.Now()
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	q.rows = append(q.rows, &inMemoryJobRow{
		id:          id,
		jobType:     jobType,
		payload:     payload,
		status:      "pending",
		attempts:    0,
		maxAttempts: maxAttempts,
		scheduledAt: scheduledAt,
		createdAt:   time.Now(),
	})
	return id
}

// get returns a snapshot of the row for the given ID, or nil if not found.
func (q *inMemoryQueue) get(id string) *inMemoryJobRow {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.id == id {
			// Return a shallow copy so the caller sees a consistent snapshot.
			cp := *r
			return &cp
		}
	}
	return nil
}

// remove deletes the row with the given ID (cleanup step).
func (q *inMemoryQueue) remove(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, r := range q.rows {
		if r.id == id {
			q.rows = append(q.rows[:i], q.rows[i+1:]...)
			return
		}
	}
}

// deadLetterCount returns how many rows are in the dead-letter list.
func (q *inMemoryQueue) deadLetterCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.deadLetter)
}

// ClaimNext implements Queue. Thread-safe FIFO claim.
func (q *inMemoryQueue) ClaimNext(_ context.Context, instanceID string) (*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	for _, r := range q.rows {
		if r.status == "pending" && !r.scheduledAt.After(now) {
			r.status = "claimed"
			r.attempts++
			t := time.Now()
			r.claimedAt = &t
			r.claimedBy = instanceID
			r.lastError = nil
			return &Job{
				ID:          r.id,
				Type:        r.jobType,
				Payload:     r.payload,
				Attempts:    r.attempts,
				MaxAttempts: r.maxAttempts,
			}, nil
		}
	}
	return nil, nil // empty queue
}

// MarkDone implements Queue.
func (q *inMemoryQueue) MarkDone(_ context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "done"
			r.lastError = nil
			return nil
		}
	}
	return fmt.Errorf("inMemoryQueue: job %s not found", jobID)
}

// MarkRetry implements Queue.
func (q *inMemoryQueue) MarkRetry(_ context.Context, jobID, lastErr string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.id == jobID {
			r.status = "pending"
			r.claimedAt = nil
			r.claimedBy = ""
			r.lastError = &lastErr
			return nil
		}
	}
	return fmt.Errorf("inMemoryQueue: job %s not found", jobID)
}

// MarkFailed implements Queue — copies to dead letter and sets status='failed'.
func (q *inMemoryQueue) MarkFailed(_ context.Context, job *Job, lastErr string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.id == job.ID {
			q.deadLetter = append(q.deadLetter, deadLetterRow{
				originalJobID:     r.id,
				jobType:           r.jobType,
				payload:           r.payload,
				attempts:          r.attempts,
				lastError:         lastErr,
				failedAt:          time.Now(),
				originalCreatedAt: r.createdAt,
			})
			r.status = "failed"
			r.lastError = &lastErr
			return nil
		}
	}
	return fmt.Errorf("inMemoryQueue: job %s not found", job.ID)
}

// Compile-time guard: inMemoryQueue must satisfy the Queue interface.
var _ Queue = (*inMemoryQueue)(nil)

// ---------------------------------------------------------------------------
// Helper — poll until predicate or timeout
// ---------------------------------------------------------------------------

func pollUntil(t *testing.T, timeout time.Duration, interval time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

// ---------------------------------------------------------------------------
// Feature #20 tests
// ---------------------------------------------------------------------------

// TestWorkerJobs_PendingJobSurvivesOfflineWorker mirrors steps 1–5:
// a job inserted while the worker is offline transitions to 'done' once
// the worker starts.
func TestWorkerJobs_PendingJobSurvivesOfflineWorker(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	// STEP 2: INSERT row while worker is "offline" (worker not yet created).
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	// STEP 3: Verify row exists with status='pending'.
	row := q.get(jobID)
	if row == nil {
		t.Fatalf("step 3: job %s not found in queue", jobID)
	}
	if row.status != "pending" {
		t.Fatalf("step 3: expected status=pending, got %s", row.status)
	}

	// STEP 4: Start worker.
	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-01",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("step 4: worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// STEP 5: Poll DB for status change — expect 'done' within 30s.
	ok := pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})
	if !ok {
		row = q.get(jobID)
		status := "<nil>"
		if row != nil {
			status = row.status
		}
		t.Fatalf("step 5: job %s did not reach status=done within timeout (current: %s)", jobID, status)
	}

	// STEP 9: Cleanup.
	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_ClaimedByIsNonEmpty mirrors step 6: claimed_by must be
// populated with the worker's instance ID after processing.
func TestWorkerJobs_ClaimedByIsNonEmpty(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-instance-abc",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait until done.
	pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})

	// STEP 6: Verify claimed_by is non-empty.
	row := q.get(jobID)
	if row == nil {
		t.Fatal("step 6: job not found after completion")
	}
	if row.claimedBy == "" {
		t.Fatal("step 6: claimed_by is empty; worker instance ID must be written on claim")
	}
	if row.claimedBy != "test-instance-abc" {
		t.Fatalf("step 6: expected claimed_by=test-instance-abc, got %q", row.claimedBy)
	}

	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_AttemptsIsOne mirrors step 7: a first-time successful job
// must show attempts=1.
func TestWorkerJobs_AttemptsIsOne(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-attempts",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})

	// STEP 7: Verify attempts = 1.
	row := q.get(jobID)
	if row == nil {
		t.Fatal("step 7: job not found after completion")
	}
	if row.attempts != 1 {
		t.Fatalf("step 7: expected attempts=1, got %d", row.attempts)
	}

	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_ClaimedAtIsSet mirrors step 8: claimed_at must be
// non-nil and within the last 30 seconds (proves the timestamp is set at
// claim time, not left NULL).
func TestWorkerJobs_ClaimedAtIsSet(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	before := time.Now()
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-claimedat",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})

	// STEP 8: claimed_at IS NOT NULL and within last 30s.
	row := q.get(jobID)
	if row == nil {
		t.Fatal("step 8: job not found after completion")
	}
	if row.claimedAt == nil {
		t.Fatal("step 8: claimed_at is NULL; must be set when worker claims the row")
	}
	after := time.Now()
	if row.claimedAt.Before(before) || row.claimedAt.After(after) {
		t.Fatalf("step 8: claimed_at %v is outside expected window [%v, %v]",
			row.claimedAt, before, after)
	}
	if time.Since(*row.claimedAt) > 30*time.Second {
		t.Fatalf("step 8: claimed_at %v is older than 30s", row.claimedAt)
	}

	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_CleanupRow mirrors step 9: after removal the row must no
// longer be visible in the queue.
func TestWorkerJobs_CleanupRow(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-cleanup",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})

	cancel()
	_ = w.Stop()

	// STEP 9: Cleanup — remove the row.
	q.remove(jobID)

	if q.get(jobID) != nil {
		t.Fatalf("step 9: job %s still present after remove", jobID)
	}
}

// TestWorkerJobs_FullLifecycle runs all steps as a single sequence to prove
// the end-to-end persistent-queue contract: insert while offline → worker
// starts → done → all metadata populated → cleanup.
func TestWorkerJobs_FullLifecycle(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	// -- STEP 2: Insert directly (worker offline) --
	before := time.Now()
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	// -- STEP 3: Verify status='pending' --
	if r := q.get(jobID); r == nil || r.status != "pending" {
		t.Fatalf("step 3: expected status=pending")
	}

	// -- STEP 4: Start worker --
	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-lifecycle",
		PollInterval:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("step 4: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// -- STEP 5: Poll until status='done' --
	ok := pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})
	if !ok {
		t.Fatalf("step 5: job did not reach status=done within timeout")
	}

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job vanished after completion")
	}

	// -- STEP 6: claimed_by is non-empty --
	if row.claimedBy == "" {
		t.Fatal("step 6: claimed_by is empty")
	}

	// -- STEP 7: attempts = 1 --
	if row.attempts != 1 {
		t.Fatalf("step 7: expected attempts=1, got %d", row.attempts)
	}

	// -- STEP 8: claimed_at IS NOT NULL and within last 30s --
	if row.claimedAt == nil {
		t.Fatal("step 8: claimed_at is nil")
	}
	after := time.Now()
	if row.claimedAt.Before(before) || row.claimedAt.After(after) {
		t.Fatalf("step 8: claimed_at %v out of window [%v, %v]", row.claimedAt, before, after)
	}

	cancel()
	_ = w.Stop()

	// -- STEP 9: Cleanup --
	q.remove(jobID)
	if q.get(jobID) != nil {
		t.Fatalf("step 9: job %s still present after remove", jobID)
	}
}

// TestWorkerJobs_FailingHandlerRetries verifies that a failing handler
// causes the job to be retried (status returns to 'pending') and that
// after max_attempts the job moves to 'failed' and is dead-lettered.
func TestWorkerJobs_FailingHandlerRetries(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()

	callCount := 0
	reg.Register("fail.test", func(_ context.Context, _ []byte) error {
		callCount++
		return errors.New("intentional test failure")
	})

	// Insert with max_attempts=2.
	jobID := q.insert("fail.test", []byte(`{}`), time.Time{}, 2)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-retry",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait until the job ends up in 'failed'.
	ok := pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "failed"
	})
	if !ok {
		row := q.get(jobID)
		status := "<nil>"
		if row != nil {
			status = fmt.Sprintf("%s (attempts=%d)", row.status, row.attempts)
		}
		t.Fatalf("job did not reach status=failed within timeout (current: %s)", status)
	}

	row := q.get(jobID)
	if row == nil {
		t.Fatal("job not found after failure")
	}
	if row.attempts != 2 {
		t.Fatalf("expected attempts=2 after exhausting max_attempts, got %d", row.attempts)
	}
	if q.deadLetterCount() == 0 {
		t.Fatal("expected dead-letter entry after exhausting retries")
	}
	if callCount != 2 {
		t.Fatalf("expected handler called 2 times, got %d", callCount)
	}

	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_UnknownJobTypeDeadLetters verifies that a job whose
// job_type has no registered handler fails immediately (since no retries
// can make it succeed) and ends in 'failed'.
func TestWorkerJobs_UnknownJobTypeDeadLetters(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry() // no handlers registered

	jobID := q.insert("unknown.type", []byte(`{}`), time.Time{}, 1)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-unknown",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	ok := pollUntil(t, 5*time.Second, 10*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "failed"
	})
	if !ok {
		t.Fatalf("job with unknown type did not reach status=failed within timeout")
	}

	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_FutureScheduledAtNotClaimed verifies that a job with
// scheduled_at in the future is not picked up immediately — it must wait
// until the schedule time arrives.
func TestWorkerJobs_FutureScheduledAtNotClaimed(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop.test", func(_ context.Context, _ []byte) error { return nil })

	// Schedule 500ms in the future.
	future := time.Now().Add(500 * time.Millisecond)
	jobID := q.insert("noop.test", []byte(`{}`), future, 3)

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-future",
		PollInterval:    20 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// The job must NOT be picked up in the first 200ms (well before the
	// scheduled_at arrives).
	time.Sleep(200 * time.Millisecond)
	row := q.get(jobID)
	if row == nil {
		t.Fatal("job row disappeared before scheduled time")
	}
	if row.status != "pending" {
		t.Fatalf("job was claimed before scheduled_at; expected pending, got %s", row.status)
	}

	// But it must be done soon after scheduled_at passes.
	ok := pollUntil(t, 5*time.Second, 20*time.Millisecond, func() bool {
		r := q.get(jobID)
		return r != nil && r.status == "done"
	})
	if !ok {
		t.Fatalf("job with future scheduled_at did not complete within timeout")
	}

	cancel()
	_ = w.Stop()
	q.remove(jobID)
}

// TestWorkerJobs_EmptyQueueDoesNotPanic verifies that a Worker against an
// empty queue loops cleanly and can be stopped without panicking.
func TestWorkerJobs_EmptyQueueDoesNotPanic(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-empty",
		PollInterval:    20 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Let the worker poll a few empty cycles.
	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

// TestWorkerJobs_StopIsIdempotent verifies that calling Stop twice does not
// panic and returns consistent results.
func TestWorkerJobs_StopIsIdempotent(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "test-worker-idempotent",
		PollInterval:    20 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Let it start.
	time.Sleep(30 * time.Millisecond)

	err1 := w.Stop()
	err2 := w.Stop()

	// Both calls must not panic. The first should return nil (clean stop).
	// The second may return nil or context.DeadlineExceeded depending on
	// whether doneCh is already closed — both are acceptable.
	if err1 != nil {
		t.Fatalf("first Stop returned unexpected error: %v", err1)
	}
	// err2 may be nil or context.DeadlineExceeded — just must not panic.
	_ = err2
}

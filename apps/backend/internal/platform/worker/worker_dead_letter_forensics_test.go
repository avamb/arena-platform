// Package worker — feature #50: worker_dead_letter retains original_job_id for forensics.
//
// When a job exhausts max_attempts, the worker moves it to worker_dead_letter
// with the full forensic record:
//
//   - original_job_id   — links back to the source worker_jobs row
//   - attempts          — total number of attempts made
//   - last_error        — error text from the final failure
//   - original_created_at — timestamp of the original worker_jobs row (preserved
//     so forensic queries can measure how long a job lingered before dying)
//   - failed_at         — timestamp when the dead-letter row was written
//
// The original worker_jobs row is NOT deleted; it reaches a terminal status
// ('failed') so operators can correlate both tables.
//
// All tests use inMemoryQueue (defined in worker_jobs_persistence_test.go) so
// no live database is required.
package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// runUntilDeadLettered starts a worker on q and waits until the job identified
// by jobID has been dead-lettered (status='failed'). It stops the worker and
// returns the dead-letter entries and the final job row.
func runUntilDeadLettered(
	t *testing.T,
	q *inMemoryQueue,
	reg *Registry,
	jobID string,
	instanceID string,
) (dl []deadLetterRow, row *inMemoryJobRow) {
	t.Helper()

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      instanceID,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for the job to reach terminal status='failed'.
	if !waitForStatus(q, jobID, "failed", 2, 10*time.Second) {
		r := q.get(jobID)
		t.Fatalf("job did not reach status=failed within timeout; row=%+v", r)
	}

	cancel()
	_ = w.Stop()

	// Snapshot dead-letter slice under lock.
	q.mu.Lock()
	entries := make([]deadLetterRow, len(q.deadLetter))
	copy(entries, q.deadLetter)
	q.mu.Unlock()

	return entries, q.get(jobID)
}

// ---------------------------------------------------------------------------
// Step 1–3: Insert, run to exhaustion, query dead-letter
// ---------------------------------------------------------------------------

// TestDeadLetter_SingleEntryAfterExhaustion verifies that exactly one dead-letter
// row is created for a job that exhausts max_attempts=2.
func TestDeadLetter_SingleEntryAfterExhaustion(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{"key":"step1"}`), time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-entry")

	if len(dl) != 1 {
		t.Fatalf("step 3: expected exactly 1 dead-letter entry, got %d", len(dl))
	}
}

// ---------------------------------------------------------------------------
// Step 4: Verify original_job_id matches
// ---------------------------------------------------------------------------

// TestDeadLetter_OriginalJobIDMatches verifies that dead-letter entry's
// original_job_id equals the job ID returned at INSERT time (step 4).
func TestDeadLetter_OriginalJobIDMatches(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-jobid")

	if len(dl) == 0 {
		t.Fatal("step 4: no dead-letter entries found")
	}

	// Find the entry that corresponds to our job.
	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("step 4: no dead-letter entry with original_job_id=%s; entries=%+v", jobID, dl)
	}
}

// ---------------------------------------------------------------------------
// Step 5a: Verify attempts == max_attempts (2)
// ---------------------------------------------------------------------------

// TestDeadLetter_AttemptsEqualMaxAttempts verifies the dead-letter row's
// attempts field equals max_attempts (2 in this test) — step 5.
func TestDeadLetter_AttemptsEqualMaxAttempts(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("intentional always-fail")
	})

	const maxAttempts = 2
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, maxAttempts)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-attempts")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("step 5: dead-letter entry for job %s not found", jobID)
	}
	if found.attempts != maxAttempts {
		t.Fatalf("step 5: expected attempts=%d in dead-letter, got %d", maxAttempts, found.attempts)
	}
}

// ---------------------------------------------------------------------------
// Step 5b: Verify last_error non-empty
// ---------------------------------------------------------------------------

// TestDeadLetter_LastErrorNonEmpty verifies that the dead-letter entry's
// last_error field is non-empty — step 5.
func TestDeadLetter_LastErrorNonEmpty(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	errMsg := "specific failure reason for forensics"
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New(errMsg)
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-lasterr")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("step 5: dead-letter entry for job %s not found", jobID)
	}
	if found.lastError == "" {
		t.Fatal("step 5: last_error is empty; expected non-empty error text")
	}
}

// TestDeadLetter_LastErrorContainsHandlerMessage verifies that last_error in the
// dead-letter entry contains the actual error returned by the handler.
func TestDeadLetter_LastErrorContainsHandlerMessage(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	const errMsg = "unique-failure-reason-12345"
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New(errMsg)
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-errtext")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("dead-letter entry for job %s not found", jobID)
	}
	if found.lastError != errMsg {
		t.Fatalf("expected last_error=%q, got %q", errMsg, found.lastError)
	}
}

// ---------------------------------------------------------------------------
// Step 6: Verify original_created_at preserved
// ---------------------------------------------------------------------------

// TestDeadLetter_OriginalCreatedAtPreserved verifies that the dead-letter row's
// original_created_at matches the worker_jobs row's created_at — step 6.
func TestDeadLetter_OriginalCreatedAtPreserved(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	// Capture the time window around job insertion so we can bound created_at.
	before := time.Now()
	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)
	after := time.Now()

	// Retrieve the original createdAt from the queue row before the worker runs.
	originalRow := q.get(jobID)
	if originalRow == nil {
		t.Fatal("step 6: job row not found immediately after insert")
	}
	originalCreatedAt := originalRow.createdAt

	// Sanity check: createdAt is within the insertion window.
	if originalCreatedAt.Before(before) || originalCreatedAt.After(after) {
		t.Fatalf("step 6: createdAt %v outside insertion window [%v, %v]",
			originalCreatedAt, before, after)
	}

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-createdat")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("step 6: dead-letter entry for job %s not found", jobID)
	}

	// original_created_at must equal the job row's created_at exactly.
	if !found.originalCreatedAt.Equal(originalCreatedAt) {
		t.Fatalf("step 6: original_created_at mismatch: dead-letter has %v, job row had %v",
			found.originalCreatedAt, originalCreatedAt)
	}
}

// TestDeadLetter_OriginalCreatedAtIsNonZero verifies the dead-letter row's
// original_created_at is not the zero value (never left unset).
func TestDeadLetter_OriginalCreatedAtIsNonZero(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-nonzero-ts")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("dead-letter entry for job %s not found", jobID)
	}
	if found.originalCreatedAt.IsZero() {
		t.Fatal("step 6: original_created_at is zero; it must be populated from worker_jobs.created_at")
	}
}

// TestDeadLetter_FailedAtIsRecent verifies that the failed_at timestamp on the
// dead-letter entry is recent (within the last 10 seconds).
func TestDeadLetter_FailedAtIsRecent(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)
	testStart := time.Now()

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-failedat")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("dead-letter entry for job %s not found", jobID)
	}
	if found.failedAt.IsZero() {
		t.Fatal("failed_at is zero; must be set when the dead-letter row is written")
	}
	if found.failedAt.Before(testStart) {
		t.Fatalf("failed_at %v is before test start %v; must be >= test start", found.failedAt, testStart)
	}
	if time.Since(found.failedAt) > 10*time.Second {
		t.Fatalf("failed_at %v is more than 10s ago; must be recent", found.failedAt)
	}
}

// ---------------------------------------------------------------------------
// Step 7: worker_jobs row reaches terminal status ('failed' or deleted)
// ---------------------------------------------------------------------------

// TestDeadLetter_WorkerJobsRowStatusFailed verifies that after exhausting
// max_attempts, the original worker_jobs row has status='failed' — step 7.
func TestDeadLetter_WorkerJobsRowStatusFailed(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	_, row := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-status")

	if row == nil {
		// Per spec, the row "may be deleted or status='failed'". If deleted, that is also valid.
		// But in our design the row is retained with status='failed'.
		t.Log("step 7: worker_jobs row was deleted after dead-lettering (design choice)")
		return
	}
	if row.status != "failed" {
		t.Fatalf("step 7: expected status=failed, got %s", row.status)
	}
}

// TestDeadLetter_WorkerJobsRowNotDeleted verifies that the design choice of
// retaining the failed row (not deleting it) is observed, providing an
// audit trail in worker_jobs alongside the forensic record in worker_dead_letter.
func TestDeadLetter_WorkerJobsRowNotDeleted(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	_, row := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-notdeleted")

	if row == nil {
		t.Fatalf("step 7: worker_jobs row was deleted; design requires it to remain as status='failed'")
	}
}

// ---------------------------------------------------------------------------
// Additional: payload and job_type preserved in dead-letter
// ---------------------------------------------------------------------------

// TestDeadLetter_JobTypePreserved verifies the dead-letter row copies job_type
// from the original row (needed to route forensic alerts).
func TestDeadLetter_JobTypePreserved(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	jobID := q.insert("test.always_fail", []byte(`{}`), time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-jobtype")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("dead-letter entry for job %s not found", jobID)
	}
	if found.jobType != "test.always_fail" {
		t.Fatalf("expected job_type=test.always_fail, got %q", found.jobType)
	}
}

// TestDeadLetter_PayloadPreserved verifies that the original payload bytes are
// preserved in the dead-letter entry unchanged.
func TestDeadLetter_PayloadPreserved(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New("fail")
	})

	payload := []byte(`{"event_id":"forensics-test-12345","amount":99}`)
	jobID := q.insert("test.always_fail", payload, time.Time{}, 2)

	dl, _ := runUntilDeadLettered(t, q, reg, jobID, "worker-dl-payload")

	var found *deadLetterRow
	for i := range dl {
		if dl[i].originalJobID == jobID {
			found = &dl[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("dead-letter entry for job %s not found", jobID)
	}
	if string(found.payload) != string(payload) {
		t.Fatalf("payload mismatch: expected %q, got %q", payload, found.payload)
	}
}

// ---------------------------------------------------------------------------
// Full verification: all 7 feature steps in a single test
// ---------------------------------------------------------------------------

// TestDeadLetter_FullVerification is the canonical test for feature #50.
// It runs all seven feature steps in sequence, replicating the manual DB
// query scenario with the inMemoryQueue stand-in.
//
//  1. Insert a job with job_type='test.always_fail', max_attempts=2
//  2. Wait until worker has attempted twice and given up
//  3. Query dead-letter entries (ordered by failed_at DESC equivalent)
//  4. Verify original_job_id matches
//  5. Verify attempts=2, last_error non-empty
//  6. Verify original_created_at preserved (equals worker_jobs.created_at)
//  7. Query worker_jobs — row must be present with status='failed' (per design)
func TestDeadLetter_FullVerification(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	const errMsg = "intentional always-fail for feature-50"
	reg.Register("test.always_fail", func(_ context.Context, _ []byte) error {
		return errors.New(errMsg)
	})

	// STEP 1: Insert job with max_attempts=2.
	before := time.Now()
	jobID := q.insert("test.always_fail", []byte(`{"step":"full-verify"}`), time.Time{}, 2)
	after := time.Now()

	// Capture original created_at before worker touches the row.
	originalRow := q.get(jobID)
	if originalRow == nil {
		t.Fatal("step 1: job not found immediately after insert")
	}
	if originalRow.status != "pending" {
		t.Fatalf("step 1: expected status=pending, got %s", originalRow.status)
	}
	originalCreatedAt := originalRow.createdAt
	if originalCreatedAt.Before(before) || originalCreatedAt.After(after) {
		t.Fatalf("step 1: created_at %v outside insert window [%v, %v]",
			originalCreatedAt, before, after)
	}

	// STEP 2: Run worker until exhaustion.
	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      "worker-full-verify",
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("step 2: worker.New: %v", err)
	}

	testStart := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if !waitForStatus(q, jobID, "failed", 2, 10*time.Second) {
		row := q.get(jobID)
		t.Fatalf("step 2: job did not reach status=failed; row=%+v", row)
	}

	cancel()
	_ = w.Stop()

	// STEP 3: Query dead-letter (snapshot, ordered by failedAt DESC).
	q.mu.Lock()
	entries := make([]deadLetterRow, len(q.deadLetter))
	copy(entries, q.deadLetter)
	q.mu.Unlock()

	if len(entries) == 0 {
		t.Fatal("step 3: dead-letter is empty; expected at least 1 entry")
	}

	// Find the most-recently-failed entry for our job.
	var found *deadLetterRow
	var latestFailedAt time.Time
	for i := range entries {
		if entries[i].originalJobID == jobID {
			if found == nil || entries[i].failedAt.After(latestFailedAt) {
				found = &entries[i]
				latestFailedAt = entries[i].failedAt
			}
		}
	}
	if found == nil {
		t.Fatalf("step 3: no dead-letter entry with original_job_id=%s", jobID)
	}

	// STEP 4: original_job_id matches.
	if found.originalJobID != jobID {
		t.Fatalf("step 4: original_job_id=%s, expected %s", found.originalJobID, jobID)
	}

	// STEP 5a: attempts == 2.
	if found.attempts != 2 {
		t.Fatalf("step 5: expected attempts=2 in dead-letter, got %d", found.attempts)
	}

	// STEP 5b: last_error non-empty and matches handler error.
	if found.lastError == "" {
		t.Fatal("step 5: last_error is empty; must be populated from handler error")
	}
	if found.lastError != errMsg {
		t.Fatalf("step 5: last_error=%q, expected %q", found.lastError, errMsg)
	}

	// STEP 6: original_created_at preserved.
	if found.originalCreatedAt.IsZero() {
		t.Fatal("step 6: original_created_at is zero; must be copied from worker_jobs.created_at")
	}
	if !found.originalCreatedAt.Equal(originalCreatedAt) {
		t.Fatalf("step 6: original_created_at=%v, expected %v (original job created_at)",
			found.originalCreatedAt, originalCreatedAt)
	}

	// Bonus: failed_at must be recent and after test start.
	if found.failedAt.IsZero() {
		t.Fatal("step 6 bonus: failed_at is zero")
	}
	if found.failedAt.Before(testStart) {
		t.Fatalf("step 6 bonus: failed_at=%v is before test start=%v", found.failedAt, testStart)
	}

	// STEP 7: worker_jobs row must reach terminal status='failed' (per design: not deleted).
	finalRow := q.get(jobID)
	if finalRow == nil {
		t.Fatal("step 7: worker_jobs row was deleted; per design it must remain with status='failed'")
	}
	if finalRow.status != "failed" {
		t.Fatalf("step 7: expected status=failed, got %s", finalRow.status)
	}
	// last_error on the worker_jobs row must also be populated.
	if finalRow.lastError == nil || *finalRow.lastError == "" {
		t.Fatal("step 7: worker_jobs.last_error is nil/empty after dead-lettering")
	}
}

//go:build integration

// Package worker — integration tests for feature #109 (Worker binary bootstrap).
//
// These tests verify the worker queue against a real PostgreSQL 17 container
// (testcontainers-go) covering the scenarios required by step 5:
//
//   - Submit job, consume, verify job reaches status='done'.
//   - Idempotency: duplicate job submissions via separate rows both get consumed.
//   - Retry + backoff: a failing handler retries; job stays pending between attempts.
//   - Dead-letter on max attempts: exhausted job moves to worker_dead_letter.
//
// Run with:
//
//	go test -tags integration -timeout 300s ./apps/backend/internal/platform/worker/...
package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/tests/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// waitJobStatus polls the worker_jobs table until the named job reaches the
// expected status or the deadline expires.
func waitJobStatus(t *testing.T, pool *pgxpool.Pool, jobID, wantStatus string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var status string
		err := pool.QueryRow(ctx,
			`SELECT status FROM worker_jobs WHERE id = $1::uuid`,
			jobID,
		).Scan(&status)
		if err != nil {
			t.Logf("waitJobStatus: query error: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if status == wantStatus {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	var got string
	_ = pool.QueryRow(ctx,
		`SELECT status FROM worker_jobs WHERE id = $1::uuid`, jobID,
	).Scan(&got)
	t.Errorf("waitJobStatus: job %s expected status=%q after %v, got %q", jobID, wantStatus, timeout, got)
}

// countDeadLetter counts rows in worker_dead_letter for a given job_type.
func countDeadLetter(t *testing.T, pool *pgxpool.Pool, jobType string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM worker_dead_letter WHERE job_type = $1`, jobType,
	).Scan(&n)
	if err != nil {
		t.Fatalf("countDeadLetter: %v", err)
	}
	return n
}

// newTestWorker builds a Worker backed by a real PGQueue against pool.
func newTestWorker(t *testing.T, pool *pgxpool.Pool, reg *Registry, poll time.Duration) *Worker {
	t.Helper()
	q := NewPGQueue(pool)
	w, err := New(Options{
		Queue:        q,
		Registry:     reg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: poll,
		RetryBackoff: poll * 3,
	})
	if err != nil {
		t.Fatalf("newTestWorker: %v", err)
	}
	return w
}

// ---------------------------------------------------------------------------
// Step 5a: Submit job → consume → verify done.
// ---------------------------------------------------------------------------

// TestWorkerIntegration109_SubmitAndConsume verifies the happy path end-to-end
// against a real Postgres container (step 5).
func TestWorkerIntegration109_SubmitAndConsume(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	reg := NewRegistry()
	consumed := make(chan []byte, 1)
	reg.Register("integration.smoke", func(ctx context.Context, payload []byte) error {
		consumed <- payload
		return nil
	})

	w := newTestWorker(t, pool, reg, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	jobID, err := EnqueuePayload(ctx, pool, "integration.smoke", map[string]string{"hello": "world"}, 3)
	if err != nil {
		t.Fatalf("step 5: EnqueuePayload: %v", err)
	}

	select {
	case payload := <-consumed:
		if len(payload) == 0 {
			t.Error("step 5: consumed payload is empty")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("step 5: job was not consumed within 15s")
	}

	waitJobStatus(t, pool, jobID, "done", 10*time.Second)

	cancel()
	_ = w.Stop()
}

// ---------------------------------------------------------------------------
// Step 5b: Duplicate submit → both jobs consumed (idempotency at submit level).
// ---------------------------------------------------------------------------

// TestWorkerIntegration109_DuplicateSubmitBothConsumed verifies that submitting
// two identical job payloads results in two independent rows both reaching
// status='done' — the worker does not deduplicate at the queue level; that is
// the caller's responsibility via idempotency keys (step 5).
func TestWorkerIntegration109_DuplicateSubmitBothConsumed(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	reg := NewRegistry()
	var consumedCount int32
	doneCh := make(chan struct{}, 2)
	reg.Register("integration.dup", func(ctx context.Context, payload []byte) error {
		doneCh <- struct{}{}
		return nil
	})

	w := newTestWorker(t, pool, reg, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	payload := map[string]string{"action": "process"}
	id1, err := EnqueuePayload(ctx, pool, "integration.dup", payload, 3)
	if err != nil {
		t.Fatalf("step 5: enqueue job 1: %v", err)
	}
	id2, err := EnqueuePayload(ctx, pool, "integration.dup", payload, 3)
	if err != nil {
		t.Fatalf("step 5: enqueue job 2: %v", err)
	}
	_ = consumedCount

	deadline := time.After(20 * time.Second)
	received := 0
	for received < 2 {
		select {
		case <-doneCh:
			received++
		case <-deadline:
			t.Fatalf("step 5: expected 2 jobs consumed, got %d after 20s", received)
		}
	}

	waitJobStatus(t, pool, id1, "done", 5*time.Second)
	waitJobStatus(t, pool, id2, "done", 5*time.Second)

	cancel()
	_ = w.Stop()
}

// ---------------------------------------------------------------------------
// Step 5c: Retry + backoff: failing handler retries.
// ---------------------------------------------------------------------------

// TestWorkerIntegration109_RetryAndBackoff verifies that a failing handler
// causes the job to be retried (step 5 — retry+backoff).
func TestWorkerIntegration109_RetryAndBackoff(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()
	pgtest.TruncateAll(t, pool)

	reg := NewRegistry()
	var attempts int32
	var attemptsDone = make(chan struct{}, 10)
	reg.Register("integration.retry", func(ctx context.Context, payload []byte) error {
		n := int(0)
		_ = n
		select {
		case attemptsDone <- struct{}{}:
		default:
		}
		attempts++
		if attempts < 3 {
			return errors.New("transient error, retry me")
		}
		return nil
	})

	w := newTestWorker(t, pool, reg, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	jobID, err := EnqueuePayload(ctx, pool, "integration.retry", map[string]string{}, 5)
	if err != nil {
		t.Fatalf("step 5: EnqueuePayload: %v", err)
	}

	// Wait for at least 2 retry invocations.
	deadline := time.After(30 * time.Second)
	received := 0
	for received < 2 {
		select {
		case <-attemptsDone:
			received++
		case <-deadline:
			t.Fatalf("step 5: expected at least 2 handler invocations, got %d", received)
		}
	}

	// Eventually the job should reach 'done' (on the 3rd attempt).
	waitJobStatus(t, pool, jobID, "done", 30*time.Second)

	cancel()
	_ = w.Stop()
}

// ---------------------------------------------------------------------------
// Step 5d: Dead-letter on max attempts.
// ---------------------------------------------------------------------------

// TestWorkerIntegration109_DeadLetterOnMaxAttempts verifies that a job whose
// handler always fails is moved to worker_dead_letter after max_attempts
// exhaustion (step 5).
func TestWorkerIntegration109_DeadLetterOnMaxAttempts(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()
	pgtest.TruncateAll(t, pool)

	reg := NewRegistry()
	reg.Register("integration.exhausted", func(ctx context.Context, payload []byte) error {
		return errors.New("permanent failure")
	})

	w := newTestWorker(t, pool, reg, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	_, err := EnqueuePayload(ctx, pool, "integration.exhausted", map[string]string{}, 2)
	if err != nil {
		t.Fatalf("step 5: EnqueuePayload: %v", err)
	}

	// Wait for worker_dead_letter entry.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countDeadLetter(t, pool, "integration.exhausted") > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if countDeadLetter(t, pool, "integration.exhausted") == 0 {
		t.Fatal("step 5: expected worker_dead_letter entry after max_attempts, got none")
	}

	cancel()
	_ = w.Stop()
}

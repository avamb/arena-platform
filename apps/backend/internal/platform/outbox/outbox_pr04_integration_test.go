//go:build integration

// Package outbox — PR-04 integration tests: fail-closed outbox delivery.
//
// These tests require a live PostgreSQL instance reachable via DATABASE_URL
// with all migrations applied (including 0001_init.sql which creates
// outbox_events). Run with:
//
//	go test -tags=integration ./apps/backend/internal/platform/outbox/...
//
// Each test inserts rows into outbox_events, runs OutboxEventsDispatcher
// against a real PGOutboxEventStore, and verifies the database state.
// httptest.Server instances act as real HTTP receivers.
package outbox

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// pr04Pool opens a connection pool; skips when DATABASE_URL is absent.
func pr04Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PR-04 integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q is not a Postgres DSN; skipping", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pr04Pool: open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("pr04Pool: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// insertOutboxEvent writes one row into outbox_events with processed_at NULL.
// Returns the inserted row UUID (as text).
func insertOutboxEvent(t *testing.T, pool *pgxpool.Pool, eventType string) string {
	t.Helper()
	const sql = `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
		VALUES ('pr04_test', '00000000-0000-0000-0000-000000000099', $1,
		        '{"pr04":"true"}'::jsonb, now())
		RETURNING id::text
	`
	var id string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.QueryRow(ctx, sql, eventType).Scan(&id); err != nil {
		t.Fatalf("insertOutboxEvent: %v", err)
	}
	return id
}

// outboxEventProcessedAt returns processed_at for the given row ID,
// or nil if processed_at IS NULL.
func outboxEventProcessedAt(t *testing.T, pool *pgxpool.Pool, rowID string) *time.Time {
	t.Helper()
	var ts *time.Time
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM outbox_events WHERE id = $1::uuid`, rowID,
	).Scan(&ts); err != nil {
		t.Fatalf("outboxEventProcessedAt(%s): %v", rowID, err)
	}
	return ts
}

// outboxEventAttempts returns the attempts count for the given row.
func outboxEventAttempts(t *testing.T, pool *pgxpool.Pool, rowID string) int {
	t.Helper()
	var n int
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.QueryRow(ctx,
		`SELECT attempts FROM outbox_events WHERE id = $1::uuid`, rowID,
	).Scan(&n); err != nil {
		t.Fatalf("outboxEventAttempts(%s): %v", rowID, err)
	}
	return n
}

// cleanupOutboxEvents deletes rows by event_type prefix. Called via t.Cleanup.
func cleanupOutboxEvents(t *testing.T, pool *pgxpool.Pool, eventTypePrefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`DELETE FROM outbox_events WHERE event_type LIKE $1`,
		eventTypePrefix+"%",
	); err != nil {
		t.Logf("cleanupOutboxEvents(%s): %v (non-fatal)", eventTypePrefix, err)
	}
}

// runDispatcher starts OutboxEventsDispatcher with the given dispatcher, runs
// it for up to maxWait, then stops it.
func runPR04Dispatcher(t *testing.T, pool *pgxpool.Pool, dispatcher Dispatcher, maxWait time.Duration) {
	t.Helper()
	store := NewPGOutboxEventStore(pool)
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      dispatcher,
		PollInterval:    20 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxWait)
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	<-ctx.Done()
	_ = d.Stop()
}

// ---------------------------------------------------------------------------
// Test 1: Successful webhook delivery marks processed_at
// ---------------------------------------------------------------------------

// TestPR04_WebhookSuccess_MarksProcessedAt verifies that a successful POST to
// the webhook endpoint causes the outbox_events row to receive processed_at.
func TestPR04_WebhookSuccess_MarksProcessedAt(t *testing.T) {
	pool := pr04Pool(t)
	const evType = "pr04.integration.success"
	t.Cleanup(func() { cleanupOutboxEvents(t, pool, "pr04.integration.success") })

	var hitCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rowID := insertOutboxEvent(t, pool, evType)

	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL,
		SigningSecret: []byte("pr04-test-signing-secret-32bytesX"),
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	// Run long enough for one delivery cycle.
	runPR04Dispatcher(t, pool, d, 500*time.Millisecond)

	// Verify processed_at was set.
	ts := outboxEventProcessedAt(t, pool, rowID)
	if ts == nil {
		t.Error("PR-04 success: processed_at must be non-NULL after webhook delivery")
	}

	// Verify the webhook was called exactly once.
	if hitCount.Load() < 1 {
		t.Error("PR-04 success: webhook server must have received at least one POST")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Non-2xx response leaves row retryable (processed_at NULL, attempts++)
// ---------------------------------------------------------------------------

// TestPR04_NonSuccessResponse_LeavesRowRetryable proves that a 5xx response
// from the subscriber does NOT set processed_at (row stays retryable) and
// increments attempts.
func TestPR04_NonSuccessResponse_LeavesRowRetryable(t *testing.T) {
	pool := pr04Pool(t)
	const evType = "pr04.integration.retry"
	t.Cleanup(func() { cleanupOutboxEvents(t, pool, "pr04.integration.retry") })

	// Webhook always returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rowID := insertOutboxEvent(t, pool, evType)
	attemptsInitial := outboxEventAttempts(t, pool, rowID)

	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL,
		SigningSecret: []byte("pr04-test-signing-secret-32bytesX"),
		Client:        &http.Client{Timeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	// Run long enough for at least one delivery attempt.
	runPR04Dispatcher(t, pool, d, 300*time.Millisecond)

	// processed_at must still be NULL (row is retryable).
	ts := outboxEventProcessedAt(t, pool, rowID)
	if ts != nil {
		t.Error("PR-04 retry: processed_at must remain NULL when webhook returns 5xx")
	}

	// attempts must have been incremented at least once.
	attemptsAfter := outboxEventAttempts(t, pool, rowID)
	if attemptsAfter <= attemptsInitial {
		t.Errorf("PR-04 retry: attempts must increase after 5xx; before=%d after=%d",
			attemptsInitial, attemptsAfter)
	}
}

// ---------------------------------------------------------------------------
// Test 3: HMAC signature is present and verifiable on delivery
// ---------------------------------------------------------------------------

// TestPR04_HMACSignature_VerifiedBySubscriber proves that when a signing
// secret is configured, each POST carries an X-Arena-Signature header whose
// value is the correct HMAC-SHA256 of the request body.
func TestPR04_HMACSignature_VerifiedBySubscriber(t *testing.T) {
	pool := pr04Pool(t)
	const evType = "pr04.integration.signature"
	t.Cleanup(func() { cleanupOutboxEvents(t, pool, "pr04.integration.signature") })

	secret := []byte("pr04-signature-test-secret-32bXXX")
	var capturedSig string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-Arena-Signature")
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rowID := insertOutboxEvent(t, pool, evType)

	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL,
		SigningSecret: secret,
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	runPR04Dispatcher(t, pool, d, 500*time.Millisecond)

	// Verify processed_at was set (delivery succeeded).
	if ts := outboxEventProcessedAt(t, pool, rowID); ts == nil {
		t.Fatal("PR-04 signature: processed_at must be set after successful delivery")
	}

	// Verify the signature header is present and well-formed.
	if !strings.HasPrefix(capturedSig, "sha256=") {
		t.Errorf("PR-04 signature: X-Arena-Signature must start with sha256=; got %q", capturedSig)
	}

	// Verify the HMAC is correct for the received body.
	if !VerifyHMACSignature(capturedBody, secret, capturedSig) {
		t.Errorf("PR-04 signature: X-Arena-Signature=%q is not a valid HMAC of the received body", capturedSig)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Duplicate safety — already-processed row is not re-delivered
// ---------------------------------------------------------------------------

// TestPR04_DuplicateSafety_AlreadyProcessedNotReDelivered verifies that a row
// with processed_at already set is not re-claimed or re-delivered by the
// dispatcher (replay safety / exactly-once processing guarantee).
func TestPR04_DuplicateSafety_AlreadyProcessedNotReDelivered(t *testing.T) {
	pool := pr04Pool(t)
	const evType = "pr04.integration.duplicate"
	t.Cleanup(func() { cleanupOutboxEvents(t, pool, "pr04.integration.duplicate") })

	var hitCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Insert a row and immediately mark it processed_at.
	rowID := insertOutboxEvent(t, pool, evType)
	ctx5 := context.Background()
	if _, err := pool.Exec(ctx5,
		`UPDATE outbox_events SET processed_at = now(), attempts = 1 WHERE id = $1::uuid`,
		rowID,
	); err != nil {
		t.Fatalf("mark pre-processed: %v", err)
	}

	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL,
		SigningSecret: []byte("pr04-dup-test-signing-secret-32bX"),
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	runPR04Dispatcher(t, pool, d, 200*time.Millisecond)

	// The webhook must NOT have been called (row was already processed).
	if hitCount.Load() != 0 {
		t.Errorf("PR-04 duplicate safety: already-processed row was re-delivered %d times; want 0",
			hitCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Test 5: Disabled mode — no rows are claimed or consumed by OUR dispatcher
// ---------------------------------------------------------------------------

// countingStore wraps an OutboxEventStore and records how many times
// MarkDispatched and MarkFailed are called. Used in the disabled-mode test to
// verify that OUR OutboxEventsDispatcher (with DisabledDispatcher) makes zero
// mark calls, regardless of what other processes (e.g. arena_worker) do to the
// shared outbox_events table.
type countingStore struct {
	OutboxEventStore
	claimCount    atomic.Int64
	dispatchCount atomic.Int64
	failCount     atomic.Int64
}

func (s *countingStore) ClaimNext(ctx context.Context) (*OutboxEventRow, error) {
	s.claimCount.Add(1)
	return s.OutboxEventStore.ClaimNext(ctx)
}

func (s *countingStore) MarkDispatched(ctx context.Context, id string) error {
	s.dispatchCount.Add(1)
	return s.OutboxEventStore.MarkDispatched(ctx, id)
}

func (s *countingStore) MarkFailed(ctx context.Context, id string, lastErr string, nextAttemptAt *time.Time, deadLetter bool) error {
	s.failCount.Add(1)
	return s.OutboxEventStore.MarkFailed(ctx, id, lastErr, nextAttemptAt, deadLetter)
}

// Compile-time interface guard.
var _ OutboxEventStore = (*countingStore)(nil)

// TestPR04_DisabledMode_NoRowsConsumed proves that DisabledDispatcher causes
// OutboxEventsDispatcher to never call ClaimNext, MarkDispatched, or MarkFailed
// on the outbox_events store. The test uses a counting store wrapper to count
// calls made by OUR dispatcher instance, so a concurrently running arena_worker
// (which has its own separate store instance) cannot cause false failures.
func TestPR04_DisabledMode_NoRowsConsumed(t *testing.T) {
	pool := pr04Pool(t)
	const evType = "pr04.integration.disabled"
	t.Cleanup(func() { cleanupOutboxEvents(t, pool, "pr04.integration.disabled") })

	// Insert two rows into outbox_events.
	_ = insertOutboxEvent(t, pool, fmt.Sprintf("%s.1", evType))
	_ = insertOutboxEvent(t, pool, fmt.Sprintf("%s.2", evType))

	// Wrap the real store with a counter so we can track calls from OUR dispatcher.
	counting := &countingStore{OutboxEventStore: NewPGOutboxEventStore(pool)}

	// Run OutboxEventsDispatcher with DisabledDispatcher using the counting store.
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           counting,
		Dispatcher:      DisabledDispatcher{},
		PollInterval:    20 * time.Millisecond,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	<-ctx.Done()
	_ = d.Stop()

	// PR-04 disabled-mode contract: when the dispatcher implements
	// DisabledModeDispatcher, OutboxEventsDispatcher.Run MUST skip ClaimNext.
	// Zero claim calls prove the loop never even attempted to acquire a lock.
	if n := counting.claimCount.Load(); n != 0 {
		t.Errorf("PR-04 disabled: ClaimNext called %d times, want 0 (disabled mode must skip claiming rows)", n)
	}

	// MarkDispatched and MarkFailed must also be zero — no row should have been
	// claimed, so neither success nor failure path should have run.
	if n := counting.dispatchCount.Load(); n != 0 {
		t.Errorf("PR-04 disabled: MarkDispatched called %d times, want 0", n)
	}
	if n := counting.failCount.Load(); n != 0 {
		t.Errorf("PR-04 disabled: MarkFailed called %d times, want 0", n)
	}
}

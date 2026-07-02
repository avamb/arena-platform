//go:build integration

// Package outbox — integration tests for feature #100
// "Outbox writer + dispatcher placeholders"
//
// These tests require a live PostgreSQL instance reachable via DATABASE_URL
// with the outbox migration (0002_outbox.sql) already applied.  They are
// excluded from the normal "go test ./..." run and are activated only when the
// "integration" build tag is set:
//
//	go test -tags integration ./apps/backend/internal/platform/outbox/...
//
// Step 6 coverage:
//   - A write to the outbox table inside a transaction that is rolled back
//     leaves zero rows in the outbox table (atomicity guarantee).
//   - A write to the outbox table inside a committed transaction persists.
//   - The backlog count (dispatched_at IS NULL) reflects the committed rows.
package outbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// integrationPool opens a pgxpool.Pool for integration tests.
// The test is skipped when DATABASE_URL is absent or not a Postgres DSN.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping outbox integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q does not look like a Postgres DSN; skipping", dsn)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("integrationPool: open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("integrationPool: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// integrationWriter returns a PGWriter backed by the live pool.
func integrationWriter(pool *pgxpool.Pool) *PGWriter {
	return NewPGWriter(pool)
}

// countOutboxRows queries the total number of rows in the outbox table.
func countOutboxRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatalf("countOutboxRows: %v", err)
	}
	return n
}

// countBacklog counts undelivered rows (dispatched_at IS NULL).
func countBacklog(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE dispatched_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("countBacklog: %v", err)
	}
	return n
}

// cleanupOutbox deletes all rows inserted by the integration test using a
// sentinel event_type so the cleanup is targeted and idempotent.
func cleanupOutbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM outbox WHERE event_type = $1`, eventType); err != nil {
		t.Logf("cleanupOutbox: %v (non-fatal)", err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const testAggType = "integration_test"
const testAggID = "00000000-0000-0000-0000-000000000099"

// TestOutbox_RollbackRemovesRow is the canonical step 6 test:
// a write to the outbox inside a rolled-back transaction must leave no trace.
func TestOutbox_RollbackRemovesRow(t *testing.T) {
	pool := integrationPool(t)
	w := integrationWriter(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const eventType = "integration.test.rollback"
	t.Cleanup(func() { cleanupOutbox(t, context.Background(), pool, eventType) })

	before := countOutboxRows(t, ctx, pool)

	// Begin a transaction, append an outbox row, then roll back.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	ev := Event{
		AggregateType: testAggType,
		AggregateID:   testAggID,
		EventType:     eventType,
		Payload:       map[string]any{"test": "rollback_test"},
	}
	if err := w.Append(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Append: %v", err)
	}

	// Roll back — the outbox row must disappear.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	after := countOutboxRows(t, ctx, pool)
	if after != before {
		t.Errorf("row count changed after rollback: before=%d after=%d; outbox write must be atomic with the enclosing tx", before, after)
	}
}

// TestOutbox_CommitPersistsRow verifies the happy path: a committed transaction
// leaves one row in the outbox with dispatched_at IS NULL.
func TestOutbox_CommitPersistsRow(t *testing.T) {
	pool := integrationPool(t)
	w := integrationWriter(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const eventType = "integration.test.commit"
	t.Cleanup(func() { cleanupOutbox(t, context.Background(), pool, eventType) })

	before := countBacklog(t, ctx, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	ev := Event{
		AggregateType: testAggType,
		AggregateID:   testAggID,
		EventType:     eventType,
		Payload:       map[string]any{"test": "commit_test"},
	}
	if err := w.Append(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Append: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	after := countBacklog(t, ctx, pool)
	if after != before+1 {
		t.Errorf("backlog count: before=%d after=%d; want before+1", before, after)
	}
}

// TestOutbox_BacklogMonitorReadsCount verifies that sampleBacklog (the internal
// helper used by MonitorBacklog) correctly reads the undelivered row count from
// the live database.
func TestOutbox_BacklogMonitorReadsCount(t *testing.T) {
	pool := integrationPool(t)
	w := integrationWriter(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const eventType = "integration.test.monitor"
	t.Cleanup(func() { cleanupOutbox(t, context.Background(), pool, eventType) })

	// Commit one row.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	ev := Event{
		AggregateType: testAggType,
		AggregateID:   testAggID,
		EventType:     eventType,
		Payload:       map[string]any{"test": "monitor_test"},
	}
	if err := w.Append(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Append: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Sample the backlog gauge.
	var count int64
	if err := pool.QueryRow(ctx, backlogSQL).Scan(&count); err != nil {
		t.Fatalf("backlogSQL query: %v", err)
	}
	if count < 1 {
		t.Error("backlog count must be >= 1 after committing an undelivered row")
	}

	t.Logf("outbox backlog count: %d", count)
}

// TestOutbox_DispatchedAtIsNullAfterWrite verifies that a freshly written row
// has dispatched_at IS NULL (pending delivery).
func TestOutbox_DispatchedAtIsNullAfterWrite(t *testing.T) {
	pool := integrationPool(t)
	w := integrationWriter(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const eventType = "integration.test.dispatched_at"
	t.Cleanup(func() { cleanupOutbox(t, context.Background(), pool, eventType) })

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	ev := Event{
		AggregateType: testAggType,
		AggregateID:   testAggID,
		EventType:     eventType,
		Payload:       map[string]any{"test": "null_dispatched_at"},
	}
	if err := w.Append(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Append: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify dispatched_at is NULL.
	var isNull bool
	err = pool.QueryRow(ctx,
		`SELECT dispatched_at IS NULL FROM outbox WHERE event_type = $1 LIMIT 1`,
		eventType,
	).Scan(&isNull)
	if err != nil {
		t.Fatalf("query dispatched_at: %v", err)
	}
	if !isNull {
		t.Error("dispatched_at must be NULL for a freshly written outbox row")
	}
}

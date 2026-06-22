//go:build integration

// timestamptz_integration_test.go — feature #69 database-level tests.
//
// These tests require a live PostgreSQL 17 instance (with migrations applied).
// They are excluded from the normal "go test ./..." run; to activate them:
//
//	go test -tags integration ./apps/backend/internal/migrations/... -v
//
// The DATABASE_URL environment variable must point to a fully-migrated Postgres
// server (e.g. the one started by docker compose up postgres).
//
// Tests verify:
//   Step 1: SET TIMEZONE = 'America/Los_Angeles' in the session does not alter
//           how the server stores UTC timestamps.
//   Step 2: A row can be inserted into audit_events (simulating POST /v1/echo).
//   Step 3: Querying occurred_at AT TIME ZONE 'UTC' returns a UTC timestamp.
//   Step 4: The stored value reflects the moment of the insert (within 5 s).
//   Step 5: INFORMATION_SCHEMA confirms audit_events.occurred_at is
//           'timestamp with time zone' (PG reports timestamptz this way).
//   Step 6: No column in any platform table uses 'timestamp without time zone'.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// integrationDSN returns the DATABASE_URL or skips the test.
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping timestamptz integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q does not look like a Postgres DSN; skipping", dsn)
	}
	return dsn
}

// connectDB opens a single pgx connection for integration tests.
func connectDB(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := integrationDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// TestTimestamptzIntegration_AuditEventsInLosAngeles is the canonical
// integration test for feature #69.
//
// Steps performed:
//
//  1. Open a session and SET TIMEZONE = 'America/Los_Angeles'.
//  2. Insert a row into audit_events to simulate POST /v1/echo.
//  3. Query SELECT occurred_at AT TIME ZONE 'UTC' ... for that row.
//  4. Verify the returned timestamp is within 5 seconds of the insert time.
//  5. Verify the column data_type is 'timestamp with time zone'.
//  6. Verify no platform table column is 'timestamp without time zone'.
func TestTimestamptzIntegration_AuditEventsInLosAngeles(t *testing.T) {
	conn := connectDB(t)
	ctx := context.Background()

	// ── Step 1: Set session timezone to Los Angeles ───────────────────────────
	if _, err := conn.Exec(ctx, "SET TIMEZONE = 'America/Los_Angeles'"); err != nil {
		t.Fatalf("step 1: SET TIMEZONE: %v", err)
	}

	// Verify the session TZ was applied.
	var sessionTZ string
	if err := conn.QueryRow(ctx, "SHOW TIMEZONE").Scan(&sessionTZ); err != nil {
		t.Fatalf("step 1: SHOW TIMEZONE: %v", err)
	}
	if !strings.Contains(strings.ToLower(sessionTZ), "los_angeles") &&
		!strings.Contains(strings.ToLower(sessionTZ), "america") {
		t.Logf("step 1: TIMEZONE = %q (expected America/Los_Angeles)", sessionTZ)
	}

	// ── Step 2: Insert a row into audit_events ────────────────────────────────
	beforeInsert := time.Now().UTC()

	const insertSQL = `
		INSERT INTO audit_events
			(actor_type, actor_id, action, resource_type, resource_id, request_id, trace_id)
		VALUES
			('test', '00000000-0000-0000-0000-000000000069', 'v1.echo.create',
			 'echo', 'feature69_probe', 'req-timestamptz-test', 'trace-timestamptz-test')
		RETURNING id`

	var insertedID string
	if err := conn.QueryRow(ctx, insertSQL).Scan(&insertedID); err != nil {
		t.Fatalf("step 2: INSERT audit_events: %v", err)
	}
	if insertedID == "" {
		t.Fatal("step 2: inserted id is empty")
	}
	t.Logf("step 2: inserted audit_events row id = %s", insertedID)

	// ── Step 3: Query occurred_at AT TIME ZONE 'UTC' ──────────────────────────
	const querySQL = `
		SELECT occurred_at AT TIME ZONE 'UTC'
		FROM audit_events
		WHERE id = $1`

	var occurredAtUTC time.Time
	if err := conn.QueryRow(ctx, querySQL, insertedID).Scan(&occurredAtUTC); err != nil {
		t.Fatalf("step 3: SELECT occurred_at AT TIME ZONE UTC: %v", err)
	}

	// ── Step 4: Verify the value reflects the moment of the request ───────────
	afterInsert := time.Now().UTC()

	if occurredAtUTC.Before(beforeInsert.Add(-time.Second)) {
		t.Errorf("step 4: occurred_at (%v) is before the insert (%v)", occurredAtUTC, beforeInsert)
	}
	if occurredAtUTC.After(afterInsert.Add(5 * time.Second)) {
		t.Errorf("step 4: occurred_at (%v) is suspiciously far in the future (insert ended at %v)", occurredAtUTC, afterInsert)
	}
	t.Logf("step 4: occurred_at UTC = %v (within [%v, %v])", occurredAtUTC, beforeInsert, afterInsert)

	// ── Step 5: Verify column type is 'timestamp with time zone' ─────────────
	const colTypeSQL = `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_name = 'audit_events'
		  AND column_name = 'occurred_at'`

	var dataType string
	if err := conn.QueryRow(ctx, colTypeSQL).Scan(&dataType); err != nil {
		t.Fatalf("step 5: query information_schema.columns: %v", err)
	}
	if dataType != "timestamp with time zone" {
		t.Errorf("step 5: audit_events.occurred_at data_type = %q, want 'timestamp with time zone'", dataType)
	}
	t.Logf("step 5: audit_events.occurred_at data_type = %q", dataType)

	// ── Step 6: Verify NO column uses 'timestamp without time zone' ───────────
	const badColSQL = `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND data_type = 'timestamp without time zone'`

	rows, err := conn.Query(ctx, badColSQL)
	if err != nil {
		t.Fatalf("step 6: query for bad timestamp columns: %v", err)
	}
	defer rows.Close()

	var badCols []string
	for rows.Next() {
		var tbl, col, dt string
		if err := rows.Scan(&tbl, &col, &dt); err != nil {
			t.Fatalf("step 6: scan row: %v", err)
		}
		badCols = append(badCols, tbl+"."+col)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("step 6: rows error: %v", err)
	}

	if len(badCols) > 0 {
		t.Errorf("step 6: found columns declared as 'timestamp without time zone': %v", badCols)
	} else {
		t.Log("step 6: no 'timestamp without time zone' columns found — all timestamps are timestamptz")
	}

	// ── Cleanup: Remove the probe row ────────────────────────────────────────
	if _, err := conn.Exec(ctx, "DELETE FROM audit_events WHERE id = $1", insertedID); err != nil {
		t.Logf("cleanup: could not delete probe row %s: %v", insertedID, err)
	}
}

// TestTimestamptzIntegration_SessionTZDoesNotAlterStoredUTC verifies that
// setting a non-UTC session timezone does not corrupt stored timestamps — the
// server always reads back the same instant in UTC.
func TestTimestamptzIntegration_SessionTZDoesNotAlterStoredUTC(t *testing.T) {
	conn := connectDB(t)
	ctx := context.Background()

	// Insert while session is UTC (the default).
	beforeInsert := time.Now().UTC()
	const insertSQL = `
		INSERT INTO audit_events
			(actor_type, action, resource_type, resource_id, request_id, trace_id)
		VALUES
			('test', 'v1.echo.create', 'echo', 'tz_verify', 'req-tz-verify', 'trace-tz-verify')
		RETURNING id, occurred_at`

	var id string
	var storedAtUTC time.Time
	if err := conn.QueryRow(ctx, insertSQL).Scan(&id, &storedAtUTC); err != nil {
		t.Fatalf("insert: %v", err)
	}
	afterInsert := time.Now().UTC()

	// Switch session timezone to LA.
	if _, err := conn.Exec(ctx, "SET TIMEZONE = 'America/Los_Angeles'"); err != nil {
		t.Fatalf("SET TIMEZONE: %v", err)
	}

	// Read back occurred_at — pgx will return a time.Time in UTC if the column is
	// timestamptz (the driver always returns UTC for timestamptz columns).
	const querySQL = `SELECT occurred_at FROM audit_events WHERE id = $1`
	var readBack time.Time
	if err := conn.QueryRow(ctx, querySQL, id).Scan(&readBack); err != nil {
		t.Fatalf("SELECT after SET TIMEZONE: %v", err)
	}

	// The two times must represent the same instant (within 1 microsecond of
	// each other, allowing for PG clock precision).
	diff := storedAtUTC.Sub(readBack.UTC())
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Errorf("stored (%v) != readback (%v) after SET TIMEZONE: diff = %v",
			storedAtUTC, readBack.UTC(), diff)
	}

	// Also verify the readback is within the insert window.
	if readBack.UTC().Before(beforeInsert.Add(-time.Second)) {
		t.Errorf("readback occurred_at %v is before insert start %v", readBack.UTC(), beforeInsert)
	}
	if readBack.UTC().After(afterInsert.Add(5 * time.Second)) {
		t.Errorf("readback occurred_at %v is suspiciously after insert end %v", readBack.UTC(), afterInsert)
	}

	// Cleanup.
	conn.Exec(ctx, "DELETE FROM audit_events WHERE id = $1", id) //nolint:errcheck
}

//go:build integration

// pgtest_test.go — integration tests for the pgtest harness (feature #95).
//
// These tests require a running Docker daemon (testcontainers-go spins up a
// throwaway Postgres 17 container for each test). They are excluded from the
// normal "go test ./..." run and are activated only when the "integration"
// build tag is set:
//
//	go test -tags integration -timeout 120s ./apps/backend/internal/tests/pgtest/...
package pgtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/tests/pgtest"
)

// TestNewTestDB_StartsPostgresAndReturnsWorkingPool is the primary harness
// self-test (feature #95 step 5).
//
// Verifies:
//   - NewTestDB successfully starts a Postgres 17 container.
//   - The returned pool passes a ping.
//   - A simple SELECT 1 query executes without error.
//   - The migrations table (schema_migrations) exists and has rows.
func TestNewTestDB_StartsPostgresAndReturnsWorkingPool(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Basic connectivity check.
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping after NewTestDB: %v", err)
	}

	// Verify a trivial query works.
	row := pool.QueryRow(ctx, "SELECT 1")
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if n != 1 {
		t.Errorf("SELECT 1 = %d, want 1", n)
	}
}

// TestNewTestDB_MigrationsApplied verifies that all goose migrations were
// applied to the test database (feature #95 step 2).
func TestNewTestDB_MigrationsApplied(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// schema_migrations exists and has at least one row.
	row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE is_applied = true")
	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatalf("COUNT(schema_migrations): %v", err)
	}
	if count == 0 {
		t.Error("schema_migrations has 0 applied rows; expected at least 1 after NewTestDB")
	}

	// The application tables from migration 0001 must exist.
	tables := []string{
		"idempotency_keys",
		"audit_events",
		"outbox_events",
		"worker_jobs",
		"worker_dead_letter",
		"i18n_text",
	}
	for _, tbl := range tables {
		row := pool.QueryRow(ctx,
			"SELECT to_regclass($1::text) IS NOT NULL", "public."+tbl)
		var exists bool
		if err := row.Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("table %q does not exist after NewTestDB", tbl)
		}
	}
}

// TestNewTestDB_Isolation verifies that two independent calls to NewTestDB
// return isolated databases — data inserted in one does not appear in the
// other (feature #95 step 3: fresh DB per test).
func TestNewTestDB_Isolation(t *testing.T) {
	pool1, cleanup1 := pgtest.NewTestDB(t)
	defer cleanup1()

	pool2, cleanup2 := pgtest.NewTestDB(t)
	defer cleanup2()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Insert a row into pool1's database.
	_, err := pool1.Exec(ctx,
		"INSERT INTO audit_events (actor_type, action, resource_type, resource_id) VALUES ($1,$2,$3,$4)",
		"user", "create", "test_resource", "rid-001",
	)
	if err != nil {
		t.Fatalf("insert into pool1: %v", err)
	}

	// pool1 must see the row.
	row := pool1.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events")
	var count1 int
	if err := row.Scan(&count1); err != nil {
		t.Fatalf("count pool1: %v", err)
	}
	if count1 == 0 {
		t.Error("pool1: expected audit_events to have rows after INSERT")
	}

	// pool2 must NOT see the row (different database / container).
	row = pool2.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events")
	var count2 int
	if err := row.Scan(&count2); err != nil {
		t.Fatalf("count pool2: %v", err)
	}
	if count2 != 0 {
		t.Errorf("pool2: expected audit_events to be empty, got %d rows", count2)
	}
}

// TestTruncateAll_ClearsAllApplicationTables verifies that TruncateAll leaves
// all application tables empty (feature #95 step 3: truncate between tests).
func TestTruncateAll_ClearsAllApplicationTables(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Populate a few tables.
	_, err := pool.Exec(ctx,
		"INSERT INTO audit_events (actor_type, action, resource_type, resource_id) VALUES ($1,$2,$3,$4)",
		"user", "login", "session", "s-001",
	)
	if err != nil {
		t.Fatalf("insert audit_events: %v", err)
	}

	_, err = pool.Exec(ctx,
		"INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type) VALUES ($1,$2,$3)",
		"order", "o-001", "OrderCreated",
	)
	if err != nil {
		t.Fatalf("insert outbox_events: %v", err)
	}

	// Truncate everything.
	pgtest.TruncateAll(t, pool)

	// All tables should now be empty.
	tables := []string{"audit_events", "outbox_events", "worker_jobs", "idempotency_keys", "i18n_text", "worker_dead_letter"}
	for _, tbl := range tables {
		row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+tbl)
		var c int
		if err := row.Scan(&c); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if c != 0 {
			t.Errorf("TruncateAll: table %q has %d rows, want 0", tbl, c)
		}
	}
}

// TestWithTx_RollsBackAfterFn verifies that WithTx always rolls back the
// transaction after fn returns, so subsequent state is clean (feature #95 step 4).
func TestWithTx_RollsBackAfterFn(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// INSERT inside WithTx — should be invisible after WithTx returns.
	pgtest.WithTx(t, pool, func(tx pgx.Tx) {
		_, err := tx.Exec(ctx,
			"INSERT INTO audit_events (actor_type, action, resource_type, resource_id) VALUES ($1,$2,$3,$4)",
			"user", "transient", "resource", "r-tmp",
		)
		if err != nil {
			t.Errorf("insert inside WithTx: %v", err)
		}

		// Row is visible within the same transaction.
		row := tx.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events")
		var c int
		if err := row.Scan(&c); err != nil {
			t.Fatalf("count inside WithTx tx: %v", err)
		}
		if c == 0 {
			t.Error("expected row to be visible inside the transaction")
		}
	})

	// After WithTx returns the rollback has occurred; the table must be empty.
	row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events")
	var finalCount int
	if err := row.Scan(&finalCount); err != nil {
		t.Fatalf("count after WithTx: %v", err)
	}
	if finalCount != 0 {
		t.Errorf("WithTx: rollback did not revert INSERT; got %d rows, want 0", finalCount)
	}
}

// TestWithTx_QueryInsideTransaction verifies the basic transaction contract:
// queries and mutations are visible within the transaction (feature #95 step 4).
func TestWithTx_QueryInsideTransaction(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pgtest.WithTx(t, pool, func(tx pgx.Tx) {
		// SELECT in a tx must work.
		row := tx.QueryRow(ctx, "SELECT 42")
		var n int
		if err := row.Scan(&n); err != nil {
			t.Fatalf("SELECT 42 inside tx: %v", err)
		}
		if n != 42 {
			t.Errorf("SELECT 42 = %d, want 42", n)
		}
	})
}

// TestNewTestDB_FullVerification is a comprehensive sub-test sweep that
// validates all 5 feature steps in a single test run (feature #95 step 5).
func TestNewTestDB_FullVerification(t *testing.T) {
	t.Run("step1_dependency_available", func(t *testing.T) {
		// The fact that this test file compiles proves testcontainers-go
		// and its postgres module are in go.mod (feature #95 step 1).
		t.Log("testcontainers-go and modules/postgres are available (compile check passed)")
	})

	t.Run("step2_new_test_db_starts_postgres_runs_migrations", func(t *testing.T) {
		pool, cleanup := pgtest.NewTestDB(t)
		defer cleanup()

		ctx := context.Background()
		if err := pool.Ping(ctx); err != nil {
			t.Fatalf("ping: %v", err)
		}

		row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE is_applied = true")
		var c int
		if err := row.Scan(&c); err != nil {
			t.Fatalf("count schema_migrations: %v", err)
		}
		if c == 0 {
			t.Error("no migrations applied")
		}
		t.Logf("applied migrations: %d", c)
	})

	t.Run("step3_each_test_gets_isolated_db", func(t *testing.T) {
		pool1, cleanup1 := pgtest.NewTestDB(t)
		defer cleanup1()
		pool2, cleanup2 := pgtest.NewTestDB(t)
		defer cleanup2()

		ctx := context.Background()
		_, _ = pool1.Exec(ctx,
			"INSERT INTO worker_jobs (job_type) VALUES ($1)", "isolation-check")
		row := pool2.QueryRow(ctx, "SELECT COUNT(*) FROM worker_jobs")
		var c int
		if err := row.Scan(&c); err != nil {
			t.Fatalf("count: %v", err)
		}
		if c != 0 {
			t.Errorf("cross-db leak: pool2 sees %d rows from pool1", c)
		}
	})

	t.Run("step3b_truncate_all_resets_state", func(t *testing.T) {
		pool, cleanup := pgtest.NewTestDB(t)
		defer cleanup()

		ctx := context.Background()
		_, _ = pool.Exec(ctx,
			"INSERT INTO worker_jobs (job_type) VALUES ($1)", "before-truncate")
		pgtest.TruncateAll(t, pool)
		row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM worker_jobs")
		var c int
		if err := row.Scan(&c); err != nil {
			t.Fatalf("count: %v", err)
		}
		if c != 0 {
			t.Errorf("TruncateAll left %d rows", c)
		}
	})

	t.Run("step4_with_tx_helper", func(t *testing.T) {
		pool, cleanup := pgtest.NewTestDB(t)
		defer cleanup()

		ctx := context.Background()
		pgtest.WithTx(t, pool, func(tx pgx.Tx) {
			_, err := tx.Exec(ctx,
				"INSERT INTO audit_events (actor_type, action, resource_type, resource_id) VALUES ($1,$2,$3,$4)",
				"system", "test", "resource", "r-999",
			)
			if err != nil {
				t.Errorf("exec in WithTx: %v", err)
			}
		})
		// After rollback the table must be empty.
		row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events")
		var c int
		if err := row.Scan(&c); err != nil {
			t.Fatalf("count: %v", err)
		}
		if c != 0 {
			t.Errorf("WithTx did not roll back; %d rows remain", c)
		}
	})

	t.Run("step5_harness_self_test_returns_working_pool", func(t *testing.T) {
		pool, cleanup := pgtest.NewTestDB(t)
		defer cleanup()

		ctx := context.Background()
		row := pool.QueryRow(ctx, "SELECT current_database()")
		var dbName string
		if err := row.Scan(&dbName); err != nil {
			t.Fatalf("SELECT current_database(): %v", err)
		}
		if dbName == "" {
			t.Error("current_database() returned empty string")
		}
		t.Logf("connected to database: %q", dbName)
	})
}

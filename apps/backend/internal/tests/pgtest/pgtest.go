// Package pgtest provides a test helper for integration tests that need a real
// PostgreSQL database. It uses testcontainers-go to spin up a throwaway
// Postgres 17 container for each test run, applies all goose migrations from
// the embedded FS, and returns a connected *pgxpool.Pool.
//
// Usage (integration build tag required):
//
//	func TestMyFeature(t *testing.T) {
//	    pool, cleanup := pgtest.NewTestDB(t)
//	    defer cleanup()
//
//	    // Use pool for queries…
//	    pgtest.TruncateAll(t, pool)  // between sub-tests
//	}
//
//	func TestWithTransaction(t *testing.T) {
//	    pool, cleanup := pgtest.NewTestDB(t)
//	    defer cleanup()
//
//	    pgtest.WithTx(t, pool, func(tx pgx.Tx) {
//	        // tx is rolled back automatically after fn returns.
//	    })
//	}
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// _ is imported for its side effect: registering the "pgx" driver with
// database/sql so goose can use it.
var _ = stdlib.GetDefaultDriver

// NewTestDB starts a throwaway PostgreSQL 17 container, applies all goose
// migrations via the embedded FS, and returns a connected *pgxpool.Pool and
// a cleanup function.
//
// The cleanup function stops and removes the container. It is safe to call
// cleanup more than once.
//
// Requires Docker to be reachable from the test environment.
func NewTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()

	ctx := context.Background()

	// Start a Postgres 17 container with the default "test" credentials.
	ctr, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("pgtest: start postgres container: %v", err)
	}

	cleanup := func() {
		// Terminate is idempotent if already stopped.
		if termErr := ctr.Terminate(ctx); termErr != nil {
			t.Logf("pgtest: cleanup: terminate container: %v", termErr)
		}
	}

	// Obtain the host:port DSN from the container.
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		t.Fatalf("pgtest: get connection string: %v", err)
	}

	// Apply goose migrations using database/sql (goose's API requirement).
	if err := runMigrations(ctx, dsn); err != nil {
		cleanup()
		t.Fatalf("pgtest: run migrations: %v", err)
	}

	// Open a pgxpool.Pool for the caller.
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		cleanup()
		t.Fatalf("pgtest: parse DSN for pool: %v", err)
	}
	poolCfg.MinConns = 1
	poolCfg.MaxConns = 5

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		cleanup()
		t.Fatalf("pgtest: create pool: %v", err)
	}

	// Ping the pool with a tight deadline to surface connectivity issues early.
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		cleanup()
		t.Fatalf("pgtest: pool ping: %v", err)
	}

	// Wrap the pool cleanup to also close the pool before tearing down the container.
	fullCleanup := func() {
		pool.Close()
		cleanup()
	}

	return pool, fullCleanup
}

// runMigrations opens a database/sql connection and applies all goose
// migrations from the embedded FS. The connection is closed after migrations
// complete.
func runMigrations(ctx context.Context, dsn string) error {
	db, err := goose.OpenDBWithDriver("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("schema_migrations")
	// Suppress goose's own logging in tests (it uses Printf/Fatalf).
	goose.SetLogger(goose.NopLogger())

	migrateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if err := goose.UpContext(migrateCtx, db, migrations.Dir); err != nil {
		return fmt.Errorf("up: %w", err)
	}
	return nil
}

// TruncateAll truncates all application tables in the test database, resetting
// them to empty state for the next test. The schema_migrations table is left
// intact so migrations do not need to be re-applied.
//
// Call TruncateAll at the start (or end) of each sub-test when multiple
// sub-tests share a single pool from NewTestDB.
func TruncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	// Application tables defined in the migrations. The order respects FK
	// constraints (child before parent where applicable). Since all tables
	// in the foundation milestone are independent, plain alphabetical order
	// is fine here.
	const q = `
		TRUNCATE TABLE
			audit_events,
			i18n_text,
			idempotency_keys,
			outbox_events,
			worker_dead_letter,
			worker_jobs
		RESTART IDENTITY CASCADE
	`
	if _, err := pool.Exec(ctx, q); err != nil {
		t.Fatalf("pgtest: TruncateAll: %v", err)
	}
}

// WithTx runs fn inside an explicit pgx.Tx. The transaction is always rolled
// back after fn returns, even if fn panics or returns an error — this keeps
// each call isolated from subsequent calls without needing TruncateAll.
//
// Typical usage:
//
//	pgtest.WithTx(t, pool, func(tx pgx.Tx) {
//	    _, err := tx.Exec(ctx, "INSERT INTO ...")
//	    if err != nil { t.Fatal(err) }
//	})
func WithTx(t *testing.T, pool *pgxpool.Pool, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("pgtest: WithTx: begin: %v", err)
	}

	defer func() {
		// Always roll back — the test transaction is never committed so DB
		// state is automatically reverted even when fn returns nil.
		// ErrTxClosed is ignored: it means fn already committed/rolled back.
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			t.Logf("pgtest: WithTx: rollback: %v", rbErr)
		}
	}()

	fn(tx)
}


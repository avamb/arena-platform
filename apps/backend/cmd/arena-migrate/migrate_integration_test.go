//go:build integration

// Package main — integration tests for arena-migrate (feature #93, step 6).
//
// These tests require a live PostgreSQL instance reachable via DATABASE_URL.
// They are excluded from the normal "go test ./..." run and are activated
// only when the "integration" build tag is set:
//
//	go test -tags integration ./apps/backend/cmd/arena-migrate/...
//
// Each test calls the goose API directly (the same call path as the
// production run() function) so the results are equivalent to running
// the arena-migrate binary against a real database.
//
// The tests are designed to be idempotent: they call goose.UpContext to
// ensure migrations are applied before making assertions.  Re-running the
// suite against an already-migrated database is safe.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// integrationMigrateDB opens a database/sql handle for migration integration
// tests using the pgx stdlib driver (same driver as the production binary).
// The test is skipped when DATABASE_URL is absent or not a Postgres DSN.
func integrationMigrateDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping migration integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q does not look like a Postgres DSN; skipping", dsn)
	}

	// Ensure the pgx database/sql driver is registered (side-effect import).
	_ = stdlib.GetDefaultDriver

	db, err := goose.OpenDBWithDriver("pgx", dsn)
	if err != nil {
		t.Fatalf("integrationMigrateDB: open DB: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Logf("integrationMigrateDB: close DB: %v", closeErr)
		}
	})
	return db
}

// configureGooseIntegration applies the same global goose configuration that
// the production run() function uses: postgres dialect, embedded FS,
// schema_migrations table name, and a silent logger that suppresses noisy
// goose output in test logs.
func configureGooseIntegration(t *testing.T) {
	t.Helper()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("schema_migrations")
	goose.SetLogger(&integrationSilentLogger{t: t})
}

// integrationSilentLogger satisfies goose.Logger.  Printf is silenced;
// Fatalf panics to surface the error through the test harness (consistent
// with the production gooseLogger.Fatalf which also panics).
type integrationSilentLogger struct{ t *testing.T }

func (l *integrationSilentLogger) Printf(format string, v ...interface{}) {
	// Suppress routine goose output (migration applied lines, etc.).
	// Use t.Logf only for debugging: uncomment if you need verbose output.
	// l.t.Logf("[goose] "+format, v...)
}

func (l *integrationSilentLogger) Fatalf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	l.t.Logf("[goose fatal] %s", msg)
	panic("goose fatal: " + msg)
}

// Compile-time guard: integrationSilentLogger must satisfy goose.Logger.
// goose.Logger is an interface; if its methods change this line will fail.
var _ goose.Logger = (*integrationSilentLogger)(nil)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestMigrateUp_AppliesBaselineMigration verifies that goose.UpContext
// applies the embedded baseline migration and records at least one row in
// schema_migrations as applied.
//
// Feature #93 step 6: arena-migrate up applies 0001_init.sql.
func TestMigrateUp_AppliesBaselineMigration(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Apply all pending migrations (no-op when already applied).
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	// Verify at least one applied row exists in schema_migrations.
	var appliedCount int
	row := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE is_applied = true")
	if err := row.Scan(&appliedCount); err != nil {
		t.Fatalf("query schema_migrations applied count: %v", err)
	}
	if appliedCount < 1 {
		t.Errorf("schema_migrations applied count = %d; want >= 1 after up", appliedCount)
	}

	// Verify the version API returns a positive number.
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("goose.GetDBVersionContext: %v", err)
	}
	if version <= 0 {
		t.Errorf("schema version after up = %d; want > 0", version)
	}

	t.Logf("baseline migration applied: version=%d applied_rows=%d", version, appliedCount)
}

// TestMigrateUp_IsIdempotent verifies that calling goose.UpContext a second
// time (when no pending migrations remain) succeeds without error and leaves
// the schema version unchanged.
//
// Feature #93 step 6: "arena-migrate up идемпотентен".
func TestMigrateUp_IsIdempotent(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// First run — apply all pending migrations.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("first goose.UpContext: %v", err)
	}
	v1, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext after first up: %v", err)
	}
	if v1 <= 0 {
		t.Fatalf("version after first up = %d; want > 0", v1)
	}

	// Second run — must be a no-op: same version, no error.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("second goose.UpContext (idempotency check): %v", err)
	}
	v2, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext after second up: %v", err)
	}
	if v2 != v1 {
		t.Errorf("version changed after idempotent up: %d -> %d; want unchanged", v1, v2)
	}

	t.Logf("idempotent up verified: version stable at %d", v2)
}

// TestMigrateStatus_ShowsApplied verifies that after goose.UpContext the
// baseline migration (version 1, matching sequence number in 0001_init.sql)
// is recorded in schema_migrations as applied.
//
// Feature #93 step 6: "status показывает applied".
func TestMigrateStatus_ShowsApplied(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Ensure migrations are applied before asserting.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	// Baseline migration is sequence 1; goose records version_id=1 for 0001_init.sql.
	var versionID int64
	var isApplied bool
	row := db.QueryRowContext(ctx,
		`SELECT version_id, is_applied
		   FROM schema_migrations
		  WHERE version_id = 1
		  ORDER BY id DESC
		  LIMIT 1`)
	if err := row.Scan(&versionID, &isApplied); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("baseline migration (version_id=1) not found in schema_migrations after up")
		}
		t.Fatalf("query schema_migrations version 1: %v", err)
	}
	if !isApplied {
		t.Errorf("schema_migrations version_id=%d is_applied=false; want true", versionID)
	}

	t.Logf("schema_migrations: version_id=%d is_applied=%v (status=applied)", versionID, isApplied)
}

// TestMigrateVersion_ReturnsPositiveAfterUp mirrors the `arena-migrate version`
// subcommand — verifies goose.GetDBVersionContext returns the correct version
// number after applying all embedded migrations.
func TestMigrateVersion_ReturnsPositiveAfterUp(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("goose.GetDBVersionContext: %v", err)
	}
	if version <= 0 {
		t.Errorf("version = %d; want > 0 (baseline migration should be version 1)", version)
	}

	t.Logf("current schema version: %d", version)
}

// TestMigrateSchemaHasExpectedTables verifies that the platform tables created
// by 0001_init.sql actually exist in the database after arena-migrate up.
// This guards against silent partial migration failures.
func TestMigrateSchemaHasExpectedTables(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	wantTables := []string{
		"idempotency_keys",
		"audit_events",
		"outbox_events",
		"worker_jobs",
		"worker_dead_letter",
		"i18n_text",
	}

	for _, table := range wantTables {
		t.Run(table, func(t *testing.T) {
			var exists bool
			row := db.QueryRowContext(ctx,
				`SELECT EXISTS (
					SELECT 1
					FROM   information_schema.tables
					WHERE  table_schema = 'public'
					AND    table_name   = $1
				)`, table)
			if err := row.Scan(&exists); err != nil {
				t.Fatalf("check table %q exists: %v", table, err)
			}
			if !exists {
				t.Errorf("table %q not found in public schema after up", table)
			}
		})
	}
}

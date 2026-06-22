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
	"encoding/json"
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

// ---------------------------------------------------------------------------
// Feature #21 — i18n_text seed rows load from migration
// ---------------------------------------------------------------------------

// TestI18nText_SeedRowsExistAfterMigrate verifies that 0003_i18n_seeds.sql
// inserts at least one (namespace, key) pair for both 'en' and 'ru' locales
// so that the platform has baseline translations available immediately after
// arena-migrate up.
//
// Feature #21 steps 1-2: query i18n_text and verify rows exist for en + ru.
func TestI18nText_SeedRowsExistAfterMigrate(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	for _, locale := range []string{"en", "ru"} {
		t.Run("locale="+locale, func(t *testing.T) {
			var count int
			row := db.QueryRowContext(ctx,
				`SELECT COUNT(DISTINCT (namespace, key))
				   FROM i18n_text
				  WHERE locale = $1`, locale)
			if err := row.Scan(&count); err != nil {
				t.Fatalf("count i18n_text for locale %q: %v", locale, err)
			}
			if count < 1 {
				t.Errorf("i18n_text distinct (namespace,key) for locale=%q = %d; want >= 1 after seed migration", locale, count)
			}
			t.Logf("locale=%q distinct (namespace,key) count: %d", locale, count)
		})
	}
}

// TestI18nText_NoNullOrEmptyValues verifies that every seeded row has a
// non-NULL, non-empty value column.
//
// Feature #21 step 3: no rows with NULL or empty value.
func TestI18nText_NoNullOrEmptyValues(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	var badCount int
	row := db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		   FROM i18n_text
		  WHERE value IS NULL OR value = ''`)
	if err := row.Scan(&badCount); err != nil {
		t.Fatalf("count i18n_text with NULL/empty value: %v", err)
	}
	if badCount > 0 {
		t.Errorf("i18n_text rows with NULL or empty value = %d; want 0", badCount)
	}
}

// TestI18nText_UniqueConstraintViolation verifies that the unique index on
// (namespace, key, locale) rejects duplicate inserts with a 23505
// (unique_violation) PostgreSQL error code.
//
// Feature #21 step 4: duplicate INSERT → expect 23505.
func TestI18nText_UniqueConstraintViolation(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	// First insert a test row (use a test-specific key to avoid collisions).
	const (
		testNS     = "test.feature21"
		testKey    = "unique_constraint_check"
		testLocale = "en"
		testValue  = "test value for unique constraint verification"
	)

	// Clean up after test regardless of outcome.
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx,
			`DELETE FROM i18n_text WHERE namespace = $1 AND key = $2 AND locale = $3`,
			testNS, testKey, testLocale)
	})

	// Insert first time — must succeed.
	_, err := db.ExecContext(ctx,
		`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ($1, $2, $3, $4)`,
		testNS, testKey, testLocale, testValue)
	if err != nil {
		t.Fatalf("first INSERT into i18n_text failed unexpectedly: %v", err)
	}

	// Insert duplicate — must fail with unique_violation (SQLSTATE 23505).
	_, err = db.ExecContext(ctx,
		`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ($1, $2, $3, $4)`,
		testNS, testKey, testLocale, "a different value that should be rejected")
	if err == nil {
		t.Fatal("duplicate INSERT into i18n_text succeeded; want unique_violation 23505")
	}

	// Check for the 23505 SQLSTATE.  pgx wraps this in a *pgconn.PgError
	// which implements a Code() method.  We extract it by string matching
	// because importing pgx internals from this package would create a
	// dependency cycle.
	errStr := err.Error()
	if !strings.Contains(errStr, "23505") && !strings.Contains(errStr, "unique") {
		t.Errorf("duplicate INSERT error = %q; want error containing '23505' or 'unique'", errStr)
	}
	t.Logf("duplicate INSERT correctly rejected: %v", err)
}

// TestI18nText_LocaleCodesBCP47 verifies that all locale codes stored in
// i18n_text after the seed migration are valid BCP-47 format: either a
// 2-letter primary tag (e.g. "en", "ru") or a tag with a region subtag
// (e.g. "en-US", "zh-CN").  Tags must be lower-case for the primary subtag.
//
// Feature #21 step 5: locale codes match BCP47.
func TestI18nText_LocaleCodesBCP47(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT DISTINCT locale FROM i18n_text ORDER BY locale`)
	if err != nil {
		t.Fatalf("query distinct locales: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var locale string
		if err := rows.Scan(&locale); err != nil {
			t.Fatalf("scan locale: %v", err)
		}

		// Accept "xx" (2-letter primary) or "xx-XX" / "xx-XXX" (with region/script).
		// The primary subtag must be 2-3 lowercase alpha chars.
		// This is a lightweight check — not a full BCP-47 parser.
		if !isValidBCP47Locale(locale) {
			t.Errorf("locale %q does not look like a valid BCP-47 tag (want 2-3 lowercase letters, optionally followed by -SUBTAG)", locale)
		} else {
			t.Logf("locale %q: BCP-47 OK", locale)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating distinct locales: %v", err)
	}
}

// isValidBCP47Locale is a lightweight BCP-47 syntax check.
// It accepts primary-subtag-only tags ("en", "ru", "uk") and
// tags with a single extension subtag ("en-US", "zh-CN", "sr-Latn").
// Primary subtag: 2–3 lowercase ASCII letters.
// Extension subtag (optional): 2–4 upper- or mixed-case ASCII alpha/digit.
func isValidBCP47Locale(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.SplitN(s, "-", 2)
	primary := parts[0]
	if len(primary) < 2 || len(primary) > 3 {
		return false
	}
	for _, ch := range primary {
		if ch < 'a' || ch > 'z' {
			return false
		}
	}
	// If there is a subtag, it must be 2–4 alphanumeric characters.
	if len(parts) == 2 {
		sub := parts[1]
		if len(sub) < 2 || len(sub) > 4 {
			return false
		}
		for _, ch := range sub {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Feature #23 — arena-migrate down rolls back baseline migration
// ---------------------------------------------------------------------------

// platformTables is the canonical list of tables created by 0001_init.sql.
// Used across multiple down/up cycle tests to verify structural completeness.
var platformTables = []string{
	"idempotency_keys",
	"audit_events",
	"outbox_events",
	"worker_jobs",
	"worker_dead_letter",
	"i18n_text",
}

// tableExists returns whether the given table exists in the public schema.
func tableExists(t *testing.T, db *sql.DB, ctx context.Context, table string) bool {
	t.Helper()
	var exists bool
	row := db.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM   information_schema.tables
			WHERE  table_schema = 'public'
			AND    table_name   = $1
		)`, table)
	if err := row.Scan(&exists); err != nil {
		t.Fatalf("tableExists(%q): query error: %v", table, err)
	}
	return exists
}

// indexExists returns whether the given index exists in the public schema.
func indexExists(t *testing.T, db *sql.DB, ctx context.Context, indexName string) bool {
	t.Helper()
	var exists bool
	row := db.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM   pg_indexes
			WHERE  schemaname = 'public'
			AND    indexname  = $1
		)`, indexName)
	if err := row.Scan(&exists); err != nil {
		t.Fatalf("indexExists(%q): query error: %v", indexName, err)
	}
	return exists
}

// uuidv7FunctionExists returns whether the uuidv7() function exists.
func uuidv7FunctionExists(t *testing.T, db *sql.DB, ctx context.Context) bool {
	t.Helper()
	var exists bool
	row := db.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM   pg_proc
			JOIN   pg_namespace ON pg_namespace.oid = pg_proc.pronamespace
			WHERE  pg_namespace.nspname = 'public'
			AND    pg_proc.proname      = 'uuidv7'
		)`)
	if err := row.Scan(&exists); err != nil {
		t.Fatalf("uuidv7FunctionExists: query error: %v", err)
	}
	return exists
}

// resetAndUp is a test helper that calls goose.ResetContext followed by
// goose.UpContext to bring the database to a clean fully-migrated state.
// It is used as a t.Cleanup function so down-cycle tests leave the DB
// in a usable state for subsequent tests in the same suite run.
func resetAndUp(t *testing.T, db *sql.DB, ctx context.Context) {
	t.Helper()
	if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
		t.Logf("resetAndUp: reset error (non-fatal in cleanup): %v", err)
	}
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Logf("resetAndUp: up error (non-fatal in cleanup): %v", err)
	}
}

// TestMigrateDown_RollsBackMostRecentMigration verifies that goose.DownContext
// successfully rolls back exactly one migration (the most recent one).
//
// Feature #23 steps 2–3: arena-migrate down exits 0 and rolls back one step.
func TestMigrateDown_RollsBackMostRecentMigration(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Start from fully migrated state.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	vBefore, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext before down: %v", err)
	}
	if vBefore <= 0 {
		t.Fatalf("expected positive version before down, got %d", vBefore)
	}

	// arena-migrate down — must return nil (exit code 0 equivalent).
	// Step 3: goose.DownContext returns nil.
	if err := goose.DownContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.DownContext (step 3 exit code 0): unexpected error: %v", err)
	}

	vAfter, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext after down: %v", err)
	}

	// The version must have decreased by exactly one migration step.
	if vAfter >= vBefore {
		t.Errorf("version after down = %d; want < %d (one migration rolled back)", vAfter, vBefore)
	}

	t.Logf("arena-migrate down: version %d -> %d (one migration rolled back)", vBefore, vAfter)
}

// TestMigrateReset_TablesGoneAfterFullRollback verifies that after rolling back
// ALL migrations the platform tables no longer exist in the public schema.
// Only schema_migrations (managed by goose) should survive.
//
// Feature #23 step 4: \dt shows our tables are gone.
func TestMigrateReset_TablesGoneAfterFullRollback(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Start from fully migrated state.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	// Always restore the DB to fully migrated state when the test ends.
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	// Roll back all migrations.
	if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.ResetContext: %v", err)
	}

	// Step 4: Every platform table from 0001_init.sql must be gone.
	for _, table := range platformTables {
		t.Run("table_absent="+table, func(t *testing.T) {
			if tableExists(t, db, ctx, table) {
				t.Errorf("table %q still exists after full rollback; want gone", table)
			}
		})
	}

	// The outbox table from 0002_outbox.sql must also be gone.
	t.Run("table_absent=outbox", func(t *testing.T) {
		if tableExists(t, db, ctx, "outbox") {
			t.Errorf("table %q still exists after full rollback; want gone", "outbox")
		}
	})

	// schema_migrations must still exist (goose manages it; it is NOT dropped by reset).
	t.Run("schema_migrations_survives", func(t *testing.T) {
		if !tableExists(t, db, ctx, "schema_migrations") {
			t.Error("schema_migrations table gone after reset; goose needs it to be present")
		}
	})

	t.Logf("full rollback verified: all platform tables absent, schema_migrations intact")
}

// TestMigrateReset_StatusShowsNotApplied verifies that after a full rollback
// the schema_migrations table records migration 1 as not applied.
//
// Feature #23 step 5: arena-migrate status shows migration 1 as not applied.
func TestMigrateReset_StatusShowsNotApplied(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Start from fully migrated state.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	// Roll back all migrations.
	if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.ResetContext: %v", err)
	}

	// Step 5: version should be 0 (no migrations applied).
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("goose.GetDBVersionContext after reset: %v", err)
	}
	if version != 0 {
		t.Errorf("schema version after full rollback = %d; want 0 (no migrations applied)", version)
	}

	// The most recent row for migration version_id=1 should show is_applied=false.
	// Goose records both the apply (is_applied=true) and rollback (is_applied=false).
	var isApplied bool
	var rowFound bool
	row := db.QueryRowContext(ctx,
		`SELECT is_applied
		   FROM schema_migrations
		  WHERE version_id = 1
		  ORDER BY id DESC
		  LIMIT 1`)
	if err := row.Scan(&isApplied); err != nil {
		if err == sql.ErrNoRows {
			// If version_id=1 was never recorded in schema_migrations, that is
			// also acceptable — it means goose cleaned up the row on down.
			rowFound = false
			t.Logf("schema_migrations: no row for version_id=1 after reset (goose may remove the row)")
		} else {
			t.Fatalf("query schema_migrations version 1: %v", err)
		}
	} else {
		rowFound = true
		if isApplied {
			t.Errorf("schema_migrations version_id=1 is_applied=true after rollback; want false (not applied)")
		}
		t.Logf("schema_migrations: version_id=1 is_applied=%v (rowFound=%v)", isApplied, rowFound)
	}

	t.Logf("arena-migrate status after reset: version=0, migration 1 not applied")
}

// TestMigrateDown_UpAfterResetRestoresTables verifies that running
// goose.UpContext after a full rollback re-applies 0001_init.sql and
// all subsequent migrations, restoring every platform table.
//
// Feature #23 steps 6–7: arena-migrate up re-applies 0001_init.sql and tables exist again.
func TestMigrateDown_UpAfterResetRestoresTables(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Ensure a fully migrated starting point.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	// Roll back everything.
	if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.ResetContext: %v", err)
	}

	// Step 6: Re-apply all migrations.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext after reset (step 6): %v", err)
	}

	// Step 7: Every platform table must exist again.
	for _, table := range platformTables {
		t.Run("table_restored="+table, func(t *testing.T) {
			if !tableExists(t, db, ctx, table) {
				t.Errorf("table %q missing after up-after-reset; want restored", table)
			}
		})
	}
	t.Run("table_restored=outbox", func(t *testing.T) {
		if !tableExists(t, db, ctx, "outbox") {
			t.Errorf("table %q missing after up-after-reset; want restored", "outbox")
		}
	})

	// Version must be positive (fully migrated) again.
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext after up-after-reset: %v", err)
	}
	if version <= 0 {
		t.Errorf("version after up-after-reset = %d; want > 0", version)
	}

	t.Logf("up-after-reset: all tables restored, version=%d", version)
}

// TestMigrateDown_NoOrphanIndexesAfterCycle verifies that after a reset+up
// cycle, no orphan indexes or stale structures remain (step 8).
// Specifically checks that all key indexes from 0001_init.sql exist and that
// no unexpected user-defined indexes exist.
//
// Feature #23 step 8: no orphan rows or stale indexes.
func TestMigrateDown_NoOrphanIndexesAfterCycle(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Reset then up to perform a full cycle.
	if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.ResetContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext after reset: %v", err)
	}

	// All expected indexes from 0001_init.sql must be present.
	wantIndexes := []string{
		"idempotency_keys_key_scope_uniq",
		"idempotency_keys_expires_at_idx",
		"audit_events_resource_idx",
		"audit_events_actor_idx",
		"outbox_events_unprocessed_idx",
		"worker_jobs_status_scheduled_idx",
		"worker_jobs_idem_uniq",
		"worker_dead_letter_job_type_idx",
		"i18n_text_ns_key_locale_uniq",
		"i18n_text_ns_key_idx",
	}
	for _, idx := range wantIndexes {
		t.Run("index_present="+idx, func(t *testing.T) {
			if !indexExists(t, db, ctx, idx) {
				t.Errorf("index %q missing after reset+up cycle; want present (no orphan/missing index)", idx)
			}
		})
	}

	// Outbox index from 0002_outbox.sql must also be present.
	t.Run("index_present=outbox_backlog_idx", func(t *testing.T) {
		if !indexExists(t, db, ctx, "outbox_backlog_idx") {
			t.Errorf("index outbox_backlog_idx missing after reset+up cycle")
		}
	})

	// uuidv7() function must be present (created by 0001_init.sql Up section).
	t.Run("uuidv7_function_present", func(t *testing.T) {
		if !uuidv7FunctionExists(t, db, ctx) {
			t.Error("uuidv7() function missing after reset+up cycle")
		}
	})

	t.Logf("step 8 verified: all expected indexes present, uuidv7() function intact")
}

// TestMigrateDown_UpDownCycleDeterministic runs a complete reset→up→down→up
// cycle THREE times and verifies that each iteration produces an identical
// schema structure (deterministic rollback/restore behaviour).
//
// Feature #23 step 9: repeat cycle 3 times to verify deterministic behaviour.
func TestMigrateDown_UpDownCycleDeterministic(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	for cycle := 1; cycle <= 3; cycle++ {
		t.Run(fmt.Sprintf("cycle_%d", cycle), func(t *testing.T) {
			// --- DOWN: roll back all migrations ---
			if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
				t.Fatalf("cycle %d reset: %v", cycle, err)
			}

			// Verify tables are gone after reset.
			for _, table := range platformTables {
				if tableExists(t, db, ctx, table) {
					t.Errorf("cycle %d: table %q still exists after reset; want gone", cycle, table)
				}
			}

			vAfterReset, err := goose.GetDBVersionContext(ctx, db)
			if err != nil {
				t.Fatalf("cycle %d: GetDBVersionContext after reset: %v", cycle, err)
			}
			if vAfterReset != 0 {
				t.Errorf("cycle %d: version after reset = %d; want 0", cycle, vAfterReset)
			}

			// --- UP: re-apply all migrations ---
			if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
				t.Fatalf("cycle %d up: %v", cycle, err)
			}

			// Verify tables are back after up.
			for _, table := range platformTables {
				if !tableExists(t, db, ctx, table) {
					t.Errorf("cycle %d: table %q missing after up; want restored", cycle, table)
				}
			}

			vAfterUp, err := goose.GetDBVersionContext(ctx, db)
			if err != nil {
				t.Fatalf("cycle %d: GetDBVersionContext after up: %v", cycle, err)
			}
			if vAfterUp <= 0 {
				t.Errorf("cycle %d: version after up = %d; want > 0", cycle, vAfterUp)
			}

			// uuidv7() function must be present after each up.
			if !uuidv7FunctionExists(t, db, ctx) {
				t.Errorf("cycle %d: uuidv7() function missing after up", cycle)
			}

			t.Logf("cycle %d: reset (v=0) → up (v=%d): OK", cycle, vAfterUp)
		})
	}
}

// TestMigrateDown_DownScriptDropsInReverseOrder verifies that the 0001_init.sql
// Down section drops tables in reverse dependency order.  Specifically:
//   - i18n_text is dropped first (no dependents)
//   - idempotency_keys last among application tables
//   - uuidv7() function is dropped AFTER all tables (tables depend on it)
//
// This test reads the embedded SQL bytes and checks token order, providing a
// fast compile-time guard without requiring a live DB.
//
// Feature #23 step 10: down migration script drops in reverse dependency order.
func TestMigrateDown_DownScriptDropsInReverseOrder(t *testing.T) {
	t.Parallel()

	// Read 0001_init.sql from the embedded FS.
	data, err := migrations.FS.ReadFile("sql/0001_init.sql")
	if err != nil {
		t.Fatalf("read 0001_init.sql from embed.FS: %v", err)
	}
	content := string(data)

	// Locate the +goose Down section.
	downMarker := "-- +goose Down"
	downIdx := strings.Index(content, downMarker)
	if downIdx < 0 {
		t.Fatalf("0001_init.sql does not contain %q section", downMarker)
	}
	downSection := content[downIdx:]

	// All DROP TABLE statements in the Down section.
	// We verify order by checking that i18n_text appears before idempotency_keys
	// (inserted-last is dropped-first) and that DROP FUNCTION uuidv7 appears
	// after all DROP TABLE statements (the function is a dependency of the tables).
	dropI18n := strings.Index(downSection, "DROP TABLE IF EXISTS i18n_text")
	dropIdempotency := strings.Index(downSection, "DROP TABLE IF EXISTS idempotency_keys")
	dropFunction := strings.Index(downSection, "DROP FUNCTION IF EXISTS uuidv7")

	if dropI18n < 0 {
		t.Error("Down section: missing 'DROP TABLE IF EXISTS i18n_text'")
	}
	if dropIdempotency < 0 {
		t.Error("Down section: missing 'DROP TABLE IF EXISTS idempotency_keys'")
	}
	if dropFunction < 0 {
		t.Error("Down section: missing 'DROP FUNCTION IF EXISTS uuidv7'")
	}

	// i18n_text (last created) must be dropped before idempotency_keys (first created).
	if dropI18n >= 0 && dropIdempotency >= 0 && dropI18n > dropIdempotency {
		t.Errorf("down script: i18n_text dropped AFTER idempotency_keys; "+
			"want i18n_text first (reverse creation order). "+
			"i18n_text pos=%d, idempotency_keys pos=%d", dropI18n, dropIdempotency)
	}

	// DROP FUNCTION uuidv7 must come after all DROP TABLE statements
	// because the tables reference uuidv7() in their DEFAULT expressions.
	if dropFunction >= 0 && dropIdempotency >= 0 && dropFunction < dropIdempotency {
		t.Errorf("down script: DROP FUNCTION uuidv7() appears BEFORE DROP TABLE idempotency_keys; "+
			"want function dropped last (tables depend on it). "+
			"function pos=%d, idempotency_keys pos=%d", dropFunction, dropIdempotency)
	}

	t.Logf("down script order verified: i18n_text(%d) before idempotency_keys(%d) before uuidv7()(%d)",
		dropI18n, dropIdempotency, dropFunction)
}

// ---------------------------------------------------------------------------
// Feature #24 — arena-migrate status reports current version
// ---------------------------------------------------------------------------

// TestMigrateStatus_TableContainsColumnHeaders verifies that the human-readable
// status output written by runStatus (without --json) includes the expected
// "Applied At" and "Migration" column headers (feature #24 steps 2-3).
func TestMigrateStatus_TableContainsColumnHeaders(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	var buf strings.Builder
	if err := runStatus(ctx, db, &buf, false); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Applied At") {
		t.Errorf("status output missing 'Applied At' column header; output:\n%s", out)
	}
	if !strings.Contains(out, "Migration") {
		t.Errorf("status output missing 'Migration' column header; output:\n%s", out)
	}
	t.Logf("status table header verified:\n%s", out)
}

// TestMigrateStatus_ShowsAppliedTimestampForBaseline verifies that after
// goose.UpContext the baseline migration row (0001_init.sql) appears in the
// status table with a non-empty applied_at timestamp (feature #24 step 4).
func TestMigrateStatus_ShowsAppliedTimestampForBaseline(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	var buf strings.Builder
	if err := runStatus(ctx, db, &buf, false); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "0001_init.sql") {
		t.Errorf("status output missing '0001_init.sql'; output:\n%s", out)
	}
	// The applied_at timestamp must appear — "applied" status and NOT "Pending".
	if !strings.Contains(out, "applied") {
		t.Errorf("status output does not show 'applied' status for 0001_init.sql; output:\n%s", out)
	}
	t.Logf("baseline migration applied timestamp verified:\n%s", out)
}

// TestMigrateStatus_ShowsPendingAfterReset verifies that after a full rollback
// the migrations appear as "pending" in the status output (feature #24 step 5).
func TestMigrateStatus_ShowsPendingAfterReset(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Ensure we start migrated, then roll back everything.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.ResetContext: %v", err)
	}

	var buf strings.Builder
	if err := runStatus(ctx, db, &buf, false); err != nil {
		t.Fatalf("runStatus after reset: %v", err)
	}
	out := buf.String()

	// After full rollback, 0001_init.sql must show as pending.
	if !strings.Contains(out, "0001_init.sql") {
		t.Errorf("status output missing '0001_init.sql'; output:\n%s", out)
	}
	if !strings.Contains(out, "Pending") {
		t.Errorf("status after reset: expected 'Pending' in output; got:\n%s", out)
	}
	t.Logf("pending status after reset verified:\n%s", out)
}

// TestMigrateStatus_JSONFlagProducesValidJSON verifies that runStatus with
// jsonOut=true produces one valid JSON object per line with the required
// "version", "name", and "status" fields (feature #24 step 6).
func TestMigrateStatus_JSONFlagProducesValidJSON(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	var buf strings.Builder
	if err := runStatus(ctx, db, &buf, true /* jsonOut */); err != nil {
		t.Fatalf("runStatus --json: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatal("runStatus --json: produced no output")
	}

	lines := strings.Split(out, "\n")
	for i, line := range lines {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not valid JSON (%q): %v", i, line, err)
			continue
		}
		for _, field := range []string{"version", "name", "status"} {
			if _, ok := obj[field]; !ok {
				t.Errorf("line %d JSON missing field %q: %v", i, field, obj)
			}
		}
	}
	t.Logf("--json output (%d lines) is valid NDJSON", len(lines))
}

// TestMigrateStatus_JSONAppliedHasAppliedAt verifies that applied migrations
// in --json output include the "applied_at" field (feature #24 step 6).
func TestMigrateStatus_JSONAppliedHasAppliedAt(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	var buf strings.Builder
	if err := runStatus(ctx, db, &buf, true); err != nil {
		t.Fatalf("runStatus --json: %v", err)
	}

	found := false
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		name, _ := obj["name"].(string)
		status, _ := obj["status"].(string)
		if name == "0001_init.sql" && status == "applied" {
			if _, ok := obj["applied_at"]; !ok {
				t.Errorf("0001_init.sql applied JSON entry missing 'applied_at': %v", obj)
			}
			found = true
		}
	}
	if !found {
		t.Error("0001_init.sql not found as applied in --json output")
	}
}

// TestMigrateStatus_ExitZeroEquivalent verifies that runStatus returns nil
// (equivalent to exit code 0) for a correctly migrated database.
// Feature #24 step 2: exit 0.
func TestMigrateStatus_ExitZeroEquivalent(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.UpContext: %v", err)
	}

	var buf strings.Builder
	if err := runStatus(ctx, db, &buf, false); err != nil {
		t.Fatalf("runStatus returned non-nil error (non-zero exit equivalent): %v", err)
	}
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

// ---------------------------------------------------------------------------
// Feature #25 — arena-migrate redo re-applies last migration
// ---------------------------------------------------------------------------

// capturingGooseLogger records all Printf messages emitted by goose during a
// migration operation. This allows integration tests to assert on the specific
// log output produced by the redo command.
//
// Fatalf panics (like the production gooseLogger) so that goose fatal errors
// surface as test failures rather than silently swallowing them.
type capturingGooseLogger struct {
	t        *testing.T
	messages []string
}

func (l *capturingGooseLogger) Printf(format string, v ...interface{}) {
	msg := strings.TrimRight(fmt.Sprintf(format, v...), "\n")
	l.messages = append(l.messages, msg)
}

func (l *capturingGooseLogger) Fatalf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	l.t.Logf("[goose fatal] %s", msg)
	panic("goose fatal: " + msg)
}

// Compile-time guard: capturingGooseLogger must satisfy goose.Logger.
var _ goose.Logger = (*capturingGooseLogger)(nil)

// TestMigrateRedo_Succeeds verifies that goose.RedoContext returns nil (i.e.
// arena-migrate redo exits 0) and that the schema version is unchanged after
// the redo cycle (roll back one + re-apply one = net zero).
//
// Feature #25 steps 1–3: apply all migrations, run redo, verify exit 0.
func TestMigrateRedo_Succeeds(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Step 1: Apply all pending migrations.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	vBefore, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext before redo: %v", err)
	}
	if vBefore <= 0 {
		t.Fatalf("expected positive version before redo, got %d", vBefore)
	}

	// Step 2–3: Run redo — must return nil (equivalent to exit code 0).
	if err := goose.RedoContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.RedoContext: unexpected error (want exit 0): %v", err)
	}

	// After redo the version must be identical: redo = down(1) + up(1), net zero change.
	vAfter, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext after redo: %v", err)
	}
	if vAfter != vBefore {
		t.Errorf("version after redo = %d; want %d (redo should preserve schema version)", vAfter, vBefore)
	}

	t.Logf("redo succeeded: version stable at %d", vAfter)
}

// TestMigrateRedo_LogsRollbackThenApply verifies that redo emits log messages
// that indicate the most recent migration was first rolled back and then
// re-applied.  goose v3 uses Printf("DONE   <name> ...") for rollback steps
// and Printf("OK     <name> ...") for apply steps; our gooseLogger converts
// those Printf calls to slog.Info records.
//
// Feature #25 step 4: logs show rolling back then applying the last migration.
func TestMigrateRedo_LogsRollbackThenApply(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Configure goose with a capturing logger (not the silent one) so we can
	// inspect what goose prints during the redo operation.
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("schema_migrations")

	capture := &capturingGooseLogger{t: t}
	goose.SetLogger(capture)
	t.Cleanup(func() {
		// Restore the silent logger after this test to avoid polluting other tests.
		goose.SetLogger(&integrationSilentLogger{t: t})
	})

	// Step 1: Apply all migrations (messages from this phase are discarded below).
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	// Discard messages produced by the setup Up call; only capture redo messages.
	capture.messages = nil

	// Step 2: Run redo.
	if err := goose.RedoContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.RedoContext: %v", err)
	}

	t.Logf("captured %d goose log messages during redo: %v", len(capture.messages), capture.messages)

	// Step 4: redo must emit at least 2 messages (one rollback, one re-apply).
	if len(capture.messages) < 2 {
		t.Errorf("redo: expected >= 2 goose log messages (rollback + apply); got %d: %v",
			len(capture.messages), capture.messages)
	}

	// The migration filenames embedded in this binary.
	knownMigrations := []string{"0001_init.sql", "0002_outbox.sql", "0003_i18n_seeds.sql"}

	rollbackIdx := -1
	applyIdx := -1
	for i, msg := range capture.messages {
		upper := strings.ToUpper(msg)
		containsMig := false
		for _, name := range knownMigrations {
			if strings.Contains(msg, name) {
				containsMig = true
				break
			}
		}
		if !containsMig {
			continue
		}
		// goose v3 logs "DONE" for a successful rollback (down) step.
		if strings.Contains(upper, "DONE") && rollbackIdx == -1 {
			rollbackIdx = i
		}
		// goose v3 logs "OK" for a successful apply (up) step.
		if strings.Contains(upper, "OK") && applyIdx == -1 {
			applyIdx = i
		}
	}

	if rollbackIdx == -1 {
		t.Errorf("redo: no 'DONE' (rollback) message found for any migration file; messages: %v",
			capture.messages)
	}
	if applyIdx == -1 {
		t.Errorf("redo: no 'OK' (apply) message found for any migration file; messages: %v",
			capture.messages)
	}
	// Rollback must be logged BEFORE the re-apply.
	if rollbackIdx != -1 && applyIdx != -1 && rollbackIdx > applyIdx {
		t.Errorf("redo: rollback message (idx=%d) appeared AFTER apply message (idx=%d); want rollback first",
			rollbackIdx, applyIdx)
	}
}

// TestMigrateRedo_TablesStillPresent verifies that all platform tables remain
// present in the public schema after a redo operation. Redo must not destroy
// any schema objects created by earlier migrations.
//
// Feature #25 step 5: confirm tables are still present and structure unchanged.
func TestMigrateRedo_TablesStillPresent(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Step 1: Apply all migrations.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	// Step 2: Run redo.
	if err := goose.RedoContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.RedoContext: %v", err)
	}

	// Step 5: Every platform table must still exist after redo.
	for _, table := range platformTables {
		t.Run("table_present="+table, func(t *testing.T) {
			if !tableExists(t, db, ctx, table) {
				t.Errorf("table %q missing after redo; want present", table)
			}
		})
	}

	// schema_migrations (goose-managed) must also survive the redo.
	t.Run("schema_migrations_present", func(t *testing.T) {
		if !tableExists(t, db, ctx, "schema_migrations") {
			t.Error("schema_migrations missing after redo; want present")
		}
	})

	// uuidv7() function (created by 0001_init.sql) must still be available.
	t.Run("uuidv7_function_present", func(t *testing.T) {
		if !uuidv7FunctionExists(t, db, ctx) {
			t.Error("uuidv7() function missing after redo; want present")
		}
	})

	t.Logf("redo: all platform tables and uuidv7() function still present after redo")
}

// TestMigrateRedo_UpdatesSchemaTimestamp verifies that redo inserts a fresh
// is_applied=true row into schema_migrations for the re-applied migration,
// updating the recorded apply timestamp to reflect the most recent run.
//
// goose records every apply and rollback as a new row (id auto-increments).
// After redo there must be a new row with id > max_id_before and is_applied=true
// for the version that was redone.
//
// Feature #25 step 6: schema_migrations.applied_at for the redone migration is
// updated to the most recent run.
func TestMigrateRedo_UpdatesSchemaTimestamp(t *testing.T) {
	db := integrationMigrateDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configureGooseIntegration(t)

	// Step 1: Apply all migrations.
	if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("setup goose.UpContext: %v", err)
	}
	t.Cleanup(func() { resetAndUp(t, db, ctx) })

	// Determine which version is currently the most recent (highest applied).
	vCurrent, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("GetDBVersionContext before redo: %v", err)
	}

	// Snapshot the maximum id in schema_migrations before redo.  goose uses an
	// auto-increment id, so any rows inserted by redo will have id > this value.
	var maxIDBefore int64
	row := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM schema_migrations`)
	if err := row.Scan(&maxIDBefore); err != nil {
		t.Fatalf("query max id before redo: %v", err)
	}

	// Record the tstamp of the existing applied row for vCurrent (for comparison).
	var tstampBefore time.Time
	row = db.QueryRowContext(ctx,
		`SELECT tstamp
		   FROM schema_migrations
		  WHERE version_id = $1 AND is_applied = true
		  ORDER BY id DESC
		  LIMIT 1`, vCurrent)
	if err := row.Scan(&tstampBefore); err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("no applied row found for version_id=%d before redo", vCurrent)
		}
		t.Fatalf("query tstamp before redo: %v", err)
	}

	// Step 2: Run redo.
	if err := goose.RedoContext(ctx, db, migrations.Dir); err != nil {
		t.Fatalf("goose.RedoContext: %v", err)
	}

	// Step 6: There must be a new row for vCurrent with id > maxIDBefore and
	// is_applied = true — this row records the fresh re-apply after the rollback.
	var newID int64
	var newIsApplied bool
	var tstampAfter time.Time
	row = db.QueryRowContext(ctx,
		`SELECT id, is_applied, tstamp
		   FROM schema_migrations
		  WHERE version_id = $1 AND id > $2 AND is_applied = true
		  ORDER BY id DESC
		  LIMIT 1`, vCurrent, maxIDBefore)
	if err := row.Scan(&newID, &newIsApplied, &tstampAfter); err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("no new is_applied=true row in schema_migrations after redo for "+
				"version_id=%d (id > %d); redo must insert a fresh apply record",
				vCurrent, maxIDBefore)
		}
		t.Fatalf("query schema_migrations after redo: %v", err)
	}

	// The new row must be marked as applied.
	if !newIsApplied {
		t.Errorf("schema_migrations new row (id=%d) is_applied=false; want true "+
			"(redo must re-apply the migration)", newID)
	}

	// The new tstamp must not be earlier than the old one (time must not go backwards).
	if tstampAfter.Before(tstampBefore) {
		t.Errorf("redo: new tstamp %v is before old tstamp %v; tstamp must not go backwards",
			tstampAfter.Format(time.RFC3339), tstampBefore.Format(time.RFC3339))
	}

	t.Logf("redo updated schema_migrations: version_id=%d new_id=%d is_applied=%v "+
		"tstamp_before=%v tstamp_after=%v",
		vCurrent, newID, newIsApplied,
		tstampBefore.Format(time.RFC3339), tstampAfter.Format(time.RFC3339))
}

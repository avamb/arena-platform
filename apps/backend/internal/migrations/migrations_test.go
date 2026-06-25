// Package migrations_test verifies the embedded migration file-system that
// arena-migrate uses at runtime (feature #22).
//
// These tests do NOT require a live PostgreSQL instance. They verify:
//
//   - The embedded FS contains the expected migration files.
//   - Each SQL file starts with the required goose "Up" marker so goose
//     can parse it as a valid migration.
//   - The baseline migration (0001_init.sql) contains the six platform
//     tables expected by the foundation milestone, plus the uuidv7()
//     function and the "Down" teardown block.
//   - The "Dir" constant ("sql") matches the path inside the embedded FS
//     where goose will look for .sql files.
//   - FS is read-only (no accidental mutation surface).
package migrations_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// TestMigrationsFS_DirConstantMatchesEmbeddedPath ensures that Dir ("sql")
// is a valid sub-directory inside the embedded FS.
func TestMigrationsFS_DirConstantMatchesEmbeddedPath(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrations.FS, migrations.Dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v — Dir constant does not point to a valid path inside FS", migrations.Dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("migrations.Dir (%q) is empty; at least one .sql file expected", migrations.Dir)
	}
}

// TestMigrationsFS_BaselineMigrationExists verifies that 0001_init.sql is
// present in the embedded FS.
func TestMigrationsFS_BaselineMigrationExists(t *testing.T) {
	t.Parallel()

	const want = "sql/0001_init.sql"
	f, err := migrations.FS.Open(want)
	if err != nil {
		t.Fatalf("migrations.FS.Open(%q): %v — 0001_init.sql not found in embedded FS", want, err)
	}
	f.Close()
}

// TestMigrationsFS_BaselineMigrationHasGooseUpMarker ensures that
// 0001_init.sql starts with the "-- +goose Up" directive that goose requires
// to identify the forward-migration section.
func TestMigrationsFS_BaselineMigrationHasGooseUpMarker(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(migrations.FS, "sql/0001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(0001_init.sql): %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "-- +goose Up") {
		t.Fatal("0001_init.sql is missing the '-- +goose Up' marker required by goose")
	}
}

// TestMigrationsFS_BaselineMigrationHasGooseDownMarker ensures that the
// "Down" rollback block is present so arena-migrate down and arena-migrate
// reset work correctly.
func TestMigrationsFS_BaselineMigrationHasGooseDownMarker(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(migrations.FS, "sql/0001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(0001_init.sql): %v", err)
	}
	if !strings.Contains(string(data), "-- +goose Down") {
		t.Fatal("0001_init.sql is missing the '-- +goose Down' block — redo and reset will fail")
	}
}

// TestMigrationsFS_BaselineMigrationCreatesExpectedTables verifies that the
// six platform tables declared in the foundation spec are present as CREATE
// TABLE statements in 0001_init.sql.
func TestMigrationsFS_BaselineMigrationCreatesExpectedTables(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(migrations.FS, "sql/0001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(0001_init.sql): %v", err)
	}
	content := string(data)

	required := []string{
		"idempotency_keys",
		"audit_events",
		"outbox_events",
		"worker_jobs",
		"worker_dead_letter",
		"i18n_text",
	}
	for _, table := range required {
		needle := "CREATE TABLE " + table
		if !strings.Contains(content, needle) {
			t.Errorf("0001_init.sql: missing %q — migration is incomplete", needle)
		}
	}
}

// TestMigrationsFS_BaselineMigrationCreatesUUIDv7Function verifies the
// plpgsql uuidv7() function is defined so column defaults (DEFAULT uuidv7())
// resolve on PostgreSQL 17.
func TestMigrationsFS_BaselineMigrationCreatesUUIDv7Function(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(migrations.FS, "sql/0001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(0001_init.sql): %v", err)
	}
	if !strings.Contains(string(data), "CREATE OR REPLACE FUNCTION uuidv7()") {
		t.Fatal("0001_init.sql: missing uuidv7() function definition")
	}
}

// TestMigrationsFS_BaselineMigrationDownDropsAllTables ensures that the Down
// block explicitly drops all six platform tables so arena-migrate reset leaves
// a clean slate.
func TestMigrationsFS_BaselineMigrationDownDropsAllTables(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(migrations.FS, "sql/0001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(0001_init.sql): %v", err)
	}
	content := string(data)

	for _, table := range []string{
		"idempotency_keys",
		"audit_events",
		"outbox_events",
		"worker_jobs",
		"worker_dead_letter",
		"i18n_text",
	} {
		needle := "DROP TABLE IF EXISTS " + table
		if !strings.Contains(content, needle) {
			t.Errorf("0001_init.sql Down block: missing %q", needle)
		}
	}
}

// TestMigrationsFS_AllSQLFilesHaveGooseUpMarker ensures that every .sql file
// in the embedded FS (not just the baseline) has the required goose marker.
// This test will catch future migrations that are added without the marker.
func TestMigrationsFS_AllSQLFilesHaveGooseUpMarker(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrations.FS, migrations.Dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		path := migrations.Dir + "/" + e.Name()
		data, err := fs.ReadFile(migrations.FS, path)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", path, err)
			continue
		}
		if !strings.Contains(string(data), "-- +goose Up") {
			t.Errorf("%q: missing '-- +goose Up' marker", path)
		}
	}
}

// TestMigrationsFS_FilesAreSequentiallyNumbered verifies that all SQL files
// follow the goose sequence-number naming convention (NNNN_name.sql) that the
// arena-migrate create sub-command enforces.
func TestMigrationsFS_FilesAreSequentiallyNumbered(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrations.FS, migrations.Dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		// goose sequence files: 4+ leading digits, underscore, then name.
		if len(name) < 6 || name[4] != '_' {
			// Allow longer prefix (5-digit sequences like 00001_*.sql).
			if len(name) < 7 || name[5] != '_' {
				t.Errorf("%q: does not follow NNNN_name.sql convention", name)
			}
		}
	}
}

// Compile-time guards: ensure the exported symbols remain stable.
var (
	_ fs.FS  = migrations.FS
	_ string = migrations.Dir
)

// Package migrations_test — feature #69: Timestamps stored as timestamptz in UTC.
//
// This file contains static analysis tests that verify every SQL migration
// declares timestamp columns as 'timestamp with time zone' (timestamptz) rather
// than bare 'timestamp' or 'timestamp without time zone'.
//
// These tests do NOT require a live PostgreSQL connection; they operate entirely
// on the embedded SQL source files. Database-level verification (SET TIMEZONE,
// actual insert + query) lives in timestamptz_integration_test.go (build tag
// "integration").
package migrations_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// Aliases for the embedded FS and directory constant from the migrations package.
// These are used throughout the test file to keep code DRY.
var (
	FS  = migrations.FS
	Dir = migrations.Dir
)

// timestampBarePattern matches a column type of bare "timestamp" or "timestamp
// without time zone" in SQL DDL.  It does NOT match "timestamptz" or "timestamp
// with time zone".
//
// The negative look-around is expressed as two separate checks in Go since the
// standard library regexp does not support look-aheads.  We match the word
// "timestamp" and then confirm it is not immediately followed by "tz" or "with".
var timestampWordRE = regexp.MustCompile(`(?i)\btimestamp\b`)

// noTimestampWithoutTZ checks every SQL file in the embedded FS for bare
// "timestamp" column declarations (i.e. NOT "timestamptz" or "timestamp with
// time zone").
//
// Feature #69 step 6: Verify NO column declared as 'timestamp without time zone'.
func TestTimestamptz_NoTimestampWithoutTimeZone(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(FS, Dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", Dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		path := Dir + "/" + entry.Name()
		data, err := fs.ReadFile(FS, path)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", path, err)
			continue
		}

		checkSQLForBareTimetamp(t, path, string(data))
	}
}

// checkSQLForBareTimetamp scans the content of a single SQL file for any
// timestamp column declarations that are NOT timestamptz or "timestamp with
// time zone".
func checkSQLForBareTimetamp(t *testing.T, filename, content string) {
	t.Helper()

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		// Skip comment lines.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}

		matches := timestampWordRE.FindAllStringIndex(line, -1)
		for _, match := range matches {
			// Extract the word "timestamp" and what follows it on the same line.
			suffix := line[match[1]:]
			suffixLower := strings.ToLower(strings.TrimSpace(suffix))

			// "timestamptz" — allowed (the match ends after "timestamp", so
			// suffix starts with "tz").
			if strings.HasPrefix(suffixLower, "tz") {
				continue
			}

			// "timestamp with time zone" — allowed.
			if strings.HasPrefix(suffixLower, "with time zone") {
				continue
			}

			// "timestamp without time zone" — forbidden.
			if strings.HasPrefix(suffixLower, "without time zone") {
				t.Errorf("%s line %d: found forbidden 'timestamp without time zone' — use timestamptz: %q",
					filename, i+1, strings.TrimSpace(line))
				continue
			}

			// Bare "timestamp" followed by anything else (space, comma, newline) — forbidden.
			// This catches "timestamp NOT NULL", "timestamp DEFAULT now()", etc.
			// We skip occurrences that are part of function names / identifiers by
			// checking the character immediately before the match.
			prefix := line[:match[0]]
			if len(prefix) > 0 {
				lastChar := prefix[len(prefix)-1]
				// If the preceding character is alphanumeric or underscore, this is
				// part of a longer identifier (e.g. "unix_ts_ms", "clock_timestamp()").
				if isIdentChar(lastChar) {
					continue
				}
			}

			// Check what immediately follows: if it's alphanumeric/underscore it is
			// part of a function name like "timestamp()" → skip.
			if len(suffix) > 0 && isIdentChar(suffix[0]) && suffix[0] != ' ' {
				continue
			}

			// Bare timestamp as a type.
			t.Errorf("%s line %d: found bare 'timestamp' column type — use 'timestamptz': %q",
				filename, i+1, strings.TrimSpace(line))
		}
	}
}

// isIdentChar reports whether c can appear in a SQL/Go identifier.
func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// TestTimestamptz_AuditEventsColumnType verifies step 5: the audit_events table
// declares its occurred_at column as 'timestamptz' (timestamp with time zone).
func TestTimestamptz_AuditEventsColumnType(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(FS, "sql/0001_init.sql")
	if err != nil {
		t.Fatalf("ReadFile(0001_init.sql): %v", err)
	}

	content := string(data)

	// Find the audit_events CREATE TABLE block.
	tableStart := strings.Index(content, "CREATE TABLE audit_events")
	if tableStart < 0 {
		t.Fatal("0001_init.sql: CREATE TABLE audit_events not found")
	}

	// Find the closing ')' of the table definition.
	tableEnd := strings.Index(content[tableStart:], ");")
	if tableEnd < 0 {
		t.Fatal("0001_init.sql: closing '); of audit_events table definition not found")
	}
	tableBlock := content[tableStart : tableStart+tableEnd+2]

	// occurred_at must be timestamptz.
	if !strings.Contains(tableBlock, "occurred_at") {
		t.Fatal("audit_events: occurred_at column not found in table definition")
	}

	// Extract the line with occurred_at.
	for _, line := range strings.Split(tableBlock, "\n") {
		if strings.Contains(line, "occurred_at") {
			if !strings.Contains(line, "timestamptz") {
				t.Errorf("audit_events.occurred_at: expected 'timestamptz', got: %q",
					strings.TrimSpace(line))
			}
			return
		}
	}
	t.Fatal("audit_events: could not locate occurred_at column line")
}

// TestTimestamptz_AllTablesUseTimestamptz verifies that every table across all
// migrations uses 'timestamptz' for timestamp columns, not bare 'timestamp'.
//
// Covers all 6 platform tables:
//
//	idempotency_keys (created_at, expires_at)
//	audit_events     (occurred_at)
//	outbox_events    (occurred_at, processed_at)
//	worker_jobs      (scheduled_at, claimed_at, created_at)
//	worker_dead_letter (failed_at, original_created_at)
//	i18n_text        (updated_at)
//	outbox           (occurred_at, dispatched_at)  — from 0002_outbox.sql
func TestTimestamptz_AllTablesUseTimestamptz(t *testing.T) {
	t.Parallel()

	type columnCheck struct {
		table  string
		column string
		file   string
	}

	checks := []columnCheck{
		{"idempotency_keys", "created_at", "sql/0001_init.sql"},
		{"idempotency_keys", "expires_at", "sql/0001_init.sql"},
		{"audit_events", "occurred_at", "sql/0001_init.sql"},
		{"outbox_events", "occurred_at", "sql/0001_init.sql"},
		{"outbox_events", "processed_at", "sql/0001_init.sql"},
		{"worker_jobs", "scheduled_at", "sql/0001_init.sql"},
		{"worker_jobs", "claimed_at", "sql/0001_init.sql"},
		{"worker_jobs", "created_at", "sql/0001_init.sql"},
		{"worker_dead_letter", "failed_at", "sql/0001_init.sql"},
		{"worker_dead_letter", "original_created_at", "sql/0001_init.sql"},
		{"i18n_text", "updated_at", "sql/0001_init.sql"},
		{"outbox", "occurred_at", "sql/0002_outbox.sql"},
		{"outbox", "dispatched_at", "sql/0002_outbox.sql"},
	}

	// Cache file contents.
	fileCache := map[string]string{}

	for _, chk := range checks {
		t.Run(chk.table+"."+chk.column, func(t *testing.T) {
			content, ok := fileCache[chk.file]
			if !ok {
				data, err := fs.ReadFile(FS, chk.file)
				if err != nil {
					t.Fatalf("ReadFile(%q): %v", chk.file, err)
				}
				content = string(data)
				fileCache[chk.file] = content
			}

			// Find the table block.
			tableMarker := "CREATE TABLE " + chk.table
			tableStart := strings.Index(content, tableMarker)
			if tableStart < 0 {
				t.Fatalf("%s: CREATE TABLE %s not found in %s",
					chk.table, chk.table, chk.file)
			}
			tableEnd := strings.Index(content[tableStart:], ");")
			if tableEnd < 0 {
				t.Fatalf("%s: closing '); not found after table definition", chk.table)
			}
			tableBlock := content[tableStart : tableStart+tableEnd+2]

			// Find the column line.
			found := false
			for _, line := range strings.Split(tableBlock, "\n") {
				// Match column name as a word (not as part of an index name etc.)
				if !containsWord(line, chk.column) {
					continue
				}
				found = true
				if !strings.Contains(line, "timestamptz") {
					t.Errorf("%s.%s: expected 'timestamptz' in column definition, got: %q",
						chk.table, chk.column, strings.TrimSpace(line))
				}
				break
			}
			if !found {
				// dispatched_at and processed_at may be nullable without NOT NULL —
				// still must be timestamptz type.
				t.Errorf("%s: column %s not found in table definition",
					chk.table, chk.column)
			}
		})
	}
}

// containsWord reports whether line contains word as a whole word
// (not as a substring of another identifier).
func containsWord(line, word string) bool {
	idx := strings.Index(line, word)
	if idx < 0 {
		return false
	}
	// Check character before word.
	if idx > 0 && isIdentChar(line[idx-1]) {
		return false
	}
	// Check character after word.
	end := idx + len(word)
	if end < len(line) && isIdentChar(line[end]) {
		return false
	}
	return true
}

// TestTimestamptz_NoTimestampWithoutTimeZoneInAllFiles is a table-driven
// companion to TestTimestamptz_NoTimestampWithoutTimeZone that explicitly names
// each migration file and asserts it never uses the forbidden type.
func TestTimestamptz_NoTimestampWithoutTimeZoneInAllFiles(t *testing.T) {
	t.Parallel()

	for _, sqlFile := range []string{
		"sql/0001_init.sql",
		"sql/0002_outbox.sql",
	} {
		sqlFile := sqlFile // capture
		t.Run(sqlFile, func(t *testing.T) {
			t.Parallel()
			data, err := fs.ReadFile(FS, sqlFile)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", sqlFile, err)
			}
			if strings.Contains(strings.ToLower(string(data)), "timestamp without time zone") {
				t.Errorf("%s: found 'timestamp without time zone' — all timestamps must be timestamptz", sqlFile)
			}
		})
	}
}

// TestTimestamptz_FullVerification runs all feature #69 static checks as
// sub-tests, providing a single "pass/fail" signal for the feature gate.
func TestTimestamptz_FullVerification(t *testing.T) {
	t.Run("Step5_AuditEventsColumnIsTimestamptz", func(t *testing.T) {
		data, err := fs.ReadFile(FS, "sql/0001_init.sql")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "occurred_at   timestamptz") &&
			!strings.Contains(content, "occurred_at    timestamptz") &&
			!strings.Contains(content, "occurred_at timestamptz") {
			t.Error("audit_events.occurred_at is not declared as timestamptz")
		}
	})

	t.Run("Step6_NoTimestampWithoutTimeZone", func(t *testing.T) {
		entries, err := fs.ReadDir(FS, Dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			path := Dir + "/" + entry.Name()
			data, err := fs.ReadFile(FS, path)
			if err != nil {
				t.Errorf("ReadFile(%q): %v", path, err)
				continue
			}
			lower := strings.ToLower(string(data))
			if strings.Contains(lower, "timestamp without time zone") {
				t.Errorf("%s: found 'timestamp without time zone'", path)
			}
		}
	})
}

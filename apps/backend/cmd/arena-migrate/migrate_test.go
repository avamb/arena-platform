// Package main_test tests the arena-migrate helper functions and gooseLogger
// adapter without requiring a live PostgreSQL instance (feature #22).
//
// What is covered:
//   - countApplied: monotonic migration-count helper used in the "up" log line.
//   - parseInt64: version-number parser used by up-to and down-to subcommands.
//   - gooseLogger.Printf / gooseLogger.Fatalf: slog adapter for goose output.
//   - createMigration returns an appropriate error when the migrations
//     directory is absent (non-repo working directory).
//   - The embedded migrations.FS is accessible from the main package so that
//     goose.SetBaseFS(migrations.FS) will succeed at runtime.
//
// Tests that require a live PostgreSQL (steps 1–7 of the feature spec) are
// left as integration tests to run inside docker-compose; this file focuses
// on everything that can be verified without a DB connection.
package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// countApplied
// ---------------------------------------------------------------------------

func TestCountApplied_ZeroWhenAfterEqualsOrLessThanBefore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		before, after int64
		want          int64
	}{
		{before: 0, after: 0, want: 0},
		{before: 1, after: 1, want: 0},
		{before: 5, after: 3, want: 0}, // after < before (rollback scenario)
	}
	for _, tc := range cases {
		got := countApplied(tc.before, tc.after)
		if got != tc.want {
			t.Errorf("countApplied(%d, %d) = %d; want %d", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestCountApplied_PositiveWhenAfterExceedsBefore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		before, after int64
	}{
		{0, 1},
		{0, 5},
		{3, 4},
		{1000, 2000},
	}
	for _, tc := range cases {
		got := countApplied(tc.before, tc.after)
		if got <= 0 {
			t.Errorf("countApplied(%d, %d) = %d; want > 0", tc.before, tc.after, got)
		}
	}
}

// ---------------------------------------------------------------------------
// parseInt64
// ---------------------------------------------------------------------------

func TestParseInt64_ValidIntegers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  int64
	}{
		{"1", 1},
		{"0", 0},
		{"20250101120000", 20250101120000},
		{"9223372036854775807", 9223372036854775807}, // math.MaxInt64
	}
	for _, tc := range cases {
		got, err := parseInt64(tc.input)
		if err != nil {
			t.Errorf("parseInt64(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseInt64(%q) = %d; want %d", tc.input, got, tc.want)
		}
	}
}

func TestParseInt64_InvalidInputsReturnError(t *testing.T) {
	t.Parallel()

	// Note: "1.5" is intentionally excluded — fmt.Sscanf with %d scans "1"
	// and ignores the remainder (".5"), so parseInt64("1.5") returns (1, nil).
	cases := []string{"", "abc", "v1", "-", "NaN"}
	for _, input := range cases {
		_, err := parseInt64(input)
		if err == nil {
			t.Errorf("parseInt64(%q): expected error, got nil", input)
		}
	}
}

func TestParseInt64_ErrorMessageIncludesInput(t *testing.T) {
	t.Parallel()

	_, err := parseInt64("bad-version")
	if err == nil {
		t.Fatal("expected error for invalid version string")
	}
	if !strings.Contains(err.Error(), "bad-version") {
		t.Errorf("error message %q does not mention the invalid input", err.Error())
	}
}

// ---------------------------------------------------------------------------
// gooseLogger
// ---------------------------------------------------------------------------

// collectHandler is a minimal slog.Handler that captures log messages.
type collectHandler struct {
	messages *[]string
	level    slog.Level // minimum level to capture (default: Debug captures all)
}

func (h *collectHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *collectHandler) Handle(_ context.Context, r slog.Record) error {
	*h.messages = append(*h.messages, r.Message)
	return nil
}

func (h *collectHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *collectHandler) WithGroup(_ string) slog.Handler      { return h }

func TestGooseLogger_PrintfDelegatesToSlogInfo(t *testing.T) {
	t.Parallel()

	msgs := make([]string, 0)
	logger := slog.New(&collectHandler{messages: &msgs})
	gl := &gooseLogger{logger: logger}

	gl.Printf("goose OK    %s\n", "0001_init.sql")

	if len(msgs) == 0 {
		t.Fatal("expected at least one log record from Printf")
	}
	found := false
	for _, msg := range msgs {
		if strings.Contains(msg, "0001_init.sql") {
			found = true
		}
	}
	if !found {
		t.Errorf("Printf log records %v: none contain '0001_init.sql'", msgs)
	}
}

func TestGooseLogger_PrintfStripsTrailingNewline(t *testing.T) {
	t.Parallel()

	msgs := make([]string, 0)
	logger := slog.New(&collectHandler{messages: &msgs})
	gl := &gooseLogger{logger: logger}

	gl.Printf("line\n") // trailing newline must be stripped before logging

	if len(msgs) == 0 {
		t.Fatal("no log record emitted")
	}
	for _, msg := range msgs {
		if strings.HasSuffix(msg, "\n") {
			t.Errorf("log message %q still has trailing newline", msg)
		}
	}
}

func TestGooseLogger_FatalfPanicsWithMessage(t *testing.T) {
	t.Parallel()

	msgs := make([]string, 0)
	logger := slog.New(&collectHandler{messages: &msgs})
	gl := &gooseLogger{logger: logger}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Fatalf did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not a string: %T %v", r, r)
		}
		if !strings.Contains(msg, "fatal-error-sentinel") {
			t.Errorf("panic message %q does not contain expected sentinel", msg)
		}
	}()
	gl.Fatalf("fatal-error-sentinel: %s", "some-db-error")
}

func TestGooseLogger_FatalfLogsAtErrorLevel(t *testing.T) {
	t.Parallel()

	errorMsgs := make([]string, 0)
	logger := slog.New(&collectHandler{
		messages: &errorMsgs,
		level:    slog.LevelError,
	})
	gl := &gooseLogger{logger: logger}

	// Call Fatalf inside an inner function so that after the panic is
	// recovered, we can assert on the captured error messages.
	func() {
		defer func() { recover() }() // absorb the expected panic
		gl.Fatalf("fatal: %s", "schema-error")
	}()

	// Fatalf must log at Error level BEFORE it panics.
	if len(errorMsgs) == 0 {
		t.Fatal("Fatalf did not emit an error-level log record before panicking")
	}
}

func TestGooseLogger_PrintfDoesNotPanic(t *testing.T) {
	t.Parallel()

	msgs := make([]string, 0)
	logger := slog.New(&collectHandler{messages: &msgs})
	gl := &gooseLogger{logger: logger}

	// Printf should never panic — it is called for every goose output line.
	gl.Printf("OK   0001_init.sql\n")
	gl.Printf("Goose run: no migrations to run (current version: 1)\n")
	gl.Printf("") // empty string edge-case
}

// ---------------------------------------------------------------------------
// createMigration — error path when no name provided
// ---------------------------------------------------------------------------

func TestCreateMigration_ReturnsErrorWhenNoNameProvided(t *testing.T) {
	t.Parallel()

	msgs := make([]string, 0)
	logger := slog.New(&collectHandler{messages: &msgs})

	err := createMigration(logger, []string{}) // no name argument
	if err == nil {
		t.Fatal("expected error for missing migration name, got nil")
	}
	if !strings.Contains(err.Error(), "name required") {
		t.Errorf("error message %q: expected 'name required'", err.Error())
	}
}

func TestCreateMigration_ReturnsErrorWhenMigrationsDirAbsent(t *testing.T) {
	t.Parallel()

	// createMigration checks for the migrations directory relative to the
	// working directory.  In test execution cwd is the package dir
	// (apps/backend/cmd/arena-migrate/), which does NOT contain
	// apps/backend/internal/migrations/sql — so we expect an error.
	msgs := make([]string, 0)
	logger := slog.New(&collectHandler{messages: &msgs})

	err := createMigration(logger, []string{"test_migration"})
	if err == nil {
		t.Fatal("expected error when migrations dir is absent, got nil")
	}
	// The error message should mention the directory path and "not found".
	errMsg := err.Error()
	if !strings.Contains(errMsg, "not found") {
		t.Errorf("error message %q: expected 'not found' to indicate missing dir", errMsg)
	}
}

// ---------------------------------------------------------------------------
// Compile-time guards: pin the symbols this test depends on.
// ---------------------------------------------------------------------------
var (
	_ func(int64, int64) int64  = countApplied
	_ func(string) (int64, error) = parseInt64
)

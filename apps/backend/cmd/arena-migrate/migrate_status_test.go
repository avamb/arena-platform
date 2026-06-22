// Package main_test — unit tests for the arena-migrate status command.
//
// These tests cover the pure formatting logic and the parseVersionFromFilename
// helper without requiring a live PostgreSQL instance.  DB-dependent behaviour
// (actual applied-at timestamps, pending rows) is verified by the integration
// tests in migrate_integration_test.go.
//
// Feature #24 — arena-migrate status reports current version.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseVersionFromFilename
// ---------------------------------------------------------------------------

func TestParseVersionFromFilename_SequenceNumbered(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want int64
	}{
		{"0001_init.sql", 1},
		{"0002_outbox.sql", 2},
		{"0003_i18n_seeds.sql", 3},
		{"0042_add_users.sql", 42},
		{"9999_test.sql", 9999},
	}
	for _, tc := range cases {
		got, err := parseVersionFromFilename(tc.name)
		if err != nil {
			t.Errorf("parseVersionFromFilename(%q): unexpected error: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseVersionFromFilename(%q) = %d; want %d", tc.name, got, tc.want)
		}
	}
}

func TestParseVersionFromFilename_TimestampNumbered(t *testing.T) {
	t.Parallel()

	got, err := parseVersionFromFilename("20250101120000_add_users.sql")
	if err != nil {
		t.Fatalf("parseVersionFromFilename timestamp: unexpected error: %v", err)
	}
	if got != 20250101120000 {
		t.Errorf("parseVersionFromFilename timestamp: got %d; want 20250101120000", got)
	}
}

func TestParseVersionFromFilename_NoUnderscoreReturnsError(t *testing.T) {
	t.Parallel()

	_, err := parseVersionFromFilename("0001init.sql")
	if err == nil {
		t.Error("expected error for filename without underscore, got nil")
	}
}

func TestParseVersionFromFilename_EmptyPrefixReturnsError(t *testing.T) {
	t.Parallel()

	_, err := parseVersionFromFilename("_init.sql")
	if err == nil {
		t.Error("expected error for filename with empty version prefix, got nil")
	}
}

func TestParseVersionFromFilename_NonNumericPrefixReturnsError(t *testing.T) {
	t.Parallel()

	_, err := parseVersionFromFilename("abc_init.sql")
	if err == nil {
		t.Error("expected error for non-numeric version prefix, got nil")
	}
}

// ---------------------------------------------------------------------------
// writeStatusTable
// ---------------------------------------------------------------------------

func TestWriteStatusTable_ContainsAppliedAtColumn(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := writeStatusTable(&buf, []migrationStatusEntry{})
	if err != nil {
		t.Fatalf("writeStatusTable: unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Applied At") {
		t.Errorf("table output %q: missing 'Applied At' column header", out)
	}
}

func TestWriteStatusTable_ContainsMigrationColumn(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := writeStatusTable(&buf, []migrationStatusEntry{})
	if err != nil {
		t.Fatalf("writeStatusTable: unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Migration") {
		t.Errorf("table output %q: missing 'Migration' column header", out)
	}
}

func TestWriteStatusTable_ContainsSeparatorLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	_ = writeStatusTable(&buf, nil)
	out := buf.String()
	if !strings.Contains(out, "===") {
		t.Errorf("table output %q: missing separator line (===)", out)
	}
}

func TestWriteStatusTable_AppliedRowShowsTimestamp(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{
			Version:   1,
			Name:      "0001_init.sql",
			Status:    "applied",
			AppliedAt: "2025-06-01T10:00:00Z",
		},
	}
	var buf bytes.Buffer
	if err := writeStatusTable(&buf, entries); err != nil {
		t.Fatalf("writeStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0001_init.sql") {
		t.Errorf("table output %q: missing migration name '0001_init.sql'", out)
	}
	if !strings.Contains(out, "2025-06-01T10:00:00Z") {
		t.Errorf("table output %q: missing applied_at timestamp '2025-06-01T10:00:00Z'", out)
	}
	if !strings.Contains(out, "applied") {
		t.Errorf("table output %q: missing 'applied' status", out)
	}
}

func TestWriteStatusTable_PendingRowShowsPending(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{
			Version: 9999,
			Name:    "9999_test.sql",
			Status:  "pending",
		},
	}
	var buf bytes.Buffer
	if err := writeStatusTable(&buf, entries); err != nil {
		t.Fatalf("writeStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "9999_test.sql") {
		t.Errorf("table output %q: missing migration name '9999_test.sql'", out)
	}
	if !strings.Contains(out, "Pending") {
		t.Errorf("table output %q: missing 'Pending' marker for pending migration", out)
	}
}

func TestWriteStatusTable_MultipleEntries(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{Version: 1, Name: "0001_init.sql", Status: "applied", AppliedAt: "2025-01-01T00:00:00Z"},
		{Version: 2, Name: "0002_outbox.sql", Status: "applied", AppliedAt: "2025-01-02T00:00:00Z"},
		{Version: 9999, Name: "9999_test.sql", Status: "pending"},
	}
	var buf bytes.Buffer
	if err := writeStatusTable(&buf, entries); err != nil {
		t.Fatalf("writeStatusTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"0001_init.sql", "0002_outbox.sql", "9999_test.sql"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q", want)
		}
	}
	if !strings.Contains(out, "Pending") {
		t.Errorf("table output missing 'Pending' for 9999_test.sql")
	}
}

func TestWriteStatusTable_EmptyListHasHeaderOnly(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := writeStatusTable(&buf, nil); err != nil {
		t.Fatalf("writeStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Applied At") {
		t.Error("empty table output: missing 'Applied At' header")
	}
	// Should not contain any migration names when list is empty.
	if strings.Contains(out, ".sql") {
		t.Errorf("empty table output unexpectedly contains .sql: %q", out)
	}
}

// ---------------------------------------------------------------------------
// writeStatusJSON
// ---------------------------------------------------------------------------

func TestWriteStatusJSON_ProducesValidJSON(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{Version: 1, Name: "0001_init.sql", Status: "applied", AppliedAt: "2025-06-01T10:00:00Z"},
		{Version: 2, Name: "0002_outbox.sql", Status: "pending"},
	}
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, entries); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	// Each line must be a valid JSON object.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON lines, got %d: %q", len(lines), buf.String())
	}
	for i, line := range lines {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d %q is not valid JSON: %v", i, line, err)
		}
	}
}

func TestWriteStatusJSON_ContainsRequiredFields(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{Version: 1, Name: "0001_init.sql", Status: "applied", AppliedAt: "2025-06-01T10:00:00Z"},
	}
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, entries); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &obj); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	for _, field := range []string{"version", "name", "status", "applied_at"} {
		if _, ok := obj[field]; !ok {
			t.Errorf("JSON output missing field %q; got: %v", field, obj)
		}
	}
}

func TestWriteStatusJSON_PendingOmitsAppliedAt(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{Version: 9999, Name: "9999_test.sql", Status: "pending"},
	}
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, entries); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	output := buf.String()
	// "applied_at" key should be absent for pending migrations (omitempty).
	if strings.Contains(output, "applied_at") {
		t.Errorf("JSON for pending migration contains 'applied_at'; want omitted: %q", output)
	}
	if !strings.Contains(output, `"status":"pending"`) {
		t.Errorf("JSON for pending migration missing status:pending: %q", output)
	}
}

func TestWriteStatusJSON_StatusFieldValues(t *testing.T) {
	t.Parallel()

	entries := []migrationStatusEntry{
		{Version: 1, Name: "0001_init.sql", Status: "applied", AppliedAt: "2025-01-01T00:00:00Z"},
		{Version: 2, Name: "0002_outbox.sql", Status: "pending"},
	}
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, entries); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"status":"applied"`) {
		t.Errorf("JSON missing applied status entry: %q", out)
	}
	if !strings.Contains(out, `"status":"pending"`) {
		t.Errorf("JSON missing pending status entry: %q", out)
	}
}

func TestWriteStatusJSON_EmptyList(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, nil); err != nil {
		t.Fatalf("writeStatusJSON with nil: unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("writeStatusJSON with empty list: expected no output, got %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// migrationStatusEntry JSON marshaling
// ---------------------------------------------------------------------------

func TestMigrationStatusEntry_MarshalJSON_Applied(t *testing.T) {
	t.Parallel()

	e := migrationStatusEntry{
		Version:   1,
		Name:      "0001_init.sql",
		Status:    "applied",
		AppliedAt: "2025-06-01T10:00:00Z",
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got["version"] != float64(1) {
		t.Errorf("version: got %v, want 1", got["version"])
	}
	if got["name"] != "0001_init.sql" {
		t.Errorf("name: got %v, want '0001_init.sql'", got["name"])
	}
	if got["status"] != "applied" {
		t.Errorf("status: got %v, want 'applied'", got["status"])
	}
	if got["applied_at"] != "2025-06-01T10:00:00Z" {
		t.Errorf("applied_at: got %v, want '2025-06-01T10:00:00Z'", got["applied_at"])
	}
}

func TestMigrationStatusEntry_MarshalJSON_Pending(t *testing.T) {
	t.Parallel()

	e := migrationStatusEntry{
		Version: 2,
		Name:    "0002_outbox.sql",
		Status:  "pending",
		// AppliedAt intentionally empty
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	// applied_at must be absent (omitempty on empty string).
	if strings.Contains(string(data), "applied_at") {
		t.Errorf("pending entry JSON contains 'applied_at'; want omitted: %s", data)
	}
}

// ---------------------------------------------------------------------------
// Compile-time guard: pin the exported symbols this test depends on.
// ---------------------------------------------------------------------------

var (
	_ func(string) (int64, error) = parseVersionFromFilename
	_ = migrationStatusEntry{}
)

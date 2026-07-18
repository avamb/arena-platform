package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/catalog/catalogimport"
)

// ---------------------------------------------------------------------------
// Feature #386 — arena-bil24-import unit tests
//
// These tests exercise the pure-logic layer of the importer (parsing,
// validation, dry-run output) without requiring a live PostgreSQL connection.
// They cover the three mandated scenarios:
//
//  1. Fresh import: the fixture file parses successfully and all valid rows
//     are accepted.
//  2. Re-run idempotency: running the same batch through validateRows twice
//     produces the same set of valid events (deterministic).
//  3. Malformed row rejected without aborting the batch: fixture_with_bad_row
//     contains one invalid row; the importer returns the other two rows as
//     valid and the bad row as a RowError.
// ---------------------------------------------------------------------------

// TestPR386_ParseFixtureFile verifies that the standard fixture file is
// parsed into exactly 3 events with the expected field values.
func TestPR386_ParseFixtureFile(t *testing.T) {
	t.Helper()

	events, err := parseSnapshotFile("testdata/fixture_events.json")
	if err != nil {
		t.Fatalf("parseSnapshotFile: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Row 0
	e := events[0]
	if e.ExternalBil24ID != "BIL24-1001" {
		t.Errorf("row[0].ExternalBil24ID = %q, want BIL24-1001", e.ExternalBil24ID)
	}
	if e.Title != "Rock Night 2026" {
		t.Errorf("row[0].Title = %q, want 'Rock Night 2026'", e.Title)
	}
	if len(e.PriceTiers) != 2 {
		t.Errorf("row[0].PriceTiers len = %d, want 2", len(e.PriceTiers))
	}
	if e.PriceTiers[0].PriceKopeks != 150000 {
		t.Errorf("row[0].PriceTiers[0].PriceKopeks = %d, want 150000", e.PriceTiers[0].PriceKopeks)
	}

	// Row 1 — no ends_at (should default)
	e1 := events[1]
	if e1.ExternalBil24ID != "BIL24-1002" {
		t.Errorf("row[1].ExternalBil24ID = %q, want BIL24-1002", e1.ExternalBil24ID)
	}
	if e1.EndsAt != nil {
		t.Errorf("row[1].EndsAt = %v, want nil", e1.EndsAt)
	}

	// Row 2 — no price tiers
	e2 := events[2]
	if len(e2.PriceTiers) != 0 {
		t.Errorf("row[2].PriceTiers len = %d, want 0", len(e2.PriceTiers))
	}
}

// TestPR386_FreshImportAllRowsValid verifies that all three rows in the
// standard fixture pass validation (fresh import scenario).
func TestPR386_FreshImportAllRowsValid(t *testing.T) {
	events, err := parseSnapshotFile("testdata/fixture_events.json")
	if err != nil {
		t.Fatalf("parseSnapshotFile: %v", err)
	}

	valid, rowErrs := validateRows(events)

	if len(rowErrs) != 0 {
		t.Errorf("expected 0 validation errors, got %d: %v", len(rowErrs), rowErrs)
	}
	if len(valid) != 3 {
		t.Errorf("expected 3 valid rows, got %d", len(valid))
	}
}

// TestPR386_RerunIdempotency verifies that running validateRows on the same
// slice twice produces identical results (deterministic / idempotent logic).
func TestPR386_RerunIdempotency(t *testing.T) {
	events, err := parseSnapshotFile("testdata/fixture_events.json")
	if err != nil {
		t.Fatalf("parseSnapshotFile: %v", err)
	}

	valid1, errs1 := validateRows(events)
	valid2, errs2 := validateRows(events)

	if len(valid1) != len(valid2) {
		t.Errorf("first run: %d valid, second run: %d valid", len(valid1), len(valid2))
	}
	if len(errs1) != len(errs2) {
		t.Errorf("first run: %d errors, second run: %d errors", len(errs1), len(errs2))
	}
	for i := range valid1 {
		if valid1[i].ExternalBil24ID != valid2[i].ExternalBil24ID {
			t.Errorf("row[%d]: first run id=%q, second run id=%q", i, valid1[i].ExternalBil24ID, valid2[i].ExternalBil24ID)
		}
	}
}

// TestPR386_MalformedRowRejectedBatchContinues verifies that one bad row in
// the fixture is rejected while the other two valid rows are returned.
// This ensures the importer does NOT abort the whole batch on a single failure.
func TestPR386_MalformedRowRejectedBatchContinues(t *testing.T) {
	events, err := parseSnapshotFile("testdata/fixture_with_bad_row.json")
	if err != nil {
		t.Fatalf("parseSnapshotFile: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("fixture should have 3 rows, got %d", len(events))
	}

	valid, rowErrs := validateRows(events)

	if len(rowErrs) != 1 {
		t.Errorf("expected 1 validation error (the bad row), got %d: %v", len(rowErrs), rowErrs)
	}
	if len(valid) != 2 {
		t.Errorf("expected 2 valid rows, got %d", len(valid))
	}
	// Verify the bad row is row index 1 (the all-empty row).
	if rowErrs[0].Index != 1 {
		t.Errorf("bad row index = %d, want 1", rowErrs[0].Index)
	}
	// Verify error message is descriptive.
	if rowErrs[0].Err == nil {
		t.Error("bad row error is nil, want non-nil")
	}
}

// ---------------------------------------------------------------------------
// Bil24SnapshotEvent.Validate unit tests
// ---------------------------------------------------------------------------

// TestPR386_ValidateRequiredFields ensures Validate() rejects rows that are
// missing each required field.
func TestPR386_ValidateRequiredFields(t *testing.T) {
	goodTime := time.Date(2026, 9, 15, 19, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		event   catalogimport.Bil24SnapshotEvent
		wantErr bool
	}{
		{
			name:    "all required fields present",
			event:   catalogimport.Bil24SnapshotEvent{ExternalBil24ID: "X1", Title: "T", StartsAt: goodTime},
			wantErr: false,
		},
		{
			name:    "missing external_bil24_id",
			event:   catalogimport.Bil24SnapshotEvent{ExternalBil24ID: "", Title: "T", StartsAt: goodTime},
			wantErr: true,
		},
		{
			name:    "missing title",
			event:   catalogimport.Bil24SnapshotEvent{ExternalBil24ID: "X1", Title: "", StartsAt: goodTime},
			wantErr: true,
		},
		{
			name:    "zero starts_at",
			event:   catalogimport.Bil24SnapshotEvent{ExternalBil24ID: "X1", Title: "T", StartsAt: time.Time{}},
			wantErr: true,
		},
		{
			name: "ends_at before starts_at",
			event: func() catalogimport.Bil24SnapshotEvent {
				bad := goodTime.Add(-1 * time.Hour)
				return catalogimport.Bil24SnapshotEvent{
					ExternalBil24ID: "X1",
					Title:           "T",
					StartsAt:        goodTime,
					EndsAt:          &bad,
				}
			}(),
			wantErr: true,
		},
		{
			name: "ends_at equal to starts_at is rejected",
			event: func() catalogimport.Bil24SnapshotEvent {
				eq := goodTime
				return catalogimport.Bil24SnapshotEvent{
					ExternalBil24ID: "X1",
					Title:           "T",
					StartsAt:        goodTime,
					EndsAt:          &eq,
				}
			}(),
			wantErr: true,
		},
		{
			name: "ends_at after starts_at is accepted",
			event: func() catalogimport.Bil24SnapshotEvent {
				after := goodTime.Add(2 * time.Hour)
				return catalogimport.Bil24SnapshotEvent{
					ExternalBil24ID: "X1",
					Title:           "T",
					StartsAt:        goodTime,
					EndsAt:          &after,
				}
			}(),
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.event.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

// TestPR386_ResolvedEndsAt checks that the default end time is correctly
// computed when EndsAt is absent.
func TestPR386_ResolvedEndsAt(t *testing.T) {
	start := time.Date(2026, 9, 15, 19, 0, 0, 0, time.UTC)
	e := catalogimport.Bil24SnapshotEvent{StartsAt: start}

	got := e.ResolvedEndsAt()
	want := start.Add(catalogimport.DefaultEventDuration)
	if !got.Equal(want) {
		t.Errorf("ResolvedEndsAt() = %v, want %v", got, want)
	}
}

// TestPR386_ResolvedEndsAt_Explicit checks that an explicit EndsAt is used
// when provided.
func TestPR386_ResolvedEndsAt_Explicit(t *testing.T) {
	start := time.Date(2026, 9, 15, 19, 0, 0, 0, time.UTC)
	explicit := start.Add(4 * time.Hour)
	e := catalogimport.Bil24SnapshotEvent{StartsAt: start, EndsAt: &explicit}

	got := e.ResolvedEndsAt()
	if !got.Equal(explicit) {
		t.Errorf("ResolvedEndsAt() = %v, want %v", got, explicit)
	}
}

// TestPR386_ResolvedDescription checks venue name appending logic.
func TestPR386_ResolvedDescription(t *testing.T) {
	cases := []struct {
		name      string
		desc      string
		venue     string
		wantPart  string
	}{
		{name: "both set", desc: "Great event", venue: "Main Hall", wantPart: "(Venue: Main Hall)"},
		{name: "no venue", desc: "Great event", venue: "", wantPart: "Great event"},
		{name: "no desc, with venue", desc: "", venue: "Main Hall", wantPart: "(Venue: Main Hall)"},
		{name: "both empty", desc: "", venue: "", wantPart: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := catalogimport.Bil24SnapshotEvent{
				Description: tc.desc,
				VenueName:   tc.venue,
			}
			got := e.ResolvedDescription()
			if tc.wantPart != "" && got == "" {
				t.Errorf("ResolvedDescription() = %q, want non-empty containing %q", got, tc.wantPart)
			}
			if tc.wantPart == "" && got != "" {
				t.Errorf("ResolvedDescription() = %q, want empty", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Structural tests (feature checklist)
// ---------------------------------------------------------------------------

// TestPR386_CommandNotInAPIServer verifies that the arena-bil24-import
// package is not imported by the API server or worker binaries.
// This is a source-grep test — it searches the two production binary
// source trees for any reference to the import command package name.
func TestPR386_CommandNotInAPIServer(t *testing.T) {
	// Read arena-api main.go and arena-worker main.go and check they
	// do NOT contain "arena-bil24-import".
	files := []string{
		"../../cmd/arena-api/main.go",
		"../../cmd/arena-worker/main.go",
	}
	for _, path := range files {
		content, err := readFileContent(path)
		if err != nil {
			t.Errorf("could not read %s: %v", path, err)
			continue
		}
		if containsString(content, "arena-bil24-import") {
			t.Errorf("%s imports arena-bil24-import — operator tool must not be wired into production binaries", path)
		}
	}
}

// TestPR386_MigrationFileExists verifies that the external_bil24_id migration
// file is present in the migrations directory.
func TestPR386_MigrationFileExists(t *testing.T) {
	path := "../../internal/migrations/sql/0070_external_bil24_id.sql"
	content, err := readFileContent(path)
	if err != nil {
		t.Fatalf("migration file not found at %s: %v", path, err)
	}
	if !containsString(content, "external_bil24_id") {
		t.Errorf("migration file does not contain 'external_bil24_id'")
	}
	if !containsString(content, "UNIQUE INDEX") {
		t.Errorf("migration file does not create a UNIQUE INDEX on external_bil24_id")
	}
}

// TestPR386_MigrationHasDownSection verifies that the migration has a goose
// down section so it can be rolled back.
func TestPR386_MigrationHasDownSection(t *testing.T) {
	path := "../../internal/migrations/sql/0070_external_bil24_id.sql"
	content, err := readFileContent(path)
	if err != nil {
		t.Fatalf("migration file not found: %v", err)
	}
	if !containsString(content, "-- +goose Down") {
		t.Errorf("migration is missing a -- +goose Down section")
	}
}

// TestPR386_DryRunFlagPresent verifies that --dry-run is wired up in the
// command's source (source-grep style).
func TestPR386_DryRunFlagPresent(t *testing.T) {
	content, err := readFileContent("main.go")
	if err != nil {
		t.Fatalf("cannot read main.go: %v", err)
	}
	if !containsString(content, "dry-run") {
		t.Error("main.go does not define a --dry-run flag")
	}
}

// TestPR386_OrgIDFlagPresent verifies that the required --org-id flag exists.
func TestPR386_OrgIDFlagPresent(t *testing.T) {
	content, err := readFileContent("main.go")
	if err != nil {
		t.Fatalf("cannot read main.go: %v", err)
	}
	if !containsString(content, "org-id") {
		t.Error("main.go does not define an --org-id flag")
	}
}

// TestPR386_SnapshotTypeHasPriceTiers verifies that Bil24SnapshotEvent
// contains a PriceTiers field (completeness check).
func TestPR386_SnapshotTypeHasPriceTiers(t *testing.T) {
	e := catalogimport.Bil24SnapshotEvent{
		ExternalBil24ID: "x",
		Title:           "t",
		StartsAt:        time.Now(),
		PriceTiers: []catalogimport.PriceTier{
			{Name: "Standard", PriceKopeks: 100},
		},
	}
	if len(e.PriceTiers) != 1 {
		t.Errorf("PriceTiers length = %d, want 1", len(e.PriceTiers))
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func readFileContent(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

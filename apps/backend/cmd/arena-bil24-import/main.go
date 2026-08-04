// Package main is the entry point for arena-bil24-import, a one-shot operator
// tool that reads a Bil24 event catalog snapshot (CSV or JSON) and materialises
// the events as native arena_new catalog entries (features #386/#387).
//
// # Purpose
//
// When migrating away from Bil24, the current set of active events needs to
// appear in arena_new without requiring a live API bridge. arena-bil24-import
// performs a single, idempotent snapshot import: it reads a CSV or JSON file
// (produced by the operator from the Bil24 admin export), validates each row,
// and inserts events into the native events table with ON CONFLICT DO NOTHING
// keyed on external_bil24_id.
//
// # Source format — auto-detected by extension
//
// The --input flag must point to either a CSV or JSON file.
//
// CSV format (use a .csv extension):
//
//	external_bil24_id,title,starts_at,ends_at,venue_name,description,poster_url
//	12345,Rock Night,2026-09-15T19:00:00Z,2026-09-15T22:00:00Z,Main Hall,Rock festival,https://...
//
// JSON format (use a .json extension):
//
//	[
//	  {
//	    "external_bil24_id": "12345",
//	    "title":             "Rock Night",
//	    "starts_at":         "2026-09-15T19:00:00Z",
//	    "ends_at":           "2026-09-15T22:00:00Z",
//	    "venue_name":        "Main Hall",
//	    "description":       "Annual rock festival.",
//	    "poster_url":        "https://cdn.example.com/events/12345.jpg",
//	    "price_tiers": [
//	      {"name": "Standard", "price_kopeks": 150000},
//	      {"name": "VIP",      "price_kopeks": 350000}
//	    ]
//	  }
//	]
//
// ends_at is optional in both formats; it defaults to starts_at + 3 hours.
// price_tiers are JSON-only; CSV rows have no price tier column.
// In both formats price_tiers are logged in the summary but not written to DB.
//
// # Idempotency
//
// Every INSERT uses ON CONFLICT (external_bil24_id) DO NOTHING. Running the
// importer twice against the same snapshot is completely safe — the second run
// reports 0 imported and N skipped.
//
// # BUILD ISOLATION
//
// arena-bil24-import is an OPERATOR TOOL ONLY. It must never be imported by
// or compiled into the arena-api or arena-worker binaries. The separate
// cmd/arena-bil24-import package guarantees this at the Go build level: the
// API and worker binaries have no dependency on this package. Verify with:
//
//	grep -r "arena-bil24-import" apps/backend/cmd/arena-api apps/backend/cmd/arena-worker
//	# must return no matches
//
// # Usage
//
//	arena-bil24-import \
//	  --input    /path/to/export.csv   \
//	  --org-id   <UUID of the target organization> \
//	  [--dry-run]
//
// Exit code is 0 on success (even if all rows were skipped), 1 on fatal error.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/catalog/catalogimport"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "arena-bil24-import: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("arena-bil24-import", flag.ContinueOnError)
	inputFile := fs.String("input", "", "path to the Bil24 export file (.csv or .json, required)")
	orgID := fs.String("org-id", "", "UUID of the arena_new organization that will own the imported events (required)")
	dryRun := fs.Bool("dry-run", false, "print what would be imported without touching the database")
	dbURL := fs.String("db-url", "", "PostgreSQL DSN (overrides DATABASE_URL env var)")
	venuesMode := fs.Bool("venues", false, "import countries, cities, and venues from the live Bil24 API")
	bil24URL := fs.String("bil24-url", "", "Bil24 JSON RPC URL (overrides BIL24_API_URL)")
	bil24FID := fs.String("bil24-fid", "", "Bil24 frontend ID (overrides BIL24_FID)")
	bil24Token := fs.String("bil24-token", "", "Bil24 API token (overrides BIL24_TOKEN)")
	bil24Locale := fs.String("bil24-locale", "ru-RU", "Bil24 response locale")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*venuesMode && *inputFile == "" {
		return fmt.Errorf("--input is required; point it at the Bil24 export file (.csv or .json)")
	}
	if *orgID == "" {
		return fmt.Errorf("--org-id is required; provide the UUID of the target organization")
	}
	if *venuesMode {
		return runLiveVenueImport(liveVenueImportOptions{
			OrgID: *orgID, DryRun: *dryRun, DBURL: *dbURL, APIURL: *bil24URL,
			FID: *bil24FID, Token: *bil24Token, Locale: *bil24Locale,
		})
	}

	// -------------------------------------------------------------------------
	// Parse snapshot file (auto-detects CSV vs JSON by extension)
	// -------------------------------------------------------------------------
	events, err := parseSnapshotFile(*inputFile)
	if err != nil {
		return fmt.Errorf("parse snapshot %q: %w", *inputFile, err)
	}

	slog.Info("snapshot loaded", "path", *inputFile, "rows", len(events))

	// -------------------------------------------------------------------------
	// Validate all rows (collect per-row errors; do NOT abort)
	// -------------------------------------------------------------------------
	validated, rowErrs := validateRows(events)

	// -------------------------------------------------------------------------
	// Dry-run: print summary and exit
	// -------------------------------------------------------------------------
	if *dryRun {
		printDryRun(validated, rowErrs, *orgID)
		return nil
	}

	// -------------------------------------------------------------------------
	// Resolve DATABASE_URL
	// -------------------------------------------------------------------------
	dsn := *dbURL
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable or --db-url flag is required")
	}

	// -------------------------------------------------------------------------
	// Connect
	// -------------------------------------------------------------------------
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// -------------------------------------------------------------------------
	// Import inside a single transaction
	// -------------------------------------------------------------------------
	stats, importErr := importBatch(ctx, pool, validated, *orgID)

	// Print summary regardless of whether some rows failed.
	printSummary(stats, rowErrs, importErr)

	if importErr != nil {
		return importErr
	}
	return nil
}

// ---------------------------------------------------------------------------
// Parse
// ---------------------------------------------------------------------------

// parseSnapshotFile reads the Bil24 export file and returns the parsed events.
// It auto-detects the file format by extension: ".csv" → CSV parser,
// anything else (typically ".json") → JSON parser.
func parseSnapshotFile(path string) ([]catalogimport.Bil24SnapshotEvent, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".csv" {
		return parseCSVFile(path)
	}
	return parseJSONFile(path)
}

// parseJSONFile reads and JSON-decodes the Bil24 export file.
func parseJSONFile(path string) ([]catalogimport.Bil24SnapshotEvent, error) {
	f, err := os.Open(path) //nolint:gosec // operator-controlled path; safe for operator tools
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // read-only file close

	var events []catalogimport.Bil24SnapshotEvent
	if err := json.NewDecoder(f).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	return events, nil
}

// csvColIndex maps column names to their 0-based position in the CSV header.
type csvColIndex map[string]int

// parseCSVFile reads a Bil24 admin CSV export and maps rows to
// Bil24SnapshotEvent values. The CSV must contain a header row with these
// columns (in any order):
//
//	external_bil24_id, title, starts_at, ends_at, venue_name, description, poster_url
//
// ends_at, venue_name, description, and poster_url are optional and may be
// left empty. price_tiers are not supported in the CSV format.
func parseCSVFile(path string) ([]catalogimport.Bil24SnapshotEvent, error) {
	f, err := os.Open(path) //nolint:gosec // operator-controlled path; safe for operator tools
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // read-only file close

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	// Allow rows to have fewer fields than the header (e.g. trailing optional
	// columns omitted). -1 means "no fixed field count check".
	r.FieldsPerRecord = -1

	// Read header
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header: %w", err)
	}

	idx := make(csvColIndex, len(header))
	for i, col := range header {
		idx[strings.ToLower(strings.TrimSpace(col))] = i
	}

	// Validate required columns
	required := []string{"external_bil24_id", "title", "starts_at"}
	for _, col := range required {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("CSV is missing required column %q", col)
		}
	}

	var events []catalogimport.Bil24SnapshotEvent

	for rowNum := 2; ; rowNum++ {
		record, err := r.Read()
		if err != nil {
			// io.EOF (possibly wrapped) signals end of file — normal termination.
			if errors.Is(err, io.EOF) {
				break
			}
			// Any other read error (malformed CSV, OS error) is fatal.
			return nil, fmt.Errorf("row %d: %w", rowNum, err)
		}

		e := catalogimport.Bil24SnapshotEvent{}

		e.ExternalBil24ID = csvField(record, idx, "external_bil24_id")
		e.Title = csvField(record, idx, "title")
		e.VenueName = csvField(record, idx, "venue_name")
		e.Description = csvField(record, idx, "description")
		e.PosterURL = csvField(record, idx, "poster_url")

		startsAtStr := csvField(record, idx, "starts_at")
		if startsAtStr != "" {
			t, parseErr := time.Parse(time.RFC3339, startsAtStr)
			if parseErr != nil {
				// Try without timezone suffix (local time)
				t, parseErr = time.Parse("2006-01-02T15:04:05", startsAtStr)
			}
			if parseErr == nil {
				e.StartsAt = t
			}
			// If still can't parse, StartsAt stays zero → Validate() will reject the row
		}

		endsAtStr := csvField(record, idx, "ends_at")
		if endsAtStr != "" {
			t, parseErr := time.Parse(time.RFC3339, endsAtStr)
			if parseErr != nil {
				t, parseErr = time.Parse("2006-01-02T15:04:05", endsAtStr)
			}
			if parseErr == nil {
				e.EndsAt = &t
			}
		}

		events = append(events, e)
	}

	return events, nil
}

// csvField returns the trimmed value of a named column in a CSV record,
// or an empty string if the column is not present or the record is too short.
func csvField(record []string, idx csvColIndex, col string) string {
	i, ok := idx[col]
	if !ok || i >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[i])
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

// RowError pairs a row index with its validation error.
type RowError struct {
	Index int
	ID    string
	Err   error
}

// validateRows validates every event in the snapshot. Valid rows are returned
// in the first slice; invalid rows appear in the second slice. Invalid rows
// are NOT included in the first slice so the caller can proceed with valid
// rows only.
func validateRows(events []catalogimport.Bil24SnapshotEvent) ([]catalogimport.Bil24SnapshotEvent, []RowError) {
	var valid []catalogimport.Bil24SnapshotEvent
	var errs []RowError

	for i := range events {
		e := &events[i]
		if err := e.Validate(); err != nil {
			errs = append(errs, RowError{Index: i, ID: e.ExternalBil24ID, Err: err})
			continue
		}
		valid = append(valid, *e)
	}
	return valid, errs
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

// ImportStats holds per-run import counters.
type ImportStats struct {
	Imported int
	Skipped  int
}

// importBatch inserts all valid rows inside a single transaction.
// Each INSERT uses ON CONFLICT (external_bil24_id) DO NOTHING so a re-run
// against the same snapshot is a safe no-op.
func importBatch(ctx context.Context, pool *pgxpool.Pool, events []catalogimport.Bil24SnapshotEvent, orgID string) (ImportStats, error) {
	var stats ImportStats

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return stats, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op after commit

	for i := range events {
		e := &events[i]

		// Wave 4 (AB-37): events carry no own dates — the snapshot's
		// starts_at/ends_at describe the session-level schedule, which this
		// importer does not materialize (sessions require a venue binding,
		// AB-36). Operators create the sessions — and with them the dates —
		// after the import; until then the event renders with no date.
		tag, err := tx.Exec(ctx, `
			INSERT INTO events (
				org_id,
				name,
				description,
				status,
				visibility,
				image_url,
				external_bil24_id
			)
			VALUES ($1, $2, $3, 'draft', 'public', $4, $5)
			ON CONFLICT (external_bil24_id)
			WHERE external_bil24_id IS NOT NULL
			DO NOTHING
		`,
			orgID,
			e.Title,
			e.ResolvedDescription(),
			nilIfEmpty(e.PosterURL),
			e.ExternalBil24ID,
		)
		if err != nil {
			return stats, fmt.Errorf("insert event %q (index %d): %w", e.ExternalBil24ID, i, err)
		}
		if tag.RowsAffected() == 0 {
			stats.Skipped++
		} else {
			stats.Imported++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, fmt.Errorf("commit tx: %w", err)
	}
	return stats, nil
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

func printDryRun(valid []catalogimport.Bil24SnapshotEvent, rowErrs []RowError, orgID string) {
	fmt.Fprintf(os.Stdout, "arena-bil24-import: DRY RUN — no database changes will be made\n\n")
	fmt.Fprintf(os.Stdout, "Target org:   %s\n", orgID)
	fmt.Fprintf(os.Stdout, "Valid rows:   %d\n", len(valid))
	fmt.Fprintf(os.Stdout, "Invalid rows: %d\n\n", len(rowErrs))

	if len(valid) > 0 {
		fmt.Fprintln(os.Stdout, "Would import:")
		for _, e := range valid {
			tiers := ""
			if len(e.PriceTiers) > 0 {
				tiers = fmt.Sprintf(" [%d price tier(s)]", len(e.PriceTiers))
			}
			venue := ""
			if e.VenueName != "" {
				venue = fmt.Sprintf(" @ %s", e.VenueName)
			}
			fmt.Fprintf(os.Stdout, "  [%s] %s%s%s  starts=%s  ends=%s\n",
				e.ExternalBil24ID,
				e.Title,
				venue,
				tiers,
				e.StartsAt.Format(time.RFC3339),
				e.ResolvedEndsAt().Format(time.RFC3339),
			)
		}
	}

	if len(rowErrs) > 0 {
		fmt.Fprintln(os.Stdout, "\nWould reject (malformed rows):")
		for _, re := range rowErrs {
			fmt.Fprintf(os.Stdout, "  row[%d] id=%q: %v\n", re.Index, re.ID, re.Err)
		}
	}
}

func printSummary(stats ImportStats, rowErrs []RowError, importErr error) {
	fmt.Fprintf(os.Stdout, "\narena-bil24-import summary\n")
	fmt.Fprintf(os.Stdout, "  imported: %d\n", stats.Imported)
	fmt.Fprintf(os.Stdout, "  skipped (already present): %d\n", stats.Skipped)
	fmt.Fprintf(os.Stdout, "  rejected (validation failed): %d\n", len(rowErrs))
	for _, re := range rowErrs {
		fmt.Fprintf(os.Stdout, "    row[%d] id=%q: %v\n", re.Index, re.ID, re.Err)
	}
	if importErr != nil {
		fmt.Fprintf(os.Stderr, "  fatal error during import: %v\n", importErr)
	}
}

// nilIfEmpty returns nil for an empty string, or the string pointer otherwise.
// Used to insert NULL instead of an empty string for optional text columns.
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

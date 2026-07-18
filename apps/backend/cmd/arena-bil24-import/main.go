// Package main is the entry point for arena-bil24-import, a one-shot operator
// tool that reads a Bil24 event catalog snapshot and materialises the events
// as native arena_new catalog entries (feature #386).
//
// # Purpose
//
// When migrating away from Bil24, the current set of active events needs to
// appear in arena_new without requiring a live API bridge. arena-bil24-import
// performs a single, idempotent snapshot import: it reads a JSON file
// (produced by the operator from the Bil24 admin export), validates each row,
// and inserts events into the native events table with ON CONFLICT DO NOTHING
// keyed on external_bil24_id.
//
// # Source format
//
// The --source flag must point to a JSON file containing an array of objects:
//
//	[
//	  {
//	    "external_bil24_id": "12345",
//	    "title":             "Rock Night",
//	    "starts_at":         "2026-09-15T19:00:00Z",
//	    "ends_at":           "2026-09-15T22:00:00Z",
//	    "venue_name":        "Main Hall",
//	    "description":       "Annual rock festival.",
//	    "poster_url":        "https://cdn.bil24.pro/events/12345.jpg",
//	    "price_tiers": [
//	      {"name": "Standard", "price_kopeks": 150000},
//	      {"name": "VIP",      "price_kopeks": 350000}
//	    ]
//	  }
//	]
//
// ends_at is optional; it defaults to starts_at + 3 hours.
// price_tiers are optional and logged in the summary but not written to the DB.
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
//	  --source   /path/to/export.json \
//	  --org-id   <UUID of the target organization> \
//	  [--dry-run]
//
// Exit code is 0 on success (even if all rows were skipped), 1 on fatal error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
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
	sourceFile := fs.String("source", "", "path to the Bil24 JSON export file (required)")
	orgID := fs.String("org-id", "", "UUID of the arena_new organization that will own the imported events (required)")
	dryRun := fs.Bool("dry-run", false, "print what would be imported without touching the database")
	dbURL := fs.String("db-url", "", "PostgreSQL DSN (overrides DATABASE_URL env var)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *sourceFile == "" {
		return fmt.Errorf("--source is required; point it at the Bil24 JSON export file")
	}
	if *orgID == "" {
		return fmt.Errorf("--org-id is required; provide the UUID of the target organization")
	}

	// -------------------------------------------------------------------------
	// Parse snapshot file
	// -------------------------------------------------------------------------
	events, err := parseSnapshotFile(*sourceFile)
	if err != nil {
		return fmt.Errorf("parse snapshot %q: %w", *sourceFile, err)
	}

	slog.Info("snapshot loaded", "path", *sourceFile, "rows", len(events))

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

// parseSnapshotFile reads and JSON-decodes the Bil24 export file.
func parseSnapshotFile(path string) ([]catalogimport.Bil24SnapshotEvent, error) {
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

		tag, err := tx.Exec(ctx, `
			INSERT INTO events (
				org_id,
				name,
				description,
				status,
				start_at,
				end_at,
				visibility,
				image_url,
				external_bil24_id
			)
			VALUES ($1, $2, $3, 'draft', $4, $5, 'public', $6, $7)
			ON CONFLICT (external_bil24_id)
			WHERE external_bil24_id IS NOT NULL
			DO NOTHING
		`,
			orgID,
			e.Title,
			e.ResolvedDescription(),
			e.StartsAt.UTC(),
			e.ResolvedEndsAt().UTC(),
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

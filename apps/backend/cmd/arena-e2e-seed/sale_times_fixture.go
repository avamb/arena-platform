package main

// Fixture of the web twin test of task 5-3 (the sales end and the doors-open
// time of a session, apps/event-center/tests/smoke/live/sale-times.spec.ts).
//
// One extra event of the fixture organization, "E2E Sale Times Show", with ONE
// date in the future and nothing else: the screen and the PATCH need no
// category, and an event of its own keeps the other twins (5-2 moves and
// cancels sessions, 5-4 edits categories) away from this date.
//
// The spec calls two operations through the seed binary, because the test
// process has no SQL driver:
//
//	--sale-times-op reset    puts the date's sales end back on its start and
//	                         clears the doors-open time, so a repeated run
//	                         starts clean;
//	--sale-times-op inspect  prints {start_at, end_at, sales_end_at,
//	                         doors_open_at} as RFC 3339 instants (UTC).
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"fmt"
	"time"
	_ "time/tzdata" // the zone must resolve on a host without system tzdata

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	saleTimesEventID   = "fe0000e2-0000-7000-8000-00000000e301"
	saleTimesSessionID = "fe0000e2-0000-7000-8000-00000000e302"
	saleTimesEventName = "E2E Sale Times Show"
)

// applySaleTimesFixture is idempotent AND resetting.
func applySaleTimesFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("sale times fixture %s: %w", what, err)
		}
		return nil
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, saleTimesEventID, orgID, saleTimesEventName); err != nil {
		return err
	}
	// The date starts at NOON on the venue's clock (Madrid), whatever hour the
	// seed runs in. The spec needs its doors values (90 minutes before the
	// start, one hour after it) on the session's own calendar day: a start
	// truncated from the current instant wrapped them across midnight when the
	// job ran between 22:00 and 00:59 UTC (CI run of 2026-10-26) and the
	// "doors after the start" refusal never appeared.
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		return fmt.Errorf("sale times fixture venue zone: %w", err)
	}
	day := time.Now().In(madrid).AddDate(0, 0, 40)
	start := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, madrid).UTC()
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 10, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO UPDATE SET start_at = EXCLUDED.start_at, end_at = EXCLUDED.end_at,
			status = 'scheduled', deleted_at = NULL`, saleTimesSessionID, saleTimesEventID, venueID, start); err != nil {
		return err
	}
	// A moved start_at carries the sales end along (migration 0128's trigger); the
	// explicit reset also clears the doors time and covers a start that did not move.
	return run("reset times", `
		UPDATE sessions SET sales_end_at = start_at, doors_open_at = NULL
		WHERE id = $1`, saleTimesSessionID)
}

// runSaleTimesOp serves --sale-times-op. It prints one JSON line.
func runSaleTimesOp(op string) error {
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()

	switch op {
	case "reset":
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := applySaleTimesFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printOrgDataJSON(map[string]string{"ok": "reset"})
	case "inspect":
		var start, end, salesEnd time.Time
		var doors *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT start_at, end_at, sales_end_at, doors_open_at
			FROM sessions WHERE id = $1`, saleTimesSessionID).Scan(&start, &end, &salesEnd, &doors); err != nil {
			return fmt.Errorf("inspect session: %w", err)
		}
		var doorsText *string
		if doors != nil {
			v := doors.UTC().Format(time.RFC3339)
			doorsText = &v
		}
		return printOrgDataJSON(map[string]any{
			"start_at":      start.UTC().Format(time.RFC3339),
			"end_at":        end.UTC().Format(time.RFC3339),
			"sales_end_at":  salesEnd.UTC().Format(time.RFC3339),
			"doors_open_at": doorsText,
		})
	default:
		return fmt.Errorf("unknown --sale-times-op %q (reset, inspect)", op)
	}
}

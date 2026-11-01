package main

// Fixture of the web twin test of the session's sales state (migration 0183,
// apps/event-center/tests/smoke/live/sales-state.spec.ts).
//
// One extra event of the fixture organization, "E2E Sales State Show", with
// ONE general-admission date and one category of five free places: without a
// state set by hand the date sells ("open"), so every switch the spec makes
// is visible. It has its own event so a "sold out" set here never refuses a
// hold another twin takes.
//
// The spec reads the state through the API itself (GET .../sales-state); the
// seed binary only puts the date back:
//
//	--sales-state-op reset   clears the state, its journal and its outbox rows,
//	                         and restores the five free places.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

const (
	salesStateEventID   = "fe0000e2-0000-7000-8000-00000000e701"
	salesStateSessionID = "fe0000e2-0000-7000-8000-00000000e702"
	salesStateLedgerID  = "fe0000e2-0000-7000-8000-00000000e704"
	salesStateTierID    = "fe0000e2-0000-7000-8000-00000000e711"
	salesStateEventName = "E2E Sales State Show"
	salesStatePlaces    = 5
)

// applySalesStateFixture is idempotent AND resetting.
func applySalesStateFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("sales state fixture %s: %w", what, err)
		}
		return nil
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, salesStateEventID, orgID, salesStateEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 40).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO UPDATE SET status = 'scheduled', deleted_at = NULL`,
		salesStateSessionID, salesStateEventID, venueID, start, salesStatePlaces); err != nil {
		return err
	}
	for _, step := range []struct{ what, sql string }{
		{"journal", `DELETE FROM session_sales_state_changes WHERE session_id = $1`},
		{"state", `DELETE FROM session_sales_states WHERE session_id = $1`},
		{"places", `DELETE FROM session_seats WHERE session_id = $1`},
		{"ledger", `DELETE FROM inventory_ledger WHERE session_id = $1`},
		{"category", `DELETE FROM ticket_tiers WHERE session_id = $1`},
	} {
		if err := run(step.what, step.sql, salesStateSessionID); err != nil {
			return err
		}
	}
	if err := run("outbox", `DELETE FROM outbox_events WHERE aggregate_id = $1`, salesStateEventID); err != nil {
		return err
	}
	if err := run("ledger row", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
		VALUES ($1, $2, NULL, $3)`, salesStateLedgerID, salesStateSessionID, salesStatePlaces); err != nil {
		return err
	}
	if err := run("category", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
		VALUES ($1, $2, 'E2E Sales State Standing', 'fixed', 2000, 'EUR', $3, 0)`,
		salesStateTierID, salesStateSessionID, salesStatePlaces); err != nil {
		return err
	}
	sid, _ := uuid.Parse(salesStateSessionID)
	tid, _ := uuid.Parse(salesStateTierID)
	if err := gaquota.CreateCategory(ctx, gen.New(tx), sid, tid, salesStatePlaces); err != nil {
		return fmt.Errorf("sales state fixture places: %w", err)
	}
	return nil
}

// runSalesStateOp serves --sales-state-op. It prints one JSON line.
func runSalesStateOp(op string) error {
	if op != "reset" {
		return fmt.Errorf("unknown --sales-state-op %q (reset)", op)
	}
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applySalesStateFixture(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return printOrgDataJSON(map[string]string{"ok": "reset"})
}

package main

// Fixture of the web twin test of the smart price presets (spec
// 08_architecture/51_4_2_smart_price_ru.md,
// apps/event-center/tests/smoke/live/smart-price.spec.ts).
//
// One extra event of the fixture organization, "E2E Smart Price Show", with
// ONE general-admission date and one category of 100 free places at 20 EUR.
// It has its own event so the chain a preset creates never touches another
// twin's categories.
//
//	--smart-price-op reset   removes every preset, step and place and puts the
//	                         one category back with 100 free places.
//	--smart-price-op sell    sells every free place of the step that sells now
//	                         and runs the chain sweep once, as the worker would.
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/tierchain"
)

const (
	smartPriceEventID   = "fe0000e2-0000-7000-8000-00000000e801"
	smartPriceSessionID = "fe0000e2-0000-7000-8000-00000000e802"
	smartPriceLedgerID  = "fe0000e2-0000-7000-8000-00000000e804"
	smartPriceTierID    = "fe0000e2-0000-7000-8000-00000000e811"
	smartPriceEventName = "E2E Smart Price Show"
	smartPricePlaces    = 100
)

// applySmartPriceFixture is idempotent AND resetting.
func applySmartPriceFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("smart price fixture %s: %w", what, err)
		}
		return nil
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, smartPriceEventID, orgID, smartPriceEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 45).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO UPDATE SET status = 'scheduled', deleted_at = NULL, capacity_total = EXCLUDED.capacity_total`,
		smartPriceSessionID, smartPriceEventID, venueID, start, smartPricePlaces); err != nil {
		return err
	}
	for _, step := range []struct{ what, sql string }{
		{"presets", `DELETE FROM smart_price_rules WHERE session_id = $1`},
		{"places", `DELETE FROM session_seats WHERE session_id = $1`},
		{"ledger", `DELETE FROM inventory_ledger WHERE session_id = $1`},
		{"compat ids", `DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM ticket_tiers WHERE session_id = $1)`},
		{"categories", `DELETE FROM ticket_tiers WHERE session_id = $1`},
	} {
		if err := run(step.what, step.sql, smartPriceSessionID); err != nil {
			return err
		}
	}
	if err := run("ledger row", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
		VALUES ($1, $2, NULL, $3)`, smartPriceLedgerID, smartPriceSessionID, smartPricePlaces); err != nil {
		return err
	}
	if err := run("category", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
		VALUES ($1, $2, 'E2E Smart Standing', 'fixed', 2000, 'EUR', $3, 0)`,
		smartPriceTierID, smartPriceSessionID, smartPricePlaces); err != nil {
		return err
	}
	sid, _ := uuid.Parse(smartPriceSessionID)
	tid, _ := uuid.Parse(smartPriceTierID)
	if err := gaquota.CreateCategory(ctx, gen.New(tx), sid, tid, smartPricePlaces); err != nil {
		return fmt.Errorf("smart price fixture places: %w", err)
	}
	return nil
}

// runSmartPriceOp serves --smart-price-op. It prints one JSON line.
func runSmartPriceOp(op string) error {
	if op != "reset" && op != "sell" {
		return fmt.Errorf("unknown --smart-price-op %q (reset, sell)", op)
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
	if op == "sell" {
		// The step that sells now is the open category of the session.
		tag, err := pool.Exec(ctx, `
			UPDATE session_seats SET status = 'sold'
			WHERE session_id = $1 AND status = 'available'
			  AND tier_id = (SELECT id FROM ticket_tiers
			                  WHERE session_id = $1 AND is_open AND deleted_at IS NULL
			                  ORDER BY price_amount LIMIT 1)`, smartPriceSessionID)
		if err != nil {
			return fmt.Errorf("sell the step: %w", err)
		}
		sweep := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
		if err := sweep(ctx, nil); err != nil {
			return fmt.Errorf("chain sweep: %w", err)
		}
		return printOrgDataJSON(map[string]string{"ok": "sold", "sold": fmt.Sprint(tag.RowsAffected())})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applySmartPriceFixture(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return printOrgDataJSON(map[string]string{"ok": "reset"})
}

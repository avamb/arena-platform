package main

// Fixture of the web twin test of task 5-4 (a date's categories: open and
// close, price, sale dates, quantity; apps/event-center/tests/smoke/live/categories.spec.ts).
//
// One extra event of the fixture organization, "E2E Categories Show", with ONE
// general-admission date and five categories made the way production makes
// them (a ticket_tiers row, then gaquota.CreateCategory mints its places):
//
//	E2E Cat Standing  fixed 25.00 EUR, 20 places, 3 of them sold  (everything allowed)
//	E2E Cat Balcony   fixed base 35.00 EUR, 10 places, a price window in force now
//	                  (40.00) and a later one (50.00): a price change moves the window
//	E2E Cat Early     fixed 10.00 EUR, 5 places, chained to Late: only the price is allowed
//	E2E Cat Late      fixed 15.00 EUR, 5 places, receives Early's free places
//	E2E Cat Free      free entry, 5 places: no price
//
// It has its own event so the places other twins use are never touched.
//
// The spec calls two operations through the seed binary, because the test
// process has no SQL driver:
//
//	--categories-op reset    puts the five categories back exactly as above, so a
//	                         repeated run starts clean;
//	--categories-op inspect  prints one JSON object per category name: price,
//	                         open flag, quantity, real places, sold places, the
//	                         sale window and the price windows.
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
	catEventID   = "fe0000e2-0000-7000-8000-00000000e501"
	catSessionID = "fe0000e2-0000-7000-8000-00000000e502"
	catLedgerID  = "fe0000e2-0000-7000-8000-00000000e504"

	catEventName = "E2E Categories Show"
)

// catTier is one category of the fixture.
type catTier struct {
	ID       string
	Name     string
	Mode     string // pricing_mode
	Price    int64  // minor units, base price
	Places   int32
	Sort     int
	SoldSeed int // places marked sold after minting
}

func catTiers() []catTier {
	return []catTier{
		{ID: "fe0000e2-0000-7000-8000-00000000e511", Name: "E2E Cat Standing", Mode: "fixed", Price: 2500, Places: 20, Sort: 0, SoldSeed: 3},
		{ID: "fe0000e2-0000-7000-8000-00000000e512", Name: "E2E Cat Balcony", Mode: "fixed", Price: 3500, Places: 10, Sort: 1},
		{ID: "fe0000e2-0000-7000-8000-00000000e513", Name: "E2E Cat Early", Mode: "fixed", Price: 1000, Places: 5, Sort: 2},
		{ID: "fe0000e2-0000-7000-8000-00000000e514", Name: "E2E Cat Late", Mode: "fixed", Price: 1500, Places: 5, Sort: 3},
		{ID: "fe0000e2-0000-7000-8000-00000000e515", Name: "E2E Cat Free", Mode: "free", Price: 0, Places: 5, Sort: 4},
	}
}

// applyCategoriesFixture is idempotent AND resetting: the date returns to the
// five categories above, untouched.
func applyCategoriesFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("categories fixture %s: %w", what, err)
		}
		return nil
	}
	tiers := catTiers()
	var total int32
	for _, t := range tiers {
		total += t.Places
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, catEventID, orgID, catEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 40).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, catSessionID, catEventID, venueID, start, total); err != nil {
		return err
	}
	// Everything an earlier run changed, children first. The chain and the
	// price windows go with their categories (ON DELETE CASCADE).
	for _, step := range []struct{ what, sql string }{
		{"places", `DELETE FROM session_seats WHERE session_id = $1`},
		{"ledger", `DELETE FROM inventory_ledger WHERE session_id = $1`},
		{"categories", `DELETE FROM ticket_tiers WHERE session_id = $1`},
	} {
		if err := run(step.what, step.sql, catSessionID); err != nil {
			return err
		}
	}
	if err := run("ledger row", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
		VALUES ($1, $2, NULL, $3)`, catLedgerID, catSessionID, total); err != nil {
		return err
	}
	sid, _ := uuid.Parse(catSessionID)
	for _, t := range tiers {
		if err := run("category "+t.Name, `
			INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
			VALUES ($1, $2, $3, $4, $5, 'EUR', $6, $7)`, t.ID, catSessionID, t.Name, t.Mode, t.Price, t.Places, t.Sort); err != nil {
			return err
		}
		tid, _ := uuid.Parse(t.ID)
		if err := gaquota.CreateCategory(ctx, gen.New(tx), sid, tid, t.Places); err != nil {
			return fmt.Errorf("categories fixture places of %s: %w", t.Name, err)
		}
		if t.SoldSeed > 0 {
			if err := run("sold places of "+t.Name, `
				UPDATE session_seats SET status = 'sold' WHERE id IN
				  (SELECT id FROM session_seats WHERE session_id = $1 AND tier_id = $2 AND kind = 'ga_unit' ORDER BY seat_key LIMIT $3)`,
				catSessionID, t.ID, t.SoldSeed); err != nil {
				return err
			}
		}
	}
	// Early hands its free places to Late.
	if err := run("chain", `INSERT INTO ticket_tier_chain (tier_id, next_tier_id) VALUES ($1, $2)`, tiers[2].ID, tiers[3].ID); err != nil {
		return err
	}
	// Balcony follows a schedule: one window is in force now, one starts later.
	return run("balcony schedule", `
		INSERT INTO ticket_tier_prices (tier_id, valid_from, valid_to, price_amount) VALUES
		  ($1, now() - interval '1 hour', now() + interval '48 hours', 4000),
		  ($1, now() + interval '48 hours', NULL, 5000)`, tiers[1].ID)
}

// catInspect is one category as the spec reads it back.
type catInspect struct {
	PriceAmount     int64      `json:"price_amount"`
	IsOpen          bool       `json:"is_open"`
	Capacity        *int32     `json:"capacity"`
	Places          int        `json:"places"`
	Sold            int        `json:"sold"`
	SaleWindowStart *time.Time `json:"sale_window_start"`
	SaleWindowEnd   *time.Time `json:"sale_window_end"`
	WindowPrices    []int64    `json:"window_prices"`
}

// runCategoriesOp serves --categories-op. It prints one JSON line.
func runCategoriesOp(op string) error {
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
		if err := applyCategoriesFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]string{"ok": "reset"})
	case "inspect":
		out := map[string]catInspect{}
		for _, t := range catTiers() {
			var c catInspect
			var name string
			if err := pool.QueryRow(ctx, `
				SELECT name, price_amount, is_open, capacity, sale_window_start, sale_window_end,
				       (SELECT count(*) FROM session_seats WHERE session_id = $2 AND tier_id = $1 AND kind = 'ga_unit'),
				       (SELECT count(*) FROM session_seats WHERE session_id = $2 AND tier_id = $1 AND kind = 'ga_unit' AND status = 'sold')
				  FROM ticket_tiers WHERE id = $1 AND deleted_at IS NULL`, t.ID, catSessionID).
				Scan(&name, &c.PriceAmount, &c.IsOpen, &c.Capacity, &c.SaleWindowStart, &c.SaleWindowEnd, &c.Places, &c.Sold); err != nil {
				return fmt.Errorf("inspect %s: %w", t.Name, err)
			}
			rows, err := pool.Query(ctx, `SELECT price_amount FROM ticket_tier_prices WHERE tier_id = $1 ORDER BY valid_from`, t.ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var p int64
				if err := rows.Scan(&p); err != nil {
					rows.Close()
					return err
				}
				c.WindowPrices = append(c.WindowPrices, p)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			out[name] = c
		}
		return printCancelOrderJSON(out)
	default:
		return fmt.Errorf("unknown --categories-op %q (reset, inspect)", op)
	}
}

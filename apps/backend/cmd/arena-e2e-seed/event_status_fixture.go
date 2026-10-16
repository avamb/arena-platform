package main

// Fixture of the web twin test of task 5-1 (the status block of the event
// card, apps/event-center/tests/smoke/live/event-status.spec.ts).
//
// Three events of the fixture organization, each with one date and one priced
// category (the publish gate needs both):
//
//	E2E Status Toggle  published, nothing sold. The owner and the manager take
//	                   it off sale and publish it again.
//	E2E Status Delete  published, nothing sold. It is deleted with the typed word.
//	E2E Status Sold    published, one paid order and one ticket. A delete is
//	                   refused with the numbers, so it is archived instead.
//
// Their dates are far in the future (+70 days and later), so the main event of
// the fixture stays the FIRST row of the events list, which other specs rely on.
//
// Two operations the spec calls through the seed binary, because the test
// process has no SQL driver:
//
//	--event-status-op reset    puts the three events back: status published,
//	                           not deleted (an archive is final through the API,
//	                           a delete is soft, so the fixture undoes both);
//	--event-status-op inspect  prints status and deleted flag of each event.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	statusToggleName = "E2E Status Toggle"
	statusDeleteName = "E2E Status Delete"
	statusSoldName   = "E2E Status Sold"

	statusChannelID    = "fe0000e7-0000-7000-8000-000000000001"
	statusBuyerEmail   = "status-buyer@example.test"
	statusSoldOrderID  = "fe0000e7-0000-7000-8000-000000000051"
	statusSoldResID    = "fe0000e7-0000-7000-8000-000000000052"
	statusSoldCheckout = "fe0000e7-0000-7000-8000-000000000053"
	statusSoldTicketID = "fe0000e7-0000-7000-8000-000000000054"
	statusSoldItemID   = "fe0000e7-0000-7000-8000-000000000055"
)

// statusEvent describes one event of the fixture.
type statusEvent struct {
	name, eventID, sessionID, tierID string
	daysAhead                        int
}

func statusEvents() []statusEvent {
	return []statusEvent{
		{statusToggleName, "fe0000e7-0000-7000-8000-000000000011", "fe0000e7-0000-7000-8000-000000000012", "fe0000e7-0000-7000-8000-000000000013", 70},
		{statusDeleteName, "fe0000e7-0000-7000-8000-000000000021", "fe0000e7-0000-7000-8000-000000000022", "fe0000e7-0000-7000-8000-000000000023", 71},
		{statusSoldName, "fe0000e7-0000-7000-8000-000000000031", "fe0000e7-0000-7000-8000-000000000032", "fe0000e7-0000-7000-8000-000000000033", 72},
	}
}

func statusEventIDs() []string {
	var ids []string
	for _, e := range statusEvents() {
		ids = append(ids, e.eventID)
	}
	return ids
}

// applyEventStatusFixture is idempotent AND resetting.
func applyEventStatusFixture(ctx context.Context, tx pgx.Tx) error {
	step := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("event status fixture %s: %w", what, err)
		}
		return nil
	}
	if err := step("channel", `
		INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, 'E2E Status Channel')
		ON CONFLICT (id) DO NOTHING`, statusChannelID, orgID); err != nil {
		return err
	}
	for _, e := range statusEvents() {
		if err := step("event "+e.name, `
			INSERT INTO events (id, org_id, name, status, visibility)
			VALUES ($1, $2, $3, 'published', 'public')
			ON CONFLICT (id) DO NOTHING`, e.eventID, orgID, e.name); err != nil {
			return err
		}
		start := time.Now().UTC().AddDate(0, 0, e.daysAhead).Truncate(time.Hour)
		if err := step("session of "+e.name, `
			INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
			VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 30, 'scheduled', 'EUR', 'override')
			ON CONFLICT (id) DO NOTHING`, e.sessionID, e.eventID, venueID, start); err != nil {
			return err
		}
		if err := step("tier of "+e.name, `
			INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
			VALUES ($1, $2, 'E2E Status Standing', 'fixed', 2500, 'EUR', 30, 0)
			ON CONFLICT (id) DO NOTHING`, e.tierID, e.sessionID); err != nil {
			return err
		}
	}
	// One paid order with one ticket on the "Sold" event.
	sold := statusEvents()[2]
	if err := step("reservation", `
		INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())
		ON CONFLICT (id) DO NOTHING`, statusSoldResID, orgID, statusChannelID, sold.sessionID); err != nil {
		return err
	}
	if err := step("checkout", `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'completed')
		ON CONFLICT (id) DO NOTHING`, statusSoldCheckout, orgID, statusChannelID, statusSoldResID); err != nil {
		return err
	}
	if err := step("order", `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'Status Buyer', $8, now())
		ON CONFLICT (id) DO NOTHING`,
		statusSoldOrderID, orgID, statusChannelID, sold.eventID, sold.sessionID, statusSoldCheckout, statusSoldResID, statusBuyerEmail); err != nil {
		return err
	}
	if err := step("ticket", `
		INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		VALUES ($1, $2, $3, $4, $5, $6, 0)
		ON CONFLICT (id) DO NOTHING`, statusSoldTicketID, statusSoldCheckout, sold.sessionID, sold.tierID, statusBuyerEmail, statusSoldOrderID); err != nil {
		return err
	}
	if err := step("order item", `
		INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		VALUES ($1, $2, 0, 'ticket', $3, $4, 2500, 0, 0, 2500)
		ON CONFLICT (id) DO NOTHING`, statusSoldItemID, statusSoldOrderID, sold.tierID, statusSoldTicketID); err != nil {
		return err
	}
	// The reset: every run starts from a published, not deleted event.
	return step("reset", `
		UPDATE events SET status = 'published', deleted_at = NULL, updated_at = now()
		WHERE id = ANY($1::uuid[])`, statusEventIDs())
}

// runEventStatusOp serves --event-status-op. It prints one JSON line.
func runEventStatusOp(op string) error {
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
		if err := applyEventStatusFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printEventStatusJSON(map[string]string{"ok": "reset"})
	case "inspect":
		type row struct {
			Status  string `json:"status"`
			Deleted bool   `json:"deleted"`
		}
		out := map[string]row{}
		for _, e := range statusEvents() {
			var r row
			if err := pool.QueryRow(ctx, `SELECT status, deleted_at IS NOT NULL FROM events WHERE id = $1`, e.eventID).Scan(&r.Status, &r.Deleted); err != nil {
				return fmt.Errorf("inspect %s: %w", e.name, err)
			}
			out[e.name] = r
		}
		return printEventStatusJSON(out)
	default:
		return fmt.Errorf("unknown --event-status-op %q (reset, inspect)", op)
	}
}

func printEventStatusJSON(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(body, '\n'))
	return err
}

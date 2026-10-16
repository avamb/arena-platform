package main

// Fixture of the web twin test of task 8-1 (refund an order from the order
// card, apps/event-center/tests/smoke/live/refund.spec.ts).
//
// One extra event of the fixture organization, "E2E Refund Show", with its
// own date and category, so the places and orders other twins use are never
// touched. Three paid orders with FIXED ids:
//
//   - the owner's order: three tickets of 25 EUR, paid through Stripe; the
//     third ticket was already scanned at the entrance, so it is never
//     refundable (refunds/scanned.go);
//   - the manager's order: one ticket of 25 EUR, paid through Stripe;
//   - the site order: one ticket sold through a seller's own site
//     (orders.source = bil24_gateway), so arena only explains where the money
//     goes back.
//
// The organization gets a Stripe configuration in test mode. Its key is a
// placeholder: the live harness points arena-api at its own Stripe stub
// (STRIPE_API_BASE_URL), never at Stripe. The configuration stays
// "unverified", so the 4-7 screen still says payments are not connected.
//
// The spec calls two operations through the seed binary, because the test
// process has no SQL driver:
//
//	--refund-op reset    removes every refund of the three orders and puts the
//	                     orders, tickets and the ledger back, so a repeated run
//	                     starts clean (refunds cannot be undone otherwise);
//	--refund-op inspect  prints, per order, {refunds, states, amounts,
//	                     active_tickets, status, notify_buyer};
//	--refund-op scan     marks the manager's ticket scanned at the entrance,
//	                     the way a scan landing while the refund screen is
//	                     open would (the refund.ticket_scanned case).
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	refEventID   = "fe0000e2-0000-7000-8000-000000008101"
	refSessionID = "fe0000e2-0000-7000-8000-000000008102"
	refTierID    = "fe0000e2-0000-7000-8000-000000008103"
	refLedgerID  = "fe0000e2-0000-7000-8000-000000008104"
	refChannelID = "fe0000e2-0000-7000-8000-000000008105"
	refConfigID  = "fe0000e2-0000-7000-8000-000000008106"

	refEventName = "E2E Refund Show"
	refTierName  = "E2E Refund Standing"
	refPrice     = 2500

	// Orders of the 8-1 twin; the spec repeats them.
	refOrderOwner   = "fe0000e2-0000-7000-8000-000000008111"
	refOrderManager = "fe0000e2-0000-7000-8000-000000008112"
	refOrderSite    = "fe0000e2-0000-7000-8000-000000008113"
)

// refundOrder is one paid order of the fixture.
type refundOrder struct {
	id, reservation, checkout, payment, source, buyer, email, chargeRef string
	// tickets are the ids of the order's tickets; scanned names the ones that
	// were already scanned at the entrance.
	tickets []string
	scanned map[string]bool
}

func refundOrders() []refundOrder {
	return []refundOrder{
		{
			id: refOrderOwner, reservation: "fe0000e2-0000-7000-8000-000000008131", checkout: "fe0000e2-0000-7000-8000-000000008141",
			payment: "fe0000e2-0000-7000-8000-000000008151", source: "public_feed", buyer: "Refund Owner Buyer",
			email: "refund-owner-buyer@example.test", chargeRef: "pi_e2e_refund_owner",
			tickets: []string{"fe0000e2-0000-7000-8000-000000008121", "fe0000e2-0000-7000-8000-000000008122", "fe0000e2-0000-7000-8000-000000008123"},
			scanned: map[string]bool{"fe0000e2-0000-7000-8000-000000008123": true},
		},
		{
			id: refOrderManager, reservation: "fe0000e2-0000-7000-8000-000000008132", checkout: "fe0000e2-0000-7000-8000-000000008142",
			payment: "fe0000e2-0000-7000-8000-000000008152", source: "public_feed", buyer: "Refund Manager Buyer",
			email: "refund-manager-buyer@example.test", chargeRef: "pi_e2e_refund_manager",
			tickets: []string{"fe0000e2-0000-7000-8000-000000008124"},
		},
		{
			id: refOrderSite, reservation: "fe0000e2-0000-7000-8000-000000008133", checkout: "fe0000e2-0000-7000-8000-000000008143",
			source: "bil24_gateway", buyer: "Refund Site Buyer", email: "refund-site-buyer@example.test",
			tickets: []string{"fe0000e2-0000-7000-8000-000000008125"},
		},
	}
}

func refundOrderIDs() []string { return []string{refOrderOwner, refOrderManager, refOrderSite} }

func refundTicketIDs() []string {
	var ids []string
	for _, o := range refundOrders() {
		ids = append(ids, o.tickets...)
	}
	return ids
}

// applyRefundFixture is idempotent AND resetting: every refund of the three
// orders is removed and the orders, tickets and the ledger are written again.
func applyRefundFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("refund fixture %s: %w", what, err)
		}
		return nil
	}
	orders, tickets := refundOrderIDs(), refundTicketIDs()

	// What a refund of an earlier run left, children first.
	for _, step := range []struct{ what, sql string }{
		{"ticket refund links", `UPDATE tickets SET refund_id = NULL WHERE id = ANY($2::uuid[])`},
		{"buyer letters", `DELETE FROM worker_jobs WHERE payload->>'batch_id' IN
			(SELECT id::text FROM refund_batches WHERE order_id = ANY($1::uuid[]))`},
		{"ticket jobs", `DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($2::text[])`},
		{"scan exceptions", `DELETE FROM refund_scan_exceptions WHERE ticket_id = ANY($2::uuid[])`},
		{"scan blocks", `DELETE FROM refund_scan_blocks WHERE ticket_id = ANY($2::uuid[])`},
		{"refund events", `DELETE FROM refund_events WHERE refund_id IN
			(SELECT id FROM refunds WHERE order_id = ANY($1::uuid[]) OR ticket_id = ANY($2::uuid[]) OR cancelled_ticket_id = ANY($2::uuid[]))`},
		{"refunds", `DELETE FROM refunds WHERE order_id = ANY($1::uuid[]) OR ticket_id = ANY($2::uuid[]) OR cancelled_ticket_id = ANY($2::uuid[])`},
		{"refund batches", `DELETE FROM refund_batches WHERE order_id = ANY($1::uuid[])`},
		{"outbox", `DELETE FROM outbox_events WHERE aggregate_id = ANY($2::text[]) OR aggregate_id = ANY($1::text[])`},
		{"order events", `DELETE FROM order_events WHERE order_id = ANY($1::uuid[])`},
		{"order items", `DELETE FROM order_items WHERE order_id = ANY($1::uuid[])`},
		{"barcodes", `DELETE FROM barcodes WHERE ticket_id = ANY($2::uuid[])`},
		{"scans", `DELETE FROM scan_events WHERE ticket_id = ANY($2::uuid[])`},
		{"tickets", `DELETE FROM tickets WHERE id = ANY($2::uuid[])`},
	} {
		// Each statement gets exactly the parameters it names.
		var args []any
		if strings.Contains(step.sql, "$1") {
			args = append(args, orders)
		}
		if strings.Contains(step.sql, "$2") {
			if len(args) == 0 {
				step.sql = strings.ReplaceAll(step.sql, "$2", "$1")
			}
			args = append(args, tickets)
		}
		if err := run(step.what, step.sql, args...); err != nil {
			return err
		}
	}

	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, refEventID, orgID, refEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 40).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 20, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, refSessionID, refEventID, venueID, start); err != nil {
		return err
	}
	if err := run("category", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
		VALUES ($1, $2, $3, 'fixed', $4, 'EUR', 20, 0)
		ON CONFLICT (id) DO NOTHING`, refTierID, refSessionID, refTierName, refPrice); err != nil {
		return err
	}
	// A refunded ticket gives its place back to its category's ledger row.
	if err := run("ledger", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total, capacity_sold)
		VALUES ($1, $2, $3, 20, $4)
		ON CONFLICT (id) DO UPDATE SET capacity_sold = EXCLUDED.capacity_sold`,
		refLedgerID, refSessionID, refTierID, len(tickets)); err != nil {
		return err
	}
	if err := run("channel", `
		INSERT INTO sales_channels (id, org_id, name, settings)
		VALUES ($1, $2, 'E2E Refund Channel', '{}'::jsonb)
		ON CONFLICT (id) DO NOTHING`, refChannelID, orgID); err != nil {
		return err
	}
	// The organization's Stripe configuration (test mode, placeholder key; the
	// harness's stub answers for Stripe).
	if err := run("payment config", `
		INSERT INTO payment_provider_configs (id, org_id, provider, mode, secrets, status, is_active)
		VALUES ($1, $2, 'stripe', 'test', '{"api_key":"sk_test_e2e_refund_stub","webhook_secret":"whsec_e2e_refund_stub"}'::jsonb, 'configured', true)
		ON CONFLICT (id) DO NOTHING`, refConfigID, orgID); err != nil {
		return err
	}

	for _, o := range refundOrders() {
		total := refPrice * len(o.tickets)
		if err := run("reservation", `
			INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
			VALUES ($1, $2, $3, $4, $5, 'converted', now() + interval '1 hour', now())
			ON CONFLICT (id) DO NOTHING`, o.reservation, orgID, refChannelID, refSessionID, len(o.tickets)); err != nil {
			return err
		}
		if err := run("checkout", `
			INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed')
			ON CONFLICT (id) DO NOTHING`, o.checkout, orgID, refChannelID, o.reservation); err != nil {
			return err
		}
		if err := run("order", `
			INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'paid', 'EUR', $9, 0, 0, $9, $10, $11, now())
			ON CONFLICT (id) DO UPDATE SET status = 'paid', paid_at = now(), cancelled_at = NULL`,
			o.id, orgID, refChannelID, refEventID, refSessionID, o.checkout, o.reservation, o.source, total, o.buyer, o.email); err != nil {
			return err
		}
		if o.payment != "" {
			if err := run("payment", `
				INSERT INTO payment_intents (id, checkout_session_id, org_id, provider, provider_payment_id, provider_charge_ref,
				                             amount, currency, state, succeeded_at)
				VALUES ($1, $2, $3, 'stripe', $4, $5, $6, 'EUR', 'succeeded', now())
				ON CONFLICT (id) DO NOTHING`,
				o.payment, o.checkout, orgID, "cs_test_e2e_"+o.chargeRef, o.chargeRef, total); err != nil {
				return err
			}
		}
		for i, tk := range o.tickets {
			var usedAt any
			if o.scanned[tk] {
				usedAt = time.Now().UTC().Add(-time.Hour)
			}
			if err := run("ticket", `
				INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, used_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				tk, o.checkout, refSessionID, refTierID, o.email, o.id, i, usedAt); err != nil {
				return err
			}
			if err := run("order item", `
				INSERT INTO order_items (order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
				VALUES ($1, $2, 'ticket', $3, $4, $5, 0, 0, $5)`,
				o.id, i, refTierID, tk, refPrice); err != nil {
				return err
			}
		}
	}
	return nil
}

// runRefundOp serves --refund-op. It prints one JSON line.
func runRefundOp(op string) error {
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
		if err := applyRefundFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]string{"ok": "reset"})
	case "inspect":
		type orderState struct {
			Refunds       int      `json:"refunds"`
			States        []string `json:"states"`
			Amounts       []int64  `json:"amounts"`
			ActiveTickets int      `json:"active_tickets"`
			Status        string   `json:"status"`
			NotifyBuyer   []bool   `json:"notify_buyer"`
		}
		out := map[string]orderState{}
		for _, id := range refundOrderIDs() {
			var s orderState
			if err := pool.QueryRow(ctx, `
				SELECT count(r.id), COALESCE(array_agg(r.state ORDER BY r.created_at) FILTER (WHERE r.id IS NOT NULL), '{}'),
				       COALESCE(array_agg(r.amount ORDER BY r.created_at) FILTER (WHERE r.id IS NOT NULL), '{}')
				FROM refunds r WHERE r.order_id = $1`, id).Scan(&s.Refunds, &s.States, &s.Amounts); err != nil {
				return fmt.Errorf("inspect refunds: %w", err)
			}
			if err := pool.QueryRow(ctx, `
				SELECT COALESCE(array_agg(b.notify_buyer ORDER BY b.created_at), '{}') FROM refund_batches b WHERE b.order_id = $1`,
				id).Scan(&s.NotifyBuyer); err != nil {
				return fmt.Errorf("inspect batches: %w", err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE order_id = $1 AND status = 'active'`, id).Scan(&s.ActiveTickets); err != nil {
				return fmt.Errorf("inspect tickets: %w", err)
			}
			if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, id).Scan(&s.Status); err != nil {
				return fmt.Errorf("inspect order: %w", err)
			}
			out[id] = s
		}
		return printCancelOrderJSON(out)
	case "scan":
		// The manager's ticket is scanned at the entrance while the refund
		// screen is open: the create call must refuse it (refund.ticket_scanned).
		if _, err := pool.Exec(ctx, `UPDATE tickets SET used_at = now() WHERE order_id = $1`, refOrderManager); err != nil {
			return fmt.Errorf("scan the manager's ticket: %w", err)
		}
		return printCancelOrderJSON(map[string]string{"ok": "scanned"})
	default:
		return fmt.Errorf("unknown --refund-op %q (reset, inspect, scan)", op)
	}
}

//go:build integration

package gen_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestSessionSummary_LiveDB runs the six hand-written session-summary
// wrappers against a real schema: a unit test cannot catch a column-count or
// type mismatch in a hand-maintained scanner. The fixture is one session with
// a seated category (one seat sold here, one sold upstream, one withheld, one
// free), one paid order and one external refund.
func TestSessionSummary_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 4))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var (
		orgID, venueID, evtID, sessID = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		chanID, resvID, csID, orderID = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		tierID, tktID, itemID, refID  = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		nonce                         = orgID.String()[:8]
	)

	cleanup := func() {
		// tickets.refund_id and refunds.ticket_id point at each other.
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`UPDATE tickets SET refund_id = NULL WHERE id = $1`, tktID},
			{`DELETE FROM refunds WHERE id = $1`, refID},
			{`DELETE FROM order_items WHERE id = $1`, itemID},
			{`DELETE FROM tickets WHERE id = $1`, tktID},
			{`DELETE FROM orders WHERE id = $1`, orderID},
			{`DELETE FROM session_seats WHERE session_id = $1`, sessID},
			{`DELETE FROM checkout_sessions WHERE id = $1`, csID},
			{`DELETE FROM reservations WHERE id = $1`, resvID},
			{`DELETE FROM ticket_tiers WHERE id = $1`, tierID},
			{`DELETE FROM sales_channels WHERE id = $1`, chanID},
			{`DELETE FROM sessions WHERE id = $1`, sessID},
			{`DELETE FROM events WHERE id = $1`, evtID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM organizations WHERE id = $1`, orgID},
		} {
			if _, err := pool.Exec(context.Background(), step.sql, step.arg); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	}
	defer cleanup()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Summary Org "+nonce, "summary-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Summary Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Summary Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'CZK', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Channel "+nonce)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, 'Balcony', 'fixed', 50000, 'CZK')`, tierID, sessID)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resvID, orgID, chanID, sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)

	seat := func(key, status string, reservation *uuid.UUID) {
		exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, reservation_id)
		      VALUES ($1, $2, 'Balcony', '1', $2, $3, $4, $5)`, sessID, key, tierID, status, reservation)
	}
	seat("1", "sold", &resvID) // sold here
	seat("2", "sold", nil)     // sold upstream
	seat("3", "unavailable", nil)
	seat("4", "available", nil)

	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', 'paid', 'CZK', 50000, 1000, 2000, 51000, 'x', 'x@example.test')`,
		orderID, orgID, chanID, evtID, sessID, csID, resvID)
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id)
	      VALUES ($1, $2, $3, $4, 'x@example.test', $5)`, tktID, csID, sessID, tierID, orderID)
	exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
	      VALUES ($1, $2, 1, 'ticket', $3, $4, 50000, 1000, 2000, 51000)`, itemID, orderID, tierID, tktID)
	exec(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, succeeded_at)
	      VALUES ($1, $2, 51000, 'CZK', 'succeeded', 'external', $3, $4, now())`, refID, orgID, orderID, tktID)

	q := gen.New(pool)

	header, err := q.GetSessionSummaryHeader(ctx, sessID, orgID)
	if err != nil {
		t.Fatalf("GetSessionSummaryHeader: %v", err)
	}
	if header.EventID != evtID || header.CapacityTotal != 100 || header.SeatingPlanVersionID != nil {
		t.Errorf("header: %+v", header)
	}
	if header.VenueTimezone == nil || *header.VenueTimezone != "Europe/Prague" {
		t.Errorf("venue timezone: %v", header.VenueTimezone)
	}
	// Another organization must not see the session at all.
	if _, err := q.GetSessionSummaryHeader(ctx, sessID, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("foreign org: got %v, want pgx.ErrNoRows", err)
	}

	places, err := q.ListSessionSummaryPlaces(ctx, sessID)
	if err != nil {
		t.Fatalf("ListSessionSummaryPlaces: %v", err)
	}
	if len(places) != 1 {
		t.Fatalf("places rows: %+v", places)
	}
	p := places[0]
	if p.TierID == nil || *p.TierID != tierID || p.Kind != "seat" ||
		p.Available != 1 || p.Held != 0 || p.Sold != 2 || p.SoldUpstream != 1 || p.Unavailable != 1 {
		t.Errorf("places: %+v", p)
	}

	tiers, err := q.ListSessionSummaryTiers(ctx, sessID)
	if err != nil {
		t.Fatalf("ListSessionSummaryTiers: %v", err)
	}
	if len(tiers) != 1 || tiers[0].ID != tierID || tiers[0].PaidItems != 1 || tiers[0].PaidRevenue != 51000 {
		t.Errorf("tiers: %+v", tiers)
	}

	orders, err := q.ListSessionSummaryOrders(ctx, sessID)
	if err != nil {
		t.Fatalf("ListSessionSummaryOrders: %v", err)
	}
	if len(orders) != 1 || orders[0].Status != "paid" || orders[0].Orders != 1 ||
		orders[0].Total != 51000 || orders[0].Charge != 2000 || orders[0].Discount != 1000 {
		t.Errorf("orders: %+v", orders)
	}

	tickets, err := q.GetSessionSummaryTickets(ctx, sessID)
	if err != nil {
		t.Fatalf("GetSessionSummaryTickets: %v", err)
	}
	if tickets.Active != 1 || tickets.Cancelled != 0 || tickets.Used != 0 {
		t.Errorf("tickets: %+v", tickets)
	}

	refunds, err := q.ListSessionSummaryRefunds(ctx, sessID)
	if err != nil {
		t.Fatalf("ListSessionSummaryRefunds: %v", err)
	}
	if len(refunds) != 1 || refunds[0].Settlement != "external" || refunds[0].State != "succeeded" ||
		refunds[0].Refunds != 1 || refunds[0].Amount != 51000 {
		t.Errorf("refunds: %+v", refunds)
	}
}

//go:build integration

package conversions

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPGStoreOrder_SessionsCarryEventCityCountry reads the real projection:
// order_items -> sessions -> events/venues -> cities (English name) -> countries.
func TestPGStoreOrder_SessionsCarryEventCityCountry(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var (
		orgID, venueID, evtID, sessID, chanID = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		resvID, csID, orderID, tierID         = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		nonce                                 = orgID.String()[:8]
	)
	defer func() {
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM order_items WHERE order_id = $1`, orderID},
			{`DELETE FROM orders WHERE org_id = $1`, orgID},
			{`DELETE FROM ticket_tiers WHERE id = $1`, tierID},
			{`DELETE FROM checkout_sessions WHERE org_id = $1`, orgID},
			{`DELETE FROM reservations WHERE org_id = $1`, orgID},
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
	}()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Sess Org "+nonce, "sess-"+nonce)
	// 0006_geo seeds the tallinn city (and its English i18n name) in EE.
	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone)
	      VALUES ($1, $2, $3, (SELECT id FROM cities WHERE slug = 'tallinn'), 'Europe/Tallinn')`,
		venueID, orgID, "Sess Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Sess Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, '2099-12-19T18:00:00Z', '2099-12-19T20:00:00Z', 100, 'EUR', 'override')`, sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Channel "+nonce)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, resvID, orgID, chanID, sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 3780, 0, 0, 3780, 'B', 'b@example.test')`,
		orderID, orgID, chanID, evtID, sessID, csID, resvID)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, 'GA', 'fixed', 1890, 'EUR')`, tierID, sessID)
	for i := 1; i <= 2; i++ {
		exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, unit_price, discount, charge, total)
		      VALUES ($1, $2, $3, 'ticket', $4, 1890, 0, 0, 1890)`, uuid.New(), orderID, i, tierID)
	}

	od, err := NewPGStore(pool).Order(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(od.Sessions) != 1 {
		t.Fatalf("sessions = %+v", od.Sessions)
	}
	s := od.Sessions[0]
	if s.SessionID != sessID.String() || s.EventID != evtID.String() || s.EventName != "Sess Event "+nonce ||
		s.Venue != "Sess Hall "+nonce || s.Country != "EE" || s.Quantity != 2 || s.UnitPrice != 1890 ||
		s.City == "" || s.StartAt.Format("2006-01-02") != "2099-12-19" {
		t.Fatalf("session = %+v", s)
	}
}

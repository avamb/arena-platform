//go:build integration

package gen_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestPromoCodes_TestOrdersDoNotUseUpTheLimits proves an order of a test
// sales channel (P-13, orders.is_test) never counts against max_uses or
// max_uses_per_customer, while its redemption row is still written.
func TestPromoCodes_TestOrdersDoNotUseUpTheLimits(t *testing.T) {
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
		chanID                        = uuid.New()
		resv1, cs1, order1            = uuid.New(), uuid.New(), uuid.New()
		resv2, cs2, order2            = uuid.New(), uuid.New(), uuid.New()
		nonce                         = orgID.String()[:8]
		email                         = "promo-test-" + nonce + "@example.test"
	)
	defer func() {
		for _, s := range []string{
			`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM promo_codes WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), s, orgID); err != nil {
				t.Logf("cleanup %q: %v", s, err)
			}
		}
	}()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Promo Test Org "+nonce, "promo-test-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'CZK', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider, is_test)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe', true)`, chanID, orgID, "Test site "+nonce)

	q := gen.New(pool)
	code, err := q.InsertPromoCode(ctx, orgID, "TEST10", "percent", 10,
		nil, nil, "CZK", i32p(1), i32p(1), nil, nil, 0, "")
	if err != nil {
		t.Fatalf("InsertPromoCode: %v", err)
	}
	for _, o := range []struct{ resv, cs, order uuid.UUID }{{resv1, cs1, order1}, {resv2, cs2, order2}} {
		exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, o.resv, orgID, chanID, sessID)
		exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		      VALUES ($1, $2, $3, $4, 'completed')`, o.cs, orgID, chanID, o.resv)
		exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                          source, status, currency, subtotal, discount, charge, total, promo_code_id, buyer_name, buyer_email)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', 'paid', 'CZK', 45000, 4500, 0, 40500, $8, 'x', $9)`,
			o.order, orgID, chanID, evtID, sessID, o.cs, o.resv, code.ID, email)
	}
	var isTest bool
	if err := pool.QueryRow(ctx, `SELECT is_test FROM orders WHERE id = $1`, order1).Scan(&isTest); err != nil || !isTest {
		t.Fatalf("the order of a test channel is_test = %v (%v); the trigger must stamp it", isTest, err)
	}
	// order1 is redeemed, order2 is paid with the code and no redemption yet.
	if err := q.InsertPromoCodeRedemption(ctx, code.ID, nil, &resv1, 4500, 45000, &order1, nil, &chanID); err != nil {
		t.Fatalf("InsertPromoCodeRedemption: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM promo_code_redemptions WHERE order_id = $1`, order1).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("redemption rows of the test order = %d (%v); it must still be written", rows, err)
	}

	if n, err := q.CountPromoCodeRedemptions(ctx, code.ID); err != nil || n != 0 {
		t.Errorf("CountPromoCodeRedemptions = %d (%v); a test order must not count", n, err)
	}
	if n, err := q.CountPromoRedemptionsByBuyerEmail(ctx, code.ID, email); err != nil || n != 0 {
		t.Errorf("CountPromoRedemptionsByBuyerEmail = %d (%v); want 0", n, err)
	}
	u, err := q.CountPromoCodeUsesInFlight(ctx, code.ID, orgID, nil, nil, nil, email)
	if err != nil {
		t.Fatalf("CountPromoCodeUsesInFlight: %v", err)
	}
	if u.Used != 0 || u.Mine != 0 {
		t.Errorf("uses in flight = %+v; test orders must not use up max_uses (1)", u)
	}
}

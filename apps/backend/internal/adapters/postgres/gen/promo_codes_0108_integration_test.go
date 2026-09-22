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

// TestPromoCodes0108_LiveDB runs the promo queries migration 0108 changed
// against a real schema: session scope and currency round-trip, a
// redemption is one row per order however often it is written, and the
// usage report joins the order it paid for.
func TestPromoCodes0108_LiveDB(t *testing.T) {
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
		tierID                        = uuid.New()
		nonce                         = orgID.String()[:8]
		email                         = "promo-" + nonce + "@example.test"
	)

	cleanup := func() {
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`, orgID},
			{`DELETE FROM orders WHERE id = $1`, orderID},
			{`DELETE FROM promo_codes WHERE org_id = $1`, orgID},
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
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Promo Org "+nonce, "promo-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Promo Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Promo Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'CZK', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Site "+nonce)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, 'Standard', 'fixed', 45000, 'CZK')`, tierID, sessID)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resvID, orgID, chanID, sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)

	q := gen.New(pool)

	// ── Scope and currency round-trip ────────────────────────────────────────
	scoped, err := q.InsertPromoCode(ctx, orgID, "ARENA10", "percent", 10,
		nil, []string{sessID.String()}, "CZK", i32p(2), i32p(1), nil, nil, 0, "")
	if err != nil {
		t.Fatalf("InsertPromoCode scoped: %v", err)
	}
	if len(scoped.AppliesToSessionIDs) != 1 || scoped.AppliesToSessionIDs[0] != sessID.String() {
		t.Errorf("session scope: %v", scoped.AppliesToSessionIDs)
	}
	if scoped.Currency == nil || *scoped.Currency != "CZK" || scoped.Status != "active" {
		t.Errorf("currency/status: %+v", scoped)
	}
	if len(scoped.AppliesToTierIDs) != 0 {
		t.Errorf("nil tier ids must be stored as an empty array: %v", scoped.AppliesToTierIDs)
	}

	plain, err := q.InsertPromoCode(ctx, orgID, "OLD5", "percent", 5,
		[]string{}, []string{}, "", nil, nil, nil, nil, 0, "active")
	if err != nil {
		t.Fatalf("InsertPromoCode plain: %v", err)
	}
	if plain.Currency != nil {
		t.Errorf("an empty currency must be stored as NULL: %v", *plain.Currency)
	}
	if ci, err := q.GetPromoCodeByCodeCI(ctx, orgID, "arena10"); err != nil || ci.ID != scoped.ID {
		t.Errorf("case-insensitive lookup: %v %v", err, ci.ID)
	}

	cleared := ""
	upd, err := q.UpdatePromoCode(ctx, scoped.ID, orgID, "", nil, nil, nil, &cleared, nil, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("UpdatePromoCode: %v", err)
	}
	if upd.Currency != nil || len(upd.AppliesToSessionIDs) != 1 || upd.MaxUses == nil || *upd.MaxUses != 2 {
		t.Errorf("update must clear the currency and keep everything else: %+v", upd)
	}

	// ── One redemption per order ─────────────────────────────────────────────
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, promo_code_id, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', 'paid', 'CZK', 45000, 4500, 0, 40500, $8, 'x', $9)`,
		orderID, orgID, chanID, evtID, sessID, csID, resvID, scoped.ID, email)
	for i := 0; i < 2; i++ {
		if err := q.InsertPromoCodeRedemption(ctx, scoped.ID, nil, &resvID, 4500, 45000, &orderID, nil, &chanID); err != nil {
			t.Fatalf("InsertPromoCodeRedemption #%d: %v", i+1, err)
		}
	}
	if n, err := q.CountPromoCodeRedemptions(ctx, scoped.ID); err != nil || n != 1 {
		t.Errorf("a replayed redemption must not count twice: n=%d err=%v", n, err)
	}
	if n, err := q.CountPromoRedemptionsByBuyerEmail(ctx, scoped.ID, "PROMO-"+nonce+"@EXAMPLE.TEST"); err != nil || n != 1 {
		t.Errorf("per-buyer count is case-insensitive on the e-mail: n=%d err=%v", n, err)
	}
	if n, err := q.CountPromoRedemptionsByCustomer(ctx, scoped.ID, uuid.New()); err != nil || n != 0 {
		t.Errorf("unknown customer: n=%d err=%v", n, err)
	}

	// ── The report ───────────────────────────────────────────────────────────
	usage, err := q.ListPromoCodeUsageByOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("ListPromoCodeUsageByOrg: %v", err)
	}
	byID := map[uuid.UUID]gen.PromoCodeUsageRow{}
	for _, u := range usage {
		byID[u.PromoCodeID] = u
	}
	if u := byID[scoped.ID]; u.Uses != 1 || u.DiscountTotal != 4500 || u.LastUsedAt == nil {
		t.Errorf("usage of the used code: %+v", u)
	}
	if u, ok := byID[plain.ID]; !ok || u.Uses != 0 || u.LastUsedAt != nil {
		t.Errorf("a never-used code still has a row: %+v (%v)", u, ok)
	}

	report, err := q.ListPromoCodeRedemptionsByOrg(ctx, orgID, nil)
	if err != nil {
		t.Fatalf("ListPromoCodeRedemptionsByOrg: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("report rows: %+v", report)
	}
	r := report[0]
	if r.Code != "ARENA10" || r.OrderNumber == nil || r.OrderStatus == nil || *r.OrderStatus != "paid" ||
		r.BuyerEmail == nil || *r.BuyerEmail != email || r.ChannelName == nil || r.SessionID == nil || *r.SessionID != sessID {
		t.Errorf("report row: %+v", r)
	}
	if only, err := q.ListPromoCodeRedemptionsByOrg(ctx, orgID, &plain.ID); err != nil || len(only) != 0 {
		t.Errorf("filter by an unused code: %v %+v", err, only)
	}

	promos, err := q.ListSessionSummaryPromos(ctx, sessID)
	if err != nil {
		t.Fatalf("ListSessionSummaryPromos: %v", err)
	}
	if len(promos) != 1 || promos[0].Code != "ARENA10" || promos[0].Orders != 1 || promos[0].Discount != 4500 || promos[0].Currency != "CZK" {
		t.Errorf("session promos: %+v", promos)
	}
}

func i32p(n int32) *int32 { return &n }

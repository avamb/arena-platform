//go:build integration

// query_integration_test.go — live-DB proof of the neutral projection
// (W1-B7a, feature #504).
//
// The unit tests in build_test.go pin the arithmetic; this one pins the SQL:
// that the column list still scans, that QueryOrder resolves an order through
// the NEW tickets.order_id link (migration 0092) rather than the checkout
// session, and that QuerySession and QueryOrder agree on the same facts.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/orderexport/
package orderexport_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// testPool opens the stand pool, skipping when the suite is run without a
// database (the Unit CI job).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping orderexport integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q is not a Postgres DSN; skipping", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("testPool: open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("testPool: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestQueryOrderAndSession_LiveProjection seeds one paid order of three
// tickets with a promo discount and asserts both entry points project it
// identically.
func TestQueryOrderAndSession_LiveProjection(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	cityID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	tierID := uuid.New()
	promoID := uuid.New()
	resID := uuid.New()
	csID := uuid.New()
	orderID := uuid.New()
	citySlug := "oe504-city-" + suffix
	promoCode := "OE504" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migrations not applied?): %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM orders WHERE id=$1`, orderID)
		_, _ = pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, csID)
		_, _ = pool.Exec(c, `DELETE FROM reservations WHERE id=$1`, resID)
		_, _ = pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM promo_codes WHERE id=$1`, promoID)
		_, _ = pool.Exec(c, `DELETE FROM ticket_tiers WHERE id=$1`, tierID)
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		_, _ = pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	// ── Geography, org, venue with an explicit timezone ───────────────────
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1,$2,$3)`, cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value)
		VALUES ('geo.cities', $1, 'en', 'OE504 City')`, citySlug)
	mustExec(`INSERT INTO organizations (id, name, legal_name, slug) VALUES ($1,$2,$3,$4)`,
		orgID, "OE504 Org", "OOO OE504 Legal", "oe504-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id, timezone) VALUES ($1,$2,$3,$4,'Europe/Moscow')`,
		venueID, orgID, "OE504 Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1,$2,$3,'draft','private')`, eventID, orgID, "OE504 Event")

	// A fixed session start so the venue-local wall clock is deterministic.
	startAt := time.Date(2027, 3, 14, 17, 0, 0, 0, time.UTC)
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total,
			status, admission_mode, currency, currency_source)
		VALUES ($1,$2,$3,$4,$5,100,'draft','general_admission','RUB','override')`,
		sessionID, eventID, venueID, startAt, startAt.Add(3*time.Hour))
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1,$2,$3)`,
		channelID, orgID, "OE504 Channel")
	mustExec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		VALUES ($1,$2,'OE504 Standard','fixed',1000,'RUB',0)`, tierID, sessionID)
	mustExec(`INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value, status)
		VALUES ($1,$2,$3,'percent',10,'active')`, promoID, orgID, promoCode)

	// ── Paid order: 3 x 1000, discount 100 → 2900 ─────────────────────────
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold)
		VALUES ($1,$2,100,3)`, sessionID, tierID)
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1,$2,$3,$4,3,$5)`, resID, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	completedAt := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, total, currency, promo_code_id, payment_provider, completed_at)
		VALUES ($1,$2,$3,$4,'completed',3000,100,2900,'RUB',$5,'yookassa',$6)`,
		csID, orgID, channelID, resID, promoID, completedAt)
	mustExec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id,
			reservation_id, source, status, currency, subtotal, discount, charge, total, promo_code_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid','RUB',3000,100,0,2900,$8)`,
		orderID, orgID, channelID, eventID, sessionID, csID, resID, promoID)

	ticketIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, tid := range ticketIDs {
		mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, order_id, tier_id,
				status, issued_at, ordinal, holder_email)
			VALUES ($1,$2,$3,$4,$5,'active',NOW(),$6,'buyer@example.com')`,
			tid, sessionID, csID, orderID, tierID, i+1)
	}

	// ── QueryOrder: resolves through tickets.order_id ─────────────────────
	order, err := orderexport.QueryOrder(ctx, pool, orderID)
	if err != nil {
		t.Fatalf("QueryOrder: %v", err)
	}
	if order == nil {
		t.Fatal("QueryOrder returned nil for a paid order with 3 issued tickets")
	}
	if got := len(order.Tickets); got != 3 {
		t.Fatalf("order has %d tickets, want 3", got)
	}
	if order.CheckoutSessionID != csID {
		t.Errorf("CheckoutSessionID = %v, want %v", order.CheckoutSessionID, csID)
	}
	if order.Subtotal != 3000 || order.Discount != 100 || order.Total != 2900 {
		t.Errorf("money = %d/%d/%d, want 3000/100/2900", order.Subtotal, order.Discount, order.Total)
	}
	if order.Currency != "RUB" {
		t.Errorf("Currency = %q, want RUB", order.Currency)
	}
	if order.PaymentProvider != "yookassa" {
		t.Errorf("PaymentProvider = %q, want yookassa", order.PaymentProvider)
	}
	if !order.CompletedAt.UTC().Equal(completedAt) {
		t.Errorf("CompletedAt = %v, want %v", order.CompletedAt.UTC(), completedAt)
	}
	wantReason := "Промокод " + promoCode
	if order.DiscountReason != wantReason {
		t.Errorf("DiscountReason = %q, want %q", order.DiscountReason, wantReason)
	}
	if order.BuyerEmail != "buyer@example.com" {
		t.Errorf("BuyerEmail = %q", order.BuyerEmail)
	}

	// order.ID is the minimum system_ticket_id; every ticket back-references it.
	var minSystemTicketID int64
	if err := pool.QueryRow(ctx,
		`SELECT MIN(system_ticket_id) FROM tickets WHERE order_id=$1`, orderID,
	).Scan(&minSystemTicketID); err != nil {
		t.Fatalf("read min system_ticket_id: %v", err)
	}
	if order.ID != minSystemTicketID {
		t.Errorf("order.ID = %d, want %d (min system_ticket_id)", order.ID, minSystemTicketID)
	}

	// Per-ticket money: prorated discounts sum EXACTLY to the order discount.
	var discountSum, chargeSum int64
	for i, tk := range order.Tickets {
		if tk.OrderID != order.ID {
			t.Errorf("ticket[%d].OrderID = %d, want %d", i, tk.OrderID, order.ID)
		}
		if tk.Price != 1000 {
			t.Errorf("ticket[%d].Price = %d, want 1000 (tier price)", i, tk.Price)
		}
		if tk.TierName != "OE504 Standard" {
			t.Errorf("ticket[%d].TierName = %q", i, tk.TierName)
		}
		if tk.PlatformStatus != "active" {
			t.Errorf("ticket[%d].PlatformStatus = %q, want active", i, tk.PlatformStatus)
		}
		if tk.Seated {
			t.Errorf("ticket[%d].Seated = true, want false (general admission)", i)
		}
		// GA seat ids come from the disjoint 1e9+ range so they can never
		// collide with a real session_seats.system_seat_id.
		if tk.SeatID < 1000000000 {
			t.Errorf("ticket[%d].SeatID = %d, want a synthetic id >= 1e9 for GA", i, tk.SeatID)
		}
		if tk.Charge != tk.Price-tk.Discount || tk.TotalPrice != tk.Charge {
			t.Errorf("ticket[%d] money inconsistent: price=%d discount=%d charge=%d total=%d",
				i, tk.Price, tk.Discount, tk.Charge, tk.TotalPrice)
		}
		if tk.Event.EventName != "OE504 Event" {
			t.Errorf("ticket[%d].Event.EventName = %q", i, tk.Event.EventName)
		}
		if tk.Event.OrgLegalName != "OOO OE504 Legal" {
			t.Errorf("ticket[%d].Event.OrgLegalName = %q, want the legal_name", i, tk.Event.OrgLegalName)
		}
		if tk.Event.CityName != "OE504 City" {
			t.Errorf("ticket[%d].Event.CityName = %q, want the en i18n value", i, tk.Event.CityName)
		}
		// Venue timezone is Europe/Moscow: 17:00 UTC → 20:00 local, no suffix.
		if tk.Event.ShowTimeLocal != "2027-03-14T20:00:00" {
			t.Errorf("ticket[%d].Event.ShowTimeLocal = %q, want venue-local 2027-03-14T20:00:00",
				i, tk.Event.ShowTimeLocal)
		}
		discountSum += tk.Discount
		chargeSum += tk.Charge
	}
	if discountSum != order.Discount {
		t.Errorf("sum of per-ticket discounts = %d, want exactly %d", discountSum, order.Discount)
	}
	if chargeSum != order.Total {
		t.Errorf("sum of per-ticket charges = %d, want the order total %d", chargeSum, order.Total)
	}

	// ── QuerySession must project the same order ──────────────────────────
	sessionOrders, err := orderexport.QuerySession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("QuerySession: %v", err)
	}
	if len(sessionOrders) != 1 {
		t.Fatalf("QuerySession returned %d orders, want 1", len(sessionOrders))
	}
	so := sessionOrders[0]
	if so.ID != order.ID || so.TicketQuantity() != order.TicketQuantity() {
		t.Errorf("QuerySession order = id %d/%d tickets, QueryOrder = id %d/%d tickets",
			so.ID, so.TicketQuantity(), order.ID, order.TicketQuantity())
	}
	if so.DiscountReason != order.DiscountReason || so.Total != order.Total {
		t.Errorf("QuerySession/QueryOrder disagree: %q/%d vs %q/%d",
			so.DiscountReason, so.Total, order.DiscountReason, order.Total)
	}

	// ── QueryTicket returns one ticket plus its order header ──────────────
	tk, hdr, err := orderexport.QueryTicket(ctx, pool, ticketIDs[0])
	if err != nil {
		t.Fatalf("QueryTicket: %v", err)
	}
	if tk == nil || hdr == nil {
		t.Fatal("QueryTicket returned nil for an issued ticket of a completed order")
	}
	if tk.TicketUUID != ticketIDs[0] {
		t.Errorf("QueryTicket ticket = %v, want %v", tk.TicketUUID, ticketIDs[0])
	}
	if hdr.CheckoutSessionID != csID {
		t.Errorf("QueryTicket order header = %v, want %v", hdr.CheckoutSessionID, csID)
	}

	// ── Unknown ids project to nothing, not an error ──────────────────────
	missing, err := orderexport.QueryOrder(ctx, pool, uuid.New())
	if err != nil {
		t.Fatalf("QueryOrder(unknown): %v", err)
	}
	if missing != nil {
		t.Errorf("QueryOrder(unknown) = %+v, want nil", missing)
	}
	mtk, mhdr, err := orderexport.QueryTicket(ctx, pool, uuid.New())
	if err != nil {
		t.Fatalf("QueryTicket(unknown): %v", err)
	}
	if mtk != nil || mhdr != nil {
		t.Error("QueryTicket(unknown) returned a projection, want nils")
	}
}

// TestQueryOrder_UnpaidOrderIsNotExportable proves the projection's paid-only
// filter: an order whose checkout session is still open must not leak onto any
// export wire, even though its tickets rows exist.
func TestQueryOrder_UnpaidOrderIsNotExportable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	resID := uuid.New()
	csID := uuid.New()
	orderID := uuid.New()
	ticketID := uuid.New()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM orders WHERE id=$1`, orderID)
		_, _ = pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, csID)
		_, _ = pool.Exec(c, `DELETE FROM reservations WHERE id=$1`, resID)
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
	})

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1,$2,$3)`,
		orgID, "OE504 Unpaid Org", "oe504-unpaid-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1,$2,$3)`, venueID, orgID, "OE504 Unpaid Venue")
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1,$2,$3,'draft','private')`, eventID, orgID, "OE504 Unpaid Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total,
			status, admission_mode, currency, currency_source)
		VALUES ($1,$2,$3,NOW()+INTERVAL '60 days',NOW()+INTERVAL '60 days 2 hours',
			50,'draft','general_admission','RUB','override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1,$2,$3)`,
		channelID, orgID, "OE504 Unpaid Channel")
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1,$2,$3,$4,1,$5)`, resID, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	// state='payment_started' — the buyer is mid-payment, NOT 'completed'.
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, total, currency)
		VALUES ($1,$2,$3,$4,'payment_started',1000,0,1000,'RUB')`, csID, orgID, channelID, resID)
	mustExec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id,
			reservation_id, source, status, currency, subtotal, discount, charge, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','pending_payment','RUB',1000,0,0,1000)`,
		orderID, orgID, channelID, eventID, sessionID, csID, resID)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, order_id, status)
		VALUES ($1,$2,$3,$4,'active')`, ticketID, sessionID, csID, orderID)

	order, err := orderexport.QueryOrder(ctx, pool, orderID)
	if err != nil {
		t.Fatalf("QueryOrder: %v", err)
	}
	if order != nil {
		t.Errorf("QueryOrder projected an order whose checkout session is not completed: %+v", order)
	}

	orders, err := orderexport.QuerySession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("QuerySession: %v", err)
	}
	if len(orders) != 0 {
		t.Errorf("QuerySession returned %d orders for an uncompleted checkout, want 0", len(orders))
	}
}

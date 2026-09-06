//go:build integration

// order_wiring_w1a6c_488_integration_test.go — live-DB coverage for W1-A6c
// (feature #488, spec §14.1 / §9.1): the order aggregate is minted on the
// public-feed checkout transaction, and ticket issuance backfills the
// ticket↔order links and publishes exactly one v1.order.paid outbox row.
//
// The test drives the REAL endpoint
//
//	POST /v1/public/feeds/{feed_token}/checkout/start
//
// through the mounted chi router (no handler stubs — see AGENTS.md), then
// calls the real htickets issuance path the payment webhook would call.
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0093).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestW1A6c
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// w1a6cFixture seeds the minimal published-feed topology the public checkout
// endpoint validates against: org → venue → event(published) → session
// (scheduled, GA) → tier → ledger → channel → feed token → publication, plus
// materialised GA units for AllocateGAUnitsTx to claim.
type w1a6cFixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	orgID      uuid.UUID
	venueID    uuid.UUID
	eventID    uuid.UUID
	sessionID  uuid.UUID
	tierID     uuid.UUID
	channelID  uuid.UUID
	tokenID    uuid.UUID
	feedToken  string
	buyerMail  string
	buyerPhone string
}

func newW1A6cFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *w1a6cFixture {
	t.Helper()
	f := &w1a6cFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		tierID:    uuid.New(),
		channelID: uuid.New(),
		tokenID:   uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	f.feedToken = "w1a6c-feed-" + suffix
	f.buyerMail = fmt.Sprintf("w1a6c-buyer-%s@arena-integration.test", suffix)
	// The phone is a STRONG identity key in customers.Resolve, so a constant
	// number would fold every run of this test into one customer record (and
	// leave that record undeletable while older orders still reference it).
	f.buyerPhone = fmt.Sprintf("+3630%07d", time.Now().UnixNano()%10_000_000)

	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "W1A6c Org " + suffix, "w1a6c-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "W1A6c Venue " + suffix}},
		// GetPublicCheckoutContext demands e.status = 'published'.
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "W1A6c Event " + suffix}},
		// sessions.status CHECK is ('draft','scheduled','cancelled','completed').
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 2 hours', 100, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO ticket_tiers
		    (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'W1A6c Tier', 'fixed', 2500, 'EUR', 0)`,
			[]any{f.tierID, f.sessionID}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, 100)`,
			[]any{f.sessionID}},
		// fee_percent exercises ordering.ChargePercentBP; the collect_* flags
		// make buyer.name / buyer.phone mandatory, which is what puts them on
		// the order row.
		{`INSERT INTO sales_channels (id, org_id, name, fee_percent, collect_name, collect_phone)
		  VALUES ($1, $2, $3, 1.25, true, true)`,
			[]any{f.channelID, f.orgID, "W1A6c Channel " + suffix}},
		{`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active)
		  VALUES ($1, $2, $3, 'w1a6c', true)`,
			[]any{f.tokenID, f.feedToken, f.channelID}},
		{`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`,
			[]any{f.eventID, f.tokenID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("W1A6c fixture step %d failed: %v", i, err)
		}
	}

	// AB-51: AllocateGAUnitsTx only claims EXISTING available ga_unit rows,
	// so they must be materialised up front or the endpoint answers 409.
	// The tier is nil because this session is plan-less: AllocateGAUnitsForHold
	// filters the pool with `tier_id IS NOT DISTINCT FROM NULL` when the session
	// has no seating_plan_version, and stamps the tier at hold time.
	if _, err := gen.New(pool).InsertGAUnits(ctx, f.sessionID, "ga|pool", 0, nil, 5); err != nil {
		f.cleanup()
		t.Fatalf("W1A6c fixture InsertGAUnits: %v", err)
	}
	return f
}

// cleanup deletes the fixture rows in FK-safe order. Every statement is
// best-effort: a partially built fixture must still tear itself down.
func (f *w1a6cFixture) cleanup() {
	ctx := context.Background()
	stmts := []struct {
		sql string
		arg any
	}{
		// tickets.order_id and order_items.ticket_id (feature #488) form a FK
		// cycle between tickets and the order aggregate, so both links have to
		// be nulled before either side can be deleted.
		{`UPDATE order_items SET ticket_id = NULL
		   WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`UPDATE tickets SET order_id = NULL WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM barcodes WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM delivery_jobs WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM ticket_credentials WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM tickets WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id::text FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM orders WHERE org_id = $1`, f.orgID},
		{`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID},
		{`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM reservations WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM event_publications WHERE event_id = $1`, f.eventID},
		{`DELETE FROM agent_feed_tokens WHERE id = $1`, f.tokenID},
		{`DELETE FROM sessions WHERE id = $1`, f.sessionID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
		// customer_identities.customer_id and customer_org_links.customer_id are
		// both ON DELETE CASCADE, so deleting the customer row is enough.
		{`DELETE FROM customers WHERE id IN
		   (SELECT customer_id FROM customer_identities WHERE value_normalized = $1)`, f.buyerMail},
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("W1A6c cleanup (%.60s): %v", s.sql, err)
		}
	}
}

// TestW1A6c_PublicFeedPurchase_OrderItemsAndOrderPaid is the feature #488
// acceptance test: a public-feed purchase produces exactly one order carrying
// the buyer fields, one order_item per issued ticket with the links backfilled
// in BOTH directions, and exactly one v1.order.paid outbox row — which stays
// exactly one across an issuance replay.
func TestW1A6c_PublicFeedPurchase_OrderItemsAndOrderPaid(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newW1A6cFixture(t, ctx, pool)
	defer f.cleanup()

	srv := buildIntegrationResetServer(t, pool)
	q := gen.New(pool)

	const qty = 2

	// ── 1. Real endpoint: public-feed checkout start ─────────────────────────
	body, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        qty,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Wanda Buyer",
			"phone": f.buyerPhone,
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var startResp struct {
		CheckoutSession struct {
			ID string `json:"id"`
		} `json:"checkout_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode start response: %v (body: %s)", err, rec.Body.String())
	}
	csID, err := uuid.Parse(startResp.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id is not a UUID: %v", err)
	}

	// ── 2. The order aggregate was minted on the SAME transaction ────────────
	// (spec §14.1: no confirmed public checkout without its order).
	var orderCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE org_id = $1`, f.orgID).Scan(&orderCount); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orderCount != 1 {
		t.Fatalf("orders for org = %d, want exactly 1", orderCount)
	}

	order, err := q.GetOrderByCheckoutSession(ctx, csID)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession: %v", err)
	}
	if order.BuyerEmail == nil || *order.BuyerEmail != f.buyerMail {
		t.Errorf("orders.buyer_email = %v, want %q", order.BuyerEmail, f.buyerMail)
	}
	if order.BuyerName == nil || *order.BuyerName != "Wanda Buyer" {
		t.Errorf("orders.buyer_name = %v, want \"Wanda Buyer\"", order.BuyerName)
	}
	if order.BuyerPhone == nil || *order.BuyerPhone != f.buyerPhone {
		t.Errorf("orders.buyer_phone = %v, want %q", order.BuyerPhone, f.buyerPhone)
	}
	if order.CustomerID == nil {
		t.Error("orders.customer_id is NULL — §12.2 customer resolution did not run")
	}
	if order.Source != "public_feed" {
		t.Errorf("orders.source = %q, want \"public_feed\"", order.Source)
	}
	if order.OrgID != f.orgID || order.ChannelID != f.channelID ||
		order.SessionID != f.sessionID || order.EventID != f.eventID {
		t.Errorf("order scope = org %s / channel %s / session %s / event %s, want %s / %s / %s / %s",
			order.OrgID, order.ChannelID, order.SessionID, order.EventID,
			f.orgID, f.channelID, f.sessionID, f.eventID)
	}
	// fee_percent 1.25 → 125 bp.
	if order.ChargePercentBP != 125 {
		t.Errorf("orders.charge_percent_bp = %d, want 125", order.ChargePercentBP)
	}

	// ── 3. Issuance: the step the payment webhook triggers ───────────────────
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	tickets, err := srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs)
	if err != nil {
		t.Fatalf("IssueTicketsForCheckout: %v", err)
	}
	if len(tickets) != qty {
		t.Fatalf("issued tickets = %d, want %d", len(tickets), qty)
	}

	// Every ticket carries the order link and the buyer's email.
	for _, tk := range tickets {
		var orderID *uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT order_id FROM tickets WHERE id = $1`, tk.ID).Scan(&orderID); err != nil {
			t.Fatalf("read tickets.order_id: %v", err)
		}
		if orderID == nil || *orderID != order.ID {
			t.Errorf("ticket %s order_id = %v, want %s", tk.ID, orderID, order.ID)
		}
		if tk.HolderEmail == nil || *tk.HolderEmail != f.buyerMail {
			t.Errorf("ticket %s holder_email = %v, want %q", tk.ID, tk.HolderEmail, f.buyerMail)
		}
	}

	// order_items == tickets, one-to-one, no item left unlinked and no ticket
	// claimed twice.
	items, err := q.ListOrderItemsByOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("ListOrderItemsByOrder: %v", err)
	}
	if len(items) != len(tickets) {
		t.Fatalf("order_items = %d, tickets = %d — want one item per ticket", len(items), len(tickets))
	}
	seen := make(map[uuid.UUID]struct{}, len(items))
	for _, it := range items {
		if it.TicketID == nil {
			t.Fatalf("order_item %s (ordinal %d) has a NULL ticket_id", it.ID, it.Ordinal)
		}
		if _, dup := seen[*it.TicketID]; dup {
			t.Fatalf("ticket %s is linked to more than one order_item", *it.TicketID)
		}
		seen[*it.TicketID] = struct{}{}
	}
	for _, tk := range tickets {
		if _, ok := seen[tk.ID]; !ok {
			t.Errorf("ticket %s is not referenced by any order_item", tk.ID)
		}
	}

	// ── 4. Exactly one v1.order.paid, with the §9.1 payload ──────────────────
	var (
		paidCount   int
		rawPayload  []byte
		aggregateID uuid.UUID
	)
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE aggregate_type = $1 AND event_type = $2 AND aggregate_id = $3::text`,
		htickets.OrderAggregateType, htickets.OrderPaidEventType, order.ID).Scan(&paidCount); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if paidCount != 1 {
		t.Fatalf("v1.order.paid rows = %d, want exactly 1", paidCount)
	}
	if err := pool.QueryRow(ctx,
		`SELECT aggregate_id::uuid, payload FROM outbox_events
		 WHERE aggregate_type = $1 AND event_type = $2 AND aggregate_id = $3::text`,
		htickets.OrderAggregateType, htickets.OrderPaidEventType, order.ID,
	).Scan(&aggregateID, &rawPayload); err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	if aggregateID != order.ID {
		t.Errorf("outbox.aggregate_id = %s, want %s", aggregateID, order.ID)
	}
	var payload struct {
		OrderID     string `json:"order_id"`
		OrgID       string `json:"org_id"`
		ChannelID   string `json:"channel_id"`
		SessionID   string `json:"session_id"`
		TicketCount int    `json:"ticket_count"`
	}
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatalf("decode outbox payload: %v (raw: %s)", err, rawPayload)
	}
	if payload.OrderID != order.ID.String() || payload.OrgID != f.orgID.String() ||
		payload.ChannelID != f.channelID.String() || payload.SessionID != f.sessionID.String() {
		t.Errorf("payload identity = %+v, want order %s / org %s / channel %s / session %s",
			payload, order.ID, f.orgID, f.channelID, f.sessionID)
	}
	if payload.TicketCount != qty {
		t.Errorf("payload.ticket_count = %d, want %d", payload.TicketCount, qty)
	}

	// ── 5. Replay stays exactly-once ─────────────────────────────────────────
	// The webhook is at-least-once by construction, so a second issuance must
	// neither mint a ticket nor emit a second event.
	replayed, err := srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs)
	if err != nil {
		t.Fatalf("IssueTicketsForCheckout replay: %v", err)
	}
	if len(replayed) != qty {
		t.Errorf("replay returned %d tickets, want %d", len(replayed), qty)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE aggregate_type = $1 AND event_type = $2 AND aggregate_id = $3::text`,
		htickets.OrderAggregateType, htickets.OrderPaidEventType, order.ID).Scan(&paidCount); err != nil {
		t.Fatalf("re-count outbox rows: %v", err)
	}
	if paidCount != 1 {
		t.Fatalf("v1.order.paid rows after replay = %d, want still exactly 1", paidCount)
	}
	var ticketCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tickets WHERE checkout_session_id = $1`, csID).Scan(&ticketCount); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if ticketCount != qty {
		t.Fatalf("tickets after replay = %d, want %d", ticketCount, qty)
	}
}

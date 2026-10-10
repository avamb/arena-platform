//go:build integration

// orders_event_center_integration_test.go — EC-04 / EC-05 (spec 35 §5.5,
// §5.6) through the REAL router with a manager's JWT (membership role
// organizer, empty roles claim — exactly what the Telegram bot mints):
// the one-parameter order search (barcode, order number, e-mail, phone,
// name), the tabs, paging with total_count, the session/event filters
// (a foreign session is 404), and the order card's tickets, delivery,
// payment, unpaid_reason and channel blocks. An agent membership is the
// 403 control.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

type ecOrdersFixture struct {
	t    *testing.T
	ts   *httptest.Server
	pool *pgxpool.Pool

	orgA, orgB         uuid.UUID
	managerTok         string
	agentTok           string
	eventA, sessionA   uuid.UUID
	sessionB           uuid.UUID
	channelA           uuid.UUID
	paidOrder          uuid.UUID
	paidSystemID       int64
	expiredOrder       uuid.UUID
	expiredSystemID    int64
	ticket1, ticket2   uuid.UUID
	ticket1Code        string // stored EAN-13 credential of ticket 1
	ticket2SystemID    int64  // ticket 2 has no credential: legacy PlatformCode
	accountEmail       string // the customer's identity e-mail, not the buyer_email
	phoneDigits        string // "34" + 9 national digits
	phoneNational      string
	expiredBuyerEmail  string
	expiredFailureCode string
}

func newECOrdersFixture(t *testing.T) *ecOrdersFixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	pool := srv.pgxPool
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]
	rnd := rand.New(rand.NewSource(time.Now().UnixNano())) // #nosec G404 -- fixture randomisation only

	const (
		secret   = "integration-test-secret-32-bytes!!"
		issuer   = "arena-api"
		audience = "arena-api"
	)
	mint := func(userID uuid.UUID) string {
		tok, _, err := auth.IssueJWT(secret, userID, nil, nil, issuer, audience, time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}

	manager, err := q.InsertUser(ctx, "ec-manager-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser manager: %v", err)
	}
	agent, err := q.InsertUser(ctx, "ec-agent-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	orgA, err := q.InsertOrganization(ctx, "EC Orders A "+suffix, "ec-orders-a-"+suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization A: %v", err)
	}
	orgB, err := q.InsertOrganization(ctx, "EC Orders B "+suffix, "ec-orders-b-"+suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization B: %v", err)
	}

	f := &ecOrdersFixture{
		t: t, ts: ts, pool: pool,
		orgA: orgA.ID, orgB: orgB.ID,
		managerTok: mint(manager.ID), agentTok: mint(agent.ID),
		eventA: uuid.New(), sessionA: uuid.New(), sessionB: uuid.New(), channelA: uuid.New(),
		paidOrder: uuid.New(), expiredOrder: uuid.New(),
		ticket1: uuid.New(), ticket2: uuid.New(),
		accountEmail:       "account-" + suffix + "@example.com",
		expiredBuyerEmail:  "boris-" + suffix + "@example.com",
		expiredFailureCode: "card_declined",
	}
	f.phoneNational = fmt.Sprintf("6%08d", rnd.Intn(100000000))
	f.phoneDigits = "34" + f.phoneNational
	f.ticket1Code, err = ean13.Random()
	if err != nil {
		t.Fatalf("ean13.Random: %v", err)
	}
	ticket2Code, err := ean13.Random()
	if err != nil {
		t.Fatalf("ean13.Random: %v", err)
	}

	venueA, venueB, eventB, tierA := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	customer := uuid.New()
	res1, cs1, res2, cs2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	item1, item2, item3 := uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute)
	// The buyer typed the phone with spaces and a dash: "+34 6xx xxx-xxx".
	buyerPhone := "+34 " + f.phoneNational[:3] + " " + f.phoneNational[3:6] + "-" + f.phoneNational[6:]

	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`, []any{manager.ID, orgA.ID}},
		{`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`, []any{manager.ID, orgB.ID}},
		{`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'agent')`, []any{agent.ID, orgA.ID}},
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueA, orgA.ID, "EC Venue A " + suffix}},
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, []any{venueB, orgB.ID, "EC Venue B " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{f.eventA, orgA.ID, "EC Event A " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, []any{eventB, orgB.ID, "EC Event B " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{f.sessionA, f.eventA, venueA, start}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{f.sessionB, eventB, venueB, start}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
		  VALUES ($1, $2, 'Parterre', 'fixed', 2500, 'EUR', 1, true)`, []any{tierA, f.sessionA}},
		{`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{"hosted_page": {"enabled": true}}'::jsonb)`,
			[]any{f.channelA, orgA.ID, "EC Channel " + suffix}},
		{`INSERT INTO customers (id, display_name) VALUES ($1, 'Anna Nováková')`, []any{customer}},
		{`INSERT INTO customer_identities (customer_id, kind, value_normalized) VALUES ($1, 'email', $2)`, []any{customer, f.accountEmail}},
		{`INSERT INTO customer_identities (customer_id, kind, value_normalized) VALUES ($1, 'phone', $2)`, []any{customer, "+" + f.phoneDigits}},
		// Paid order: two tickets, bought an hour ago.
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, []any{res1, orgA.ID, f.channelA, f.sessionA}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, []any{cs1, orgA.ID, f.channelA, res1}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, customer_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone, paid_at, created_at)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'public_feed', 'paid', 'EUR', 5000, 0, 0, 5000, 'Anna Nováková', $9, $10,
		          now() - interval '1 hour', now() - interval '1 hour')`,
			[]any{f.paidOrder, orgA.ID, f.channelA, f.eventA, f.sessionA, customer, cs1, res1, "anna-" + suffix + "@example.com", buyerPhone}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, seat_sector, seat_row, seat_number)
		  VALUES ($1, $2, $3, $4, $5, $6, 0, 'A', '3', '12')`, []any{f.ticket1, cs1, f.sessionA, tierA, "anna-" + suffix + "@example.com", f.paidOrder}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		  VALUES ($1, $2, $3, $4, $5, $6, 1)`, []any{f.ticket2, cs1, f.sessionA, tierA, "anna-" + suffix + "@example.com", f.paidOrder}},
		{`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		  VALUES ($1, $2, 0, 'ticket', $3, $4, 2500, 0, 0, 2500)`, []any{item1, f.paidOrder, tierA, f.ticket1}},
		{`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		  VALUES ($1, $2, 1, 'ticket', $3, $4, 2500, 0, 0, 2500)`, []any{item2, f.paidOrder, tierA, f.ticket2}},
		// Ticket 1: stored credential + barcode row, letter sent.
		{`INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'ean13', $2)`, []any{f.ticket1, f.ticket1Code}},
		{`INSERT INTO barcodes (authority_id, external_ref, ticket_id, status)
		  SELECT id, $2, $1, 'active' FROM barcode_authorities WHERE type = 'platform'`, []any{f.ticket1, f.ticket1Code}},
		{`INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at) VALUES ($1, $2, 'sent', now() - interval '50 minutes')`,
			[]any{f.ticket1, "anna-" + suffix + "@example.com"}},
		// Ticket 2: no stored credential (legacy), scanned at the door, letter sent.
		{`INSERT INTO barcodes (authority_id, external_ref, ticket_id, status, scanned_at)
		  SELECT id, $2, $1, 'scanned', now() - interval '10 minutes' FROM barcode_authorities WHERE type = 'platform'`, []any{f.ticket2, ticket2Code}},
		{`INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at) VALUES ($1, $2, 'sent', now() - interval '50 minutes')`,
			[]any{f.ticket2, "anna-" + suffix + "@example.com"}},
		// Expired order: the card was declined, the hold ran out.
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		  VALUES ($1, $2, $3, $4, 1, 'expired', now() - interval '5 minutes')`, []any{res2, orgA.ID, f.channelA, f.sessionA}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'pricing_confirmed')`, []any{cs2, orgA.ID, f.channelA, res2}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, expires_at, created_at)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'expired', 'EUR', 2500, 0, 0, 2500, 'Boris Petrov', $8,
		          now() - interval '5 minutes', now())`,
			[]any{f.expiredOrder, orgA.ID, f.channelA, f.eventA, f.sessionA, cs2, res2, f.expiredBuyerEmail}},
		{`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, unit_price, discount, charge, total)
		  VALUES ($1, $2, 0, 'ticket', $3, 2500, 0, 0, 2500)`, []any{item3, f.expiredOrder, tierA}},
		{`INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state,
		                               failure_code, failure_message, failed_at)
		  VALUES ($1, $2, 'stripe', $3, 2500, 'EUR', 'failed', $4, 'Your card was declined.', now() - interval '20 minutes')`,
			[]any{cs2, orgA.ID, "cs_test_ec_" + uuid.NewString(), f.expiredFailureCode}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}
	for _, pair := range []struct {
		dst *int64
		sql string
		id  uuid.UUID
	}{
		{&f.paidSystemID, `SELECT system_id FROM orders WHERE id = $1`, f.paidOrder},
		{&f.expiredSystemID, `SELECT system_id FROM orders WHERE id = $1`, f.expiredOrder},
		{&f.ticket2SystemID, `SELECT system_ticket_id FROM tickets WHERE id = $1`, f.ticket2},
	} {
		if err := pool.QueryRow(ctx, pair.sql, pair.id).Scan(pair.dst); err != nil {
			t.Fatalf("read back %s: %v", pair.sql, err)
		}
	}

	t.Cleanup(func() {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM delivery_jobs WHERE ticket_id = ANY($1)`, []any{[]uuid.UUID{f.ticket1, f.ticket2}}},
			{`DELETE FROM barcodes WHERE ticket_id = ANY($1)`, []any{[]uuid.UUID{f.ticket1, f.ticket2}}},
			{`DELETE FROM ticket_credentials WHERE ticket_id = ANY($1)`, []any{[]uuid.UUID{f.ticket1, f.ticket2}}},
			{`DELETE FROM order_items WHERE order_id = ANY($1)`, []any{[]uuid.UUID{f.paidOrder, f.expiredOrder}}},
			{`DELETE FROM tickets WHERE id = ANY($1)`, []any{[]uuid.UUID{f.ticket1, f.ticket2}}},
			{`DELETE FROM payment_intents WHERE checkout_session_id = ANY($1)`, []any{[]uuid.UUID{cs1, cs2}}},
			{`DELETE FROM orders WHERE id = ANY($1)`, []any{[]uuid.UUID{f.paidOrder, f.expiredOrder}}},
			{`DELETE FROM checkout_sessions WHERE id = ANY($1)`, []any{[]uuid.UUID{cs1, cs2}}},
			{`DELETE FROM reservations WHERE id = ANY($1)`, []any{[]uuid.UUID{res1, res2}}},
			{`DELETE FROM customer_identities WHERE customer_id = $1`, []any{customer}},
			{`DELETE FROM customers WHERE id = $1`, []any{customer}},
			{`DELETE FROM memberships WHERE org_id = ANY($1)`, []any{[]uuid.UUID{orgA.ID, orgB.ID}}},
			{`DELETE FROM sales_channels WHERE id = $1`, []any{f.channelA}},
			{`DELETE FROM ticket_tiers WHERE id = $1`, []any{tierA}},
			{`DELETE FROM sessions WHERE id = ANY($1)`, []any{[]uuid.UUID{f.sessionA, f.sessionB}}},
			{`DELETE FROM events WHERE id = ANY($1)`, []any{[]uuid.UUID{f.eventA, eventB}}},
			{`DELETE FROM venues WHERE id = ANY($1)`, []any{[]uuid.UUID{venueA, venueB}}},
			{`DELETE FROM audit_events WHERE metadata->>'org_id' = ANY($1)`, []any{[]string{orgA.ID.String(), orgB.ID.String()}}},
			{`DELETE FROM organizations WHERE id = ANY($1)`, []any{[]uuid.UUID{orgA.ID, orgB.ID}}},
			{`DELETE FROM users WHERE id = ANY($1)`, []any{[]uuid.UUID{manager.ID, agent.ID}}},
		} {
			if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				t.Logf("ec orders cleanup: %s: %v", stmt.sql, err)
			}
		}
	})
	return f
}

func (f *ecOrdersFixture) get(path, token string) (int, map[string]any) {
	f.t.Helper()
	resp := integDoRequest(f.t, f.ts.Client(), http.MethodGet, f.ts.URL+path, token, "")
	raw := integReadBody(f.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			f.t.Fatalf("GET %s: decode %q: %v", path, raw, err)
		}
	}
	return resp.StatusCode, out
}

// listIDs runs the list for org A as the manager and returns the order ids
// in page order plus total_count.
func (f *ecOrdersFixture) listIDs(query string) ([]string, float64, bool) {
	f.t.Helper()
	code, body := f.get("/v1/organizations/"+f.orgA.String()+"/orders?"+query, f.managerTok)
	if code != http.StatusOK {
		f.t.Fatalf("list %q: status %d body %v", query, code, body)
	}
	rows, _ := body["orders"].([]any)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		m := r.(map[string]any)
		ids = append(ids, m["id"].(string))
	}
	total, _ := body["total_count"].(float64)
	hasMore, _ := body["has_more"].(bool)
	return ids, total, hasMore
}

func (f *ecOrdersFixture) expectOnly(query string, want uuid.UUID) {
	f.t.Helper()
	ids, total, _ := f.listIDs(query)
	if len(ids) != 1 || ids[0] != want.String() || total != 1 {
		f.t.Fatalf("list %q: got ids %v total %v, want only %s", query, ids, total, want)
	}
}

func ecErrCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func TestEventCenterOrders_SearchTabsAndPaging(t *testing.T) {
	f := newECOrdersFixture(t)
	paid, expired := f.paidOrder, f.expiredOrder

	// Recent: newest first, both orders, the list extras present.
	code, body := f.get("/v1/organizations/"+f.orgA.String()+"/orders", f.managerTok)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	rows := body["orders"].([]any)
	if len(rows) != 2 || body["total_count"] != float64(2) || body["has_more"] != false {
		t.Fatalf("recent: rows %d total %v has_more %v", len(rows), body["total_count"], body["has_more"])
	}
	first := rows[0].(map[string]any)
	if first["id"] != expired.String() || first["event_name"] == "" || first["session_timezone"] != "Europe/Madrid" ||
		first["session_start_at"] == nil {
		t.Fatalf("recent first row: %v", first)
	}

	// q precedence.
	f.expectOnly("q="+f.ticket1Code, paid)                                       // stored EAN-13
	f.expectOnly("q="+ean13.PlatformCode(f.ticket2SystemID), paid)               // legacy code of a credential-less ticket
	f.expectOnly(fmt.Sprintf("q=%d", f.expiredSystemID), expired)                // order number
	f.expectOnly(fmt.Sprintf("q=%%23%d", f.paidSystemID), paid)                  // #number
	f.expectOnly("q="+f.accountEmail, paid)                                      // customer identity, not buyer_email
	f.expectOnly("q="+strings.ToUpper(f.expiredBuyerEmail), expired)             // buyer e-mail, case-folded
	f.expectOnly("q=%2B34%20"+f.phoneNational[:3]+"-"+f.phoneNational[3:], paid) // +34 with space and dash
	f.expectOnly("q=00"+f.phoneDigits, paid)                                     // 00 prefix (13 digits: barcode AND phone)
	f.expectOnly("q="+f.phoneNational, paid)                                     // national number, suffix match
	f.expectOnly("q=Anna%20Nov%C3%A1k", paid)                                    // name similarity
	if ids, total, _ := f.listIDs("q=" + uuid.NewString()[:8] + "@nowhere.example"); len(ids) != 0 || total != 0 {
		t.Fatalf("unknown e-mail must find nothing, got %v / %v", ids, total)
	}

	// Tabs.
	f.expectOnly("tab=paid", paid)
	f.expectOnly("tab=unpaid", expired)
	if code, body := f.get("/v1/organizations/"+f.orgA.String()+"/orders?tab=refunded", f.managerTok); code != http.StatusBadRequest || ecErrCode(body) != "orders.invalid_tab" {
		t.Fatalf("tab=refunded: %d %v", code, body)
	}

	// Paging at limit=1.
	ids, total, hasMore := f.listIDs("limit=1")
	if len(ids) != 1 || ids[0] != expired.String() || total != 2 || !hasMore {
		t.Fatalf("limit=1: ids %v total %v has_more %v", ids, total, hasMore)
	}
	ids, total, hasMore = f.listIDs("limit=1&offset=1")
	if len(ids) != 1 || ids[0] != paid.String() || total != 2 || hasMore {
		t.Fatalf("limit=1&offset=1: ids %v total %v has_more %v", ids, total, hasMore)
	}

	// Session / event filters; a foreign or unknown id is 404.
	if ids, _, _ := f.listIDs("session_id=" + f.sessionA.String()); len(ids) != 2 {
		t.Fatalf("session filter: %v", ids)
	}
	if ids, _, _ := f.listIDs("event_id=" + f.eventA.String() + "&tab=paid"); len(ids) != 1 || ids[0] != paid.String() {
		t.Fatalf("event filter + tab: %v", ids)
	}
	for _, q := range []string{"session_id=" + f.sessionB.String(), "session_id=" + uuid.NewString(), "event_id=" + uuid.NewString()} {
		code, body := f.get("/v1/organizations/"+f.orgA.String()+"/orders?"+q, f.managerTok)
		if code != http.StatusNotFound || !strings.HasSuffix(ecErrCode(body), "_not_found") {
			t.Fatalf("%s: %d %v, want 404", q, code, body)
		}
	}

	// An agent membership holds no order.read.
	if code, body := f.get("/v1/organizations/"+f.orgA.String()+"/orders", f.agentTok); code != http.StatusForbidden {
		t.Fatalf("agent list: %d %v", code, body)
	}
}

func TestEventCenterOrders_DetailBlocks(t *testing.T) {
	f := newECOrdersFixture(t)

	code, body := f.get("/v1/organizations/"+f.orgA.String()+"/orders/"+f.paidOrder.String(), f.managerTok)
	if code != http.StatusOK {
		t.Fatalf("paid detail: %d %v", code, body)
	}
	tickets := body["tickets"].([]any)
	if len(tickets) != 2 {
		t.Fatalf("paid detail tickets: %v", body["tickets"])
	}
	t1 := tickets[0].(map[string]any)
	t2 := tickets[1].(map[string]any)
	if t1["id"] != f.ticket1.String() || t1["barcode"] != f.ticket1Code || t1["tier_name"] != "Parterre" ||
		t1["price"] != float64(2500) || t1["currency"] != "EUR" || t1["used_at"] != nil || t1["seat_label"] != "A / 3 / 12" {
		t.Fatalf("ticket 1: %v", t1)
	}
	if t2["id"] != f.ticket2.String() || t2["barcode"] != ean13.PlatformCode(f.ticket2SystemID) || t2["used_at"] == nil || t2["seat_label"] != nil {
		t.Fatalf("ticket 2: %v", t2)
	}
	delivery := body["delivery"].([]any)
	if len(delivery) != 2 || body["delivery_state"] != "sent" {
		t.Fatalf("delivery: %v state %v", delivery, body["delivery_state"])
	}
	for _, d := range delivery {
		m := d.(map[string]any)
		if m["status"] != "sent" || m["sent_at"] == nil || m["last_error"] != nil {
			t.Fatalf("delivery entry: %v", m)
		}
	}
	if body["payment"] != nil || body["unpaid_reason"] != "" {
		t.Fatalf("paid order payment/unpaid_reason: %v / %v", body["payment"], body["unpaid_reason"])
	}
	channel := body["channel"].(map[string]any)
	if channel["id"] != f.channelA.String() || channel["kind"] != "hosted_page" || !strings.HasPrefix(channel["name"].(string), "EC Channel") {
		t.Fatalf("channel: %v", channel)
	}
	if len(body["items"].([]any)) != 2 {
		t.Fatalf("items: %v", body["items"])
	}

	code, body = f.get("/v1/organizations/"+f.orgA.String()+"/orders/"+f.expiredOrder.String(), f.managerTok)
	if code != http.StatusOK {
		t.Fatalf("expired detail: %d %v", code, body)
	}
	payment, _ := body["payment"].(map[string]any)
	if payment == nil || payment["state"] != "failed" || payment["provider"] != "stripe" ||
		payment["failure_code"] != f.expiredFailureCode || payment["failure_message"] == nil {
		t.Fatalf("expired payment: %v", body["payment"])
	}
	if body["unpaid_reason"] != "payment_failed" || body["delivery_state"] != "none" || len(body["tickets"].([]any)) != 0 {
		t.Fatalf("expired blocks: reason %v delivery %v tickets %v", body["unpaid_reason"], body["delivery_state"], body["tickets"])
	}

	// Tenant isolation: the manager is a member of org B too, but the order
	// is org A's — 404, like an unknown id.
	if code, body := f.get("/v1/organizations/"+f.orgB.String()+"/orders/"+f.paidOrder.String(), f.managerTok); code != http.StatusNotFound || ecErrCode(body) != "orders.not_found" {
		t.Fatalf("foreign org detail: %d %v", code, body)
	}
	if code, _ := f.get("/v1/organizations/"+f.orgA.String()+"/orders/"+f.paidOrder.String(), f.agentTok); code != http.StatusForbidden {
		t.Fatalf("agent detail: %d, want 403", code)
	}
}

// TestEventCenterOrders_ManagerPassesAgentIsRefused pins the gate on both
// routes together, now that migration 0129 grants the manager the whole
// operational set: an organizer membership with an EMPTY roles claim reaches
// the list and the card (200), an agent membership in the same organization
// does not (403).
func TestEventCenterOrders_ManagerPassesAgentIsRefused(t *testing.T) {
	f := newECOrdersFixture(t)
	for _, path := range []string{
		"/v1/organizations/" + f.orgA.String() + "/orders",
		"/v1/organizations/" + f.orgA.String() + "/orders/" + f.paidOrder.String(),
	} {
		if code, body := f.get(path, f.managerTok); code != http.StatusOK {
			t.Errorf("manager GET %s: %d %v, want 200", path, code, body)
		}
		if code, body := f.get(path, f.agentTok); code != http.StatusForbidden {
			t.Errorf("agent GET %s: %d %v, want 403", path, code, body)
		}
	}
}

// TestEventCenterOrders_ExactOrderNumberListedBeforePhoneSuffix guards the
// double reading of a 10-digit query: it is an order number AND a national
// phone (a 9+ digit suffix of the buyer's digits matches). An unrelated, NEWER
// order whose phone merely ends in those digits must not push the order that
// really carries the number off the top of the list.
func TestEventCenterOrders_ExactOrderNumberListedBeforePhoneSuffix(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	decoy := uuid.New()
	number := fmt.Sprintf("%d", f.paidSystemID)
	if len(number) < 9 {
		t.Skipf("system_id %s is shorter than the 9-digit phone-suffix threshold", number)
	}
	res, cs := uuid.New(), uuid.New()
	for i, st := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		  VALUES ($1, $2, $3, $4, 1, 'expired', now() - interval '5 minutes')`, []any{res, f.orgA, f.channelA, f.sessionA}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'pricing_confirmed')`,
			[]any{cs, f.orgA, f.channelA, res}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone, created_at)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'expired', 'EUR', 2500, 0, 0, 2500,
		          'Phone Decoy', $8, $9, now() + interval '1 minute')`,
			[]any{decoy, f.orgA, f.channelA, f.eventA, f.sessionA, cs, res,
				"decoy-" + decoy.String()[:8] + "@example.com", "+34 " + number[:3] + " " + number[3:]}},
	} {
		if _, err := f.pool.Exec(ctx, st.sql, st.args...); err != nil {
			t.Fatalf("decoy fixture step %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		for _, st := range []struct {
			sql string
			id  uuid.UUID
		}{
			{`DELETE FROM orders WHERE id = $1`, decoy},
			{`DELETE FROM checkout_sessions WHERE id = $1`, cs},
			{`DELETE FROM reservations WHERE id = $1`, res},
		} {
			if _, err := f.pool.Exec(ctx, st.sql, st.id); err != nil {
				t.Logf("decoy cleanup: %v", err)
			}
		}
	})

	ids, total, _ := f.listIDs("q=" + number)
	if len(ids) < 1 || ids[0] != f.paidOrder.String() {
		t.Fatalf("q=%s: got %v, want the order numbered %s first", number, ids, number)
	}
	found := false
	for _, id := range ids {
		found = found || id == decoy.String()
	}
	if !found || total != float64(len(ids)) {
		t.Fatalf("q=%s: decoy (phone suffix) should be listed after the exact hit: ids %v total %v", number, ids, total)
	}
}

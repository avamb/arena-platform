//go:build integration

// external_refund_integration_test.go — П-14 (spec 62 14.6): "refund outside
// the platform" through the REAL router. Money the organizer returned in the
// provider's own cabinet is booked per ticket as a FINAL amount
// (settlement='external'), the tickets are cancelled and their places go back
// on sale, a second attempt is refused with the amount already recorded, the
// amount may not exceed the ticket's price, and the session summary shows the
// external total.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestExternalRefund_FinalAmountPerTicket(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, nil)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "extref-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "EXTREF")
	key := pf.key(t, org, "refund.create", "order.read")
	readOnly := pf.key(t, org, "order.read")
	ctx := context.Background()

	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	// Registered after the fixture's own cleanup, so it runs BEFORE it (LIFO):
	// external refunds and tickets point at each other.
	t.Cleanup(func() {
		for _, s := range []string{
			`UPDATE tickets SET refund_id = NULL WHERE order_id = $1`,
			`DELETE FROM refunds WHERE order_id = $1 AND settlement = 'external'`,
			`DELETE FROM barcodes WHERE ticket_id IN (SELECT id FROM tickets WHERE order_id = $1)`,
		} {
			if _, err := srv.pgxPool.Exec(context.Background(), s, f.order); err != nil {
				t.Logf("cleanup %q: %v", s, err)
			}
		}
		_, _ = srv.pgxPool.Exec(context.Background(),
			`DELETE FROM outbox_events WHERE aggregate_id = ANY($1::text[])`, []string{f.tickets[0].String(), f.tickets[1].String()})
	})
	path := fmt.Sprintf("/v1/organizations/%s/orders/%s/external-refund", org, f.order)
	body := func(items ...string) string {
		s := `{"items":[`
		for i, it := range items {
			if i > 0 {
				s += ","
			}
			s += it
		}
		return s + `]}`
	}
	item := func(ticket uuid.UUID, amount int64, ref string) string {
		return fmt.Sprintf(`{"ticket_id":%q,"amount_minor":%d,"provider_refund_ref":%q}`, ticket, amount, ref)
	}

	// Permission: a key without refund.create is refused.
	if st, _ := pf.do(t, http.MethodPost, path, readOnly, body(item(f.tickets[0], 1000, "AP-1"))); st != http.StatusForbidden {
		t.Fatalf("without refund.create: %d; want 403", st)
	}
	// Another organization's key, with the permission, cannot reach this order: the route's own 404.
	otherOrg := pf.org(t, "EXTREF2")
	otherKey := pf.key(t, otherOrg, "refund.create")
	foreign := fmt.Sprintf("/v1/organizations/%s/orders/%s/external-refund", otherOrg, f.order)
	if st, out := pf.do(t, http.MethodPost, foreign, otherKey, body(item(f.tickets[0], 100, "AP-X"))); st != http.StatusNotFound || prom0113Code(out) != "orders.not_found" {
		t.Fatalf("foreign organization: %d %v; want 404 orders.not_found", st, out)
	}
	if s, _ := f.ticket(t, 0); s != "active" {
		t.Fatalf("a foreign call changed ticket 0 to %s", s)
	}
	// Validation: nothing is written for a refusal.
	if st, out := pf.do(t, http.MethodPost, path, key, body(item(f.tickets[0], 2501, "AP-1"))); st != http.StatusUnprocessableEntity || prom0113Code(out) != "order.refund_exceeds_price" {
		t.Fatalf("above the price: %d %v", st, out)
	}
	if st, out := pf.do(t, http.MethodPost, path, key, body(item(uuid.New(), 100, "AP-1"))); st != http.StatusUnprocessableEntity || prom0113Code(out) != "order.ticket_not_in_order" {
		t.Fatalf("foreign ticket: %d %v", st, out)
	}
	if st, out := pf.do(t, http.MethodPost, path, key, body(item(f.tickets[0], 0, "AP-1"))); st != http.StatusBadRequest {
		t.Fatalf("zero amount: %d %v", st, out)
	}
	// An all-or-nothing call: the second line is bad, the first must not stick.
	if st, _ := pf.do(t, http.MethodPost, path, key, body(item(f.tickets[0], 1000, "AP-1"), item(f.tickets[1], 9999, "AP-1"))); st != http.StatusUnprocessableEntity {
		t.Fatalf("mixed call: %d", st)
	}
	if s, _ := f.ticket(t, 0); s != "active" {
		t.Fatalf("ticket 0 = %s after a refused call; want active", s)
	}

	// 1. A PARTIAL final refund of ticket 0: 10.00 of 25.00, the ticket is void.
	st, out := pf.do(t, http.MethodPost, path, key, body(item(f.tickets[0], 1000, "AP-100")))
	if st != http.StatusOK || out["order_status"] != "partially_refunded" || out["total_minor"] != float64(1000) {
		t.Fatalf("partial: %d %v", st, out)
	}
	if s, link := f.ticket(t, 0); s != "cancelled" || link == nil {
		t.Fatalf("ticket 0 = %s link %v; want cancelled and linked to the refund", s, link)
	}
	var settlement, ref, state string
	var amount int64
	if err := srv.pgxPool.QueryRow(ctx, `SELECT settlement, amount, COALESCE(external_ref, ''), state FROM refunds WHERE ticket_id = $1`, f.tickets[0]).
		Scan(&settlement, &amount, &ref, &state); err != nil {
		t.Fatal(err)
	}
	if settlement != "external" || amount != 1000 || ref != "AP-100" || state != "succeeded" {
		t.Fatalf("refund row = %s %d %q %s", settlement, amount, ref, state)
	}
	var sold int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT capacity_sold FROM inventory_ledger WHERE session_id = $1`, f.session).Scan(&sold)
	if sold != 1 {
		t.Fatalf("capacity_sold = %d; want 1 (the place went back on sale)", sold)
	}
	var cancelled int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'v1.ticket.cancelled'`, f.tickets[0].String()).Scan(&cancelled)
	if cancelled != 1 {
		t.Fatalf("v1.ticket.cancelled events = %d; want 1", cancelled)
	}
	var audits int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action = 'v1.order.external_refund'`, f.order.String()).Scan(&audits)
	if audits != 1 {
		t.Fatalf("order audit rows = %d; want 1", audits)
	}

	// 2. One FINAL refund per ticket: a second attempt answers 409 with the amount.
	st, out = pf.do(t, http.MethodPost, path, key, body(item(f.tickets[0], 500, "AP-101")))
	if st != http.StatusConflict || prom0113Code(out) != "order.external_refund_exists" {
		t.Fatalf("repeat: %d %v", st, out)
	}
	if d := extRefundDetails(out); d["amount_minor"] != float64(1000) {
		t.Fatalf("details = %v; want the recorded 1000", d)
	}

	// 3. The summary shows the external total.
	st, sum := pf.do(t, http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sessions/%s/summary", org, f.session), key, "")
	if st != http.StatusOK {
		t.Fatalf("summary: %d %v", st, sum)
	}
	found := false
	rs, _ := sum["refunds"].([]any)
	for _, r := range rs {
		m, _ := r.(map[string]any)
		if m["settlement"] == "external" && m["state"] == "succeeded" && m["amount"] == float64(1000) {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary refunds = %v; want the external 1000", sum["refunds"])
	}

	refunded := false
	ms, _ := sum["money"].([]any)
	for _, m := range ms {
		if mm, _ := m.(map[string]any); mm["refunded"] == float64(1000) {
			refunded = true
		}
	}
	if !refunded {
		t.Fatalf("summary money = %v; want refunded 1000 (external refunds count as money returned)", sum["money"])
	}

	// 4. The last ticket: the order becomes refunded.
	st, out = pf.do(t, http.MethodPost, path, key, body(item(f.tickets[1], 2500, "AP-100")))
	if st != http.StatusOK || out["order_status"] != "refunded" {
		t.Fatalf("last: %d %v", st, out)
	}
	var orderStatus string
	_ = srv.pgxPool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&orderStatus)
	if orderStatus != "refunded" {
		t.Fatalf("order = %s; want refunded", orderStatus)
	}
	// A refunded order takes no more.
	if st, out = pf.do(t, http.MethodPost, path, key, body(item(f.tickets[1], 100, ""))); st != http.StatusConflict {
		t.Fatalf("refunded order: %d %v", st, out)
	}
}

func extRefundDetails(m map[string]any) map[string]any {
	if e, ok := m["error"].(map[string]any); ok {
		if d, ok := e["details"].(map[string]any); ok {
			return d
		}
	}
	return nil
}

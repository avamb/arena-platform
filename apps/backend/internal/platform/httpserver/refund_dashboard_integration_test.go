//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// A refund the organizer made in Stripe's own dashboard reaches arena as a
// refund.updated event no arena refund carries (PAY-05, spec 36 section 7):
// it is recorded with origin provider_dashboard, a refund smaller than the
// payment flags the tickets for review, one that covers the payment cancels
// them, and a replay or a foreign payment writes nothing.
func TestRefundWebhook_DashboardRefundIsRecordedAndAppliesTheInboundPolicy(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ctx := context.Background()
	pf := &prom0113Fixture{q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(ctx, "dash-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "DASHBOARD")
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	piRef := "pi_dash_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, f.pay, piRef); err != nil {
		t.Fatal(err)
	}
	var configID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM payment_provider_configs WHERE org_id = $1 AND provider = 'stripe'`, org).Scan(&configID); err != nil {
		t.Fatal(err)
	}
	path := "/v1/payment-intents/webhook/" + configID.String()
	const secret = "whsec_pay03"

	// event also registers the refund with the stub, because arena reads the
	// refund back from the provider before it believes the delivery.
	event := func(refundID, payment string, amount int) []byte {
		stub.setRefund(refundID, "succeeded", amount, "eur", payment)
		return []byte(fmt.Sprintf(`{"id":"evt_%s","type":"refund.updated","data":{"object":{"id":%q,"object":"refund","status":"succeeded","amount":%d,"currency":"eur","payment_intent":%q}}}`,
			refundID, refundID, amount, payment))
	}
	count := func(where string, args ...any) int {
		var n int
		if err := srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	post := func(body []byte) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Stripe-Signature", signStripe(secret, body))
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook = %d; body %s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return m
	}

	// a payment arena does not own: acknowledged, nothing written
	post(event("re_dash_foreign", "pi_somebody_elses", 2500))
	if n := count(`payment_intent_id = $1 AND origin = 'provider_dashboard'`, f.pay); n != 0 {
		t.Fatalf("foreign payment wrote %d dashboard refund(s)", n)
	}

	// a partial dashboard refund: recorded, tickets flagged, none cancelled
	out := post(event("re_dash_1", piRef, 2500))
	if out["origin"] != "provider_dashboard" {
		t.Fatalf("answer = %v; want origin provider_dashboard", out)
	}
	var state, origin string
	var settled bool
	if err := srv.pgxPool.QueryRow(ctx, `SELECT state, origin, settled_at IS NOT NULL FROM refunds
		WHERE payment_intent_id = $1 AND provider_refund_id = 're_dash_1'`, f.pay).Scan(&state, &origin, &settled); err != nil {
		t.Fatalf("dashboard refund row: %v", err)
	}
	if state != "succeeded" || origin != "provider_dashboard" || !settled {
		t.Fatalf("row = %s/%s settled=%v; want succeeded/provider_dashboard/settled", state, origin, settled)
	}
	for i := range f.tickets {
		if st, _ := f.ticket(t, i); st != "active" {
			t.Fatalf("ticket %d is %q after a partial dashboard refund; want active", i, st)
		}
	}
	var held int
	if err := srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE order_id = $1 AND review_hold`, f.order).Scan(&held); err != nil || held != 2 {
		t.Fatalf("tickets on review hold = %d (%v); want 2", held, err)
	}

	// a replay writes nothing more
	post(event("re_dash_1", piRef, 2500))
	if n := count(`payment_intent_id = $1 AND origin = 'provider_dashboard'`, f.pay); n != 1 {
		t.Fatalf("after a replay there are %d dashboard refunds; want 1", n)
	}

	// the rest of the payment: the order's tickets are cancelled, the order
	// is refunded, and each ticket's v1.ticket.refunded carries ITS 2500
	post(event("re_dash_2", piRef, 2500))
	for i := range f.tickets {
		if st, _ := f.ticket(t, i); st != "cancelled" {
			t.Fatalf("ticket %d is %q after the payment was refunded in full; want cancelled", i, st)
		}
	}
	var orderStatus string
	var events int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&orderStatus)
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'refunded'`, f.order).Scan(&events)
	if orderStatus != "refunded" || events == 0 {
		t.Fatalf("order status %q with %d refunded event(s); want refunded and an order event", orderStatus, events)
	}
	rows, err := srv.pgxPool.Query(ctx, `SELECT payload->>'amount' FROM outbox_events
		WHERE event_type = 'v1.ticket.refunded' AND aggregate_id = ANY($1::text[])`,
		[]string{f.tickets[0].String(), f.tickets[1].String()})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var amt string
		_ = rows.Scan(&amt)
		if amt != "2500" {
			t.Fatalf("v1.ticket.refunded amount = %s; want the ticket's own 2500", amt)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("v1.ticket.refunded events = %d; want 2", n)
	}
}

// What the provider says when asked decides what is written: a refund the
// provider does not confirm as succeeded writes nothing, an unreadable one
// is retried (500), one larger than what is left of the payment is capped,
// and one that names an arena refund is never taken for a dashboard refund.
func TestRefundWebhook_DashboardRefundIsConfirmedCappedAndNeverStealsArenaRefunds(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ctx := context.Background()
	pf := &prom0113Fixture{q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(ctx, "dashc-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "DASHCONF")
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	piRef := "pi_dashc_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, f.pay, piRef); err != nil {
		t.Fatal(err)
	}
	var configID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM payment_provider_configs WHERE org_id = $1 AND provider = 'stripe'`, org).Scan(&configID); err != nil {
		t.Fatal(err)
	}
	path := "/v1/payment-intents/webhook/" + configID.String()
	const secret = "whsec_pay03"
	post := func(body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Stripe-Signature", signStripe(secret, []byte(body)))
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	body := func(refundID string, amount int, metadata string) string {
		return fmt.Sprintf(`{"id":"evt_%s","type":"refund.updated","data":{"object":{"id":%q,"object":"refund","status":"succeeded","amount":%d,"currency":"eur","payment_intent":%q%s}}}`,
			refundID, refundID, amount, piRef, metadata)
	}
	dashRows := func() int {
		var n int
		_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1 AND origin = 'provider_dashboard'`, f.pay).Scan(&n)
		return n
	}

	// the provider says it is only pending: nothing written
	stub.setRefund("re_c_pending", "pending", 1000, "eur", piRef)
	if code, _ := post(body("re_c_pending", 1000, "")); code != http.StatusOK || dashRows() != 0 {
		t.Fatalf("a refund the provider calls pending: code %d, %d row(s); want 200 and none", code, dashRows())
	}
	// the provider cannot be asked: 500, so it is delivered again
	stub.refundMu.Lock()
	stub.refundObjects = map[string]string{"re_c_down": ""}
	stub.refundMu.Unlock()
	if code, _ := post(body("re_c_down", 1000, "")); code != http.StatusInternalServerError || dashRows() != 0 {
		t.Fatalf("an unreadable refund: code %d, %d row(s); want 500 and none", code, dashRows())
	}
	// the event lies about the amount: the provider's own figure is recorded
	stub.setRefund("re_c_lie", "succeeded", 1000, "eur", piRef)
	if code, _ := post(body("re_c_lie", 99999, "")); code != http.StatusOK {
		t.Fatalf("lying amount: code %d", code)
	}
	var amount int64
	if err := srv.pgxPool.QueryRow(ctx, `SELECT amount FROM refunds WHERE provider_refund_id = 're_c_lie'`).Scan(&amount); err != nil || amount != 1000 {
		t.Fatalf("recorded amount %d (%v); want the provider's 1000", amount, err)
	}
	// more than what is left (5000 - 1000): capped at 4000, which covers the payment
	stub.setRefund("re_c_big", "succeeded", 9000, "eur", piRef)
	if code, _ := post(body("re_c_big", 9000, "")); code != http.StatusOK {
		t.Fatalf("over-sized refund: code %d", code)
	}
	if err := srv.pgxPool.QueryRow(ctx, `SELECT amount FROM refunds WHERE provider_refund_id = 're_c_big'`).Scan(&amount); err != nil || amount != 4000 {
		t.Fatalf("capped amount %d (%v); want 4000", amount, err)
	}
	// nothing left at all: no row, still acknowledged
	stub.setRefund("re_c_more", "succeeded", 500, "eur", piRef)
	if code, _ := post(body("re_c_more", 500, "")); code != http.StatusOK || dashRows() != 2 {
		t.Fatalf("refund beyond a fully refunded payment: code %d, %d row(s); want 200 and 2", code, dashRows())
	}

	// an event that names an arena refund is never recorded as a dashboard one
	f2 := newPay03Order(t, srv.pgxPool, org, "stripe")
	pi2 := "pi_dashc2_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, f2.pay, pi2); err != nil {
		t.Fatal(err)
	}
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	res, err := srv.refundEngine().CreateBatch(ctx, refunds.BatchInput{
		OrgID: org, OrderID: f2.order, Reason: "dash", CancelTickets: true, IdempotencyKey: "dash-" + uuid.NewString(),
		Approved: true, Items: []refunds.Item{{TicketID: f2.tickets[0]}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var arenaRefund uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM refunds WHERE batch_id = $1`, res.BatchID).Scan(&arenaRefund); err != nil {
		t.Fatal(err)
	}
	stub.setRefund("re_c_ours", "succeeded", 2500, "eur", pi2)
	ours := fmt.Sprintf(`{"id":"evt_ours","type":"refund.updated","data":{"object":{"id":"re_c_ours","object":"refund","status":"succeeded","amount":2500,"currency":"eur","payment_intent":%q,"metadata":{"arena_refund_id":%q}}}}`,
		pi2, arenaRefund.String())
	code, m := post(ours)
	if code != http.StatusOK || m["processed"] == true {
		t.Fatalf("event of an arena refund not yet recorded: code %d, %v; want 200 not processed", code, m)
	}
	var n int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1 AND origin = 'provider_dashboard'`, f2.pay).Scan(&n)
	if n != 0 {
		t.Fatalf("an arena refund was recorded as a dashboard refund (%d row(s))", n)
	}
	if st, _ := f2.ticket(t, 0); st != "active" {
		t.Fatalf("ticket is %q; an unrecorded arena refund must not cancel it", st)
	}
}

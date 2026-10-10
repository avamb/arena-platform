//go:build integration

// refund_engine_integration_test.go — PAY-03 end to end through the REAL
// router: POST /v1/refunds and POST /v1/refunds/{id}/approve now drive the
// refund through the Stripe module (a stub Stripe, newStubStripe), resolve
// the pi_… behind a cs_… hosted session whose webhook never stored it,
// cancel the order's tickets only after Stripe accepted, and leave them
// valid when Stripe refuses. Payments of the seller's own site and of a
// provider without a refund module are refused before anything is written.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_pay02?sslmode=disable \
//	    go test -tags integration -run TestRefundEngineHTTP ./apps/backend/internal/platform/httpserver/
package httpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

type pay03Fixture struct {
	pool                         *pgxpool.Pool
	org, order, cs, session, pay uuid.UUID
	tickets                      [2]uuid.UUID
}

// newPay03Order seeds a paid public order of two 2500 tickets in org, paid
// by a 5000 payment of provider whose provider_payment_id is a cs_… and
// whose provider_charge_ref is empty, plus the org's Stripe config.
func newPay03Order(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, provider string) *pay03Fixture {
	t.Helper()
	ctx := context.Background()
	f := &pay03Fixture{pool: pool, org: org, order: uuid.New(), cs: uuid.New(), session: uuid.New(),
		tickets: [2]uuid.UUID{uuid.New(), uuid.New()}}
	suffix := uuid.NewString()[:8]
	venue, event, channel, res, tier := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute)
	t.Cleanup(func() { f.cleanup(t, venue, event, channel) })
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", strings.Fields(sql)[2], err)
		}
	}
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, venue, org, "V "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, event, org, "E "+suffix)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, f.session, event, venue, start)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
	      VALUES ($1, $2, 'Stalls', 'fixed', 2500, 'EUR', 1, true)`, tier, f.session)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, $2, 100, 2)`, f.session, tier)
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{}'::jsonb)`, channel, org, "C "+suffix)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, res, org, channel, f.session)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, f.cs, org, channel, res)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, paid_at)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 0, 0, 5000, now())`,
		f.order, org, channel, event, f.session, f.cs, res)
	for i, tk := range f.tickets {
		exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		      VALUES ($1, $2, $3, $4, 'buyer@example.com', $5, $6)`, tk, f.cs, f.session, tier, f.order, i)
		exec(`INSERT INTO order_items (order_id, ordinal, tier_id, ticket_id, unit_price, total) VALUES ($1, $2, $3, $4, 2500, 2500)`,
			f.order, i, tier, tk)
	}
	exec(`INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active)
	      VALUES ($1, 'stripe', 'test', '{"api_key":"sk_test_pay03","webhook_secret":"whsec_pay03"}'::jsonb, 'configured', true)
	      ON CONFLICT DO NOTHING`, org)
	ref := "cs_test_pay03_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	pi, err := gen.New(pool).InsertPaymentIntent(ctx, &f.cs, org, provider, &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	f.pay = pi.ID
	return f
}

func (f *pay03Fixture) cleanup(t *testing.T, venue, event, channel uuid.UUID) {
	ctx := context.Background()
	for _, s := range []struct {
		sql string
		arg any
	}{
		{`UPDATE tickets SET refund_id = NULL WHERE order_id = $1`, f.order},
		{`DELETE FROM refunds WHERE payment_intent_id = $1`, f.pay},
		{`DELETE FROM refund_batches WHERE order_id = $1`, f.order},
		{`DELETE FROM order_events WHERE order_id = $1`, f.order},
		{`DELETE FROM order_items WHERE order_id = $1`, f.order},
		{`DELETE FROM outbox_events WHERE aggregate_id = ANY($1::text[])`, []string{f.tickets[0].String(), f.tickets[1].String()}},
		{`DELETE FROM tickets WHERE order_id = $1`, f.order},
		{`DELETE FROM orders WHERE id = $1`, f.order},
		{`DELETE FROM payment_intents WHERE checkout_session_id = $1`, f.cs},
		{`DELETE FROM checkout_sessions WHERE id = $1`, f.cs},
		{`DELETE FROM reservations WHERE session_id = $1`, f.session},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.session},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.session},
		{`DELETE FROM sessions WHERE id = $1`, f.session},
		{`DELETE FROM events WHERE id = $1`, event},
		{`DELETE FROM venues WHERE id = $1`, venue},
		{`DELETE FROM sales_channels WHERE id = $1`, channel},
		{`DELETE FROM payment_provider_configs WHERE org_id = $1`, f.org},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			t.Logf("cleanup %q: %v", s.sql, err)
		}
	}
}

func (f *pay03Fixture) ticket(t *testing.T, i int) (status string, refundID *uuid.UUID) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), `SELECT status, refund_id FROM tickets WHERE id = $1`, f.tickets[i]).Scan(&status, &refundID); err != nil {
		t.Fatalf("ticket: %v", err)
	}
	return status, refundID
}

type stripeRefundCall struct {
	form url.Values
	key  string
}

func TestRefundEngineHTTP_ApproveDrivesStripeAndCancelsAfterwards(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pay03-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY03")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	ctx := context.Background()

	var (
		mu    sync.Mutex
		calls []stripeRefundCall
	)
	var answer func(w http.ResponseWriter)
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		mu.Lock()
		calls = append(calls, stripeRefundCall{form: form, key: r.Header.Get("Idempotency-Key")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer(w)
	}
	stub.sessionPI = "pi_pay03_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]

	create := func(f *pay03Fixture, amount int64) (int, map[string]any) {
		return pf.do(t, http.MethodPost, "/v1/refunds", key,
			fmt.Sprintf(`{"payment_intent_id":%q,"amount":%d,"currency":"EUR","reason":"show cancelled"}`, f.pay, amount))
	}
	refundOf := func(out map[string]any) map[string]any {
		r, _ := out["refund"].(map[string]any)
		return r
	}

	// ── 1. Stripe accepts: the whole payment back, both tickets cancelled.
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	answer = func(w http.ResponseWriter) { _, _ = fmt.Fprint(w, `{"id":"re_pay03_ok","status":"succeeded"}`) }
	st, out := create(f, 5000)
	if st != http.StatusCreated {
		t.Fatalf("create: %d %v", st, out)
	}
	refundID := refundOf(out)["id"].(string)
	st, out = pf.do(t, http.MethodPost, "/v1/refunds/"+refundID+"/approve", key, "{}")
	if st != http.StatusOK || refundOf(out)["state"] != "succeeded" || refundOf(out)["provider_refund_id"] != "re_pay03_ok" {
		t.Fatalf("approve: %d %v; want 200 succeeded", st, out)
	}
	if len(calls) != 1 {
		t.Fatalf("stripe refund calls = %d; want 1", len(calls))
	}
	c := calls[0]
	if c.key != refundID || c.form.Get("payment_intent") != stub.sessionPI || c.form.Get("amount") != "5000" {
		t.Fatalf("stripe call = key %q pi %q amount %q; want the refund id, the pi_ behind the cs_, 5000",
			c.key, c.form.Get("payment_intent"), c.form.Get("amount"))
	}
	var chargeRef string
	_ = srv.pgxPool.QueryRow(ctx, `SELECT COALESCE(provider_charge_ref, '') FROM payment_intents WHERE id = $1`, f.pay).Scan(&chargeRef)
	if chargeRef != stub.sessionPI {
		t.Fatalf("provider_charge_ref = %q; want the resolved %q stored", chargeRef, stub.sessionPI)
	}
	for i := range f.tickets {
		if s, link := f.ticket(t, i); s != "cancelled" || link == nil || link.String() != refundID {
			t.Fatalf("ticket %d = %s link %v; want cancelled and linked to %s", i, s, link, refundID)
		}
	}
	var orderStatus string
	var sold int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&orderStatus)
	_ = srv.pgxPool.QueryRow(ctx, `SELECT capacity_sold FROM inventory_ledger WHERE session_id = $1`, f.session).Scan(&sold)
	if orderStatus != "refunded" || sold != 0 {
		t.Fatalf("order %s, capacity_sold %d; want refunded and the places back on sale", orderStatus, sold)
	}
	// Approving again is a 409, and Stripe is not called twice.
	if st, _ = pf.do(t, http.MethodPost, "/v1/refunds/"+refundID+"/approve", key, "{}"); st != http.StatusConflict || len(calls) != 1 {
		t.Fatalf("second approve: %d, %d calls", st, len(calls))
	}
	legacyWebhookLeavesEngineRefundAlone(t, ts, srv.pgxPool, refundID)

	// ── 2. Stripe refuses: the refund fails, the tickets stay valid.
	f2 := newPay03Order(t, srv.pgxPool, org, "stripe")
	answer = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = fmt.Fprint(w, `{"error":{"type":"card_error","code":"expired_card","message":"The card has expired."}}`)
	}
	_, out = create(f2, 5000)
	refundID = refundOf(out)["id"].(string)
	st, out = pf.do(t, http.MethodPost, "/v1/refunds/"+refundID+"/approve", key, "{}")
	if st != http.StatusOK || refundOf(out)["state"] != "failed" {
		t.Fatalf("approve on a refusal: %d %v; want 200 failed", st, out)
	}
	var failureCode string
	_ = srv.pgxPool.QueryRow(ctx, `SELECT COALESCE(failure_code, '') FROM refunds WHERE id = $1`, refundID).Scan(&failureCode)
	if failureCode != "expired_card" {
		t.Fatalf("failure_code = %q; want Stripe's code", failureCode)
	}
	for i := range f2.tickets {
		if s, _ := f2.ticket(t, i); s != "active" {
			t.Fatalf("ticket %d = %s; a refused refund must leave it valid", i, s)
		}
	}

	// ── 3. Payments arena cannot refund through: nothing is written.
	for provider, want := range map[string]struct {
		status int
		code   string
	}{
		"manual": {http.StatusConflict, "refund.seller_site_order"},
		"flitt":  {http.StatusUnprocessableEntity, "refund.provider_not_supported"},
	} {
		fx := newPay03Order(t, srv.pgxPool, org, provider)
		st, out = create(fx, 2500)
		if st != want.status || prom0113Code(out) != want.code {
			t.Fatalf("%s: %d %v; want %d %s", provider, st, out, want.status, want.code)
		}
		var n int
		_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, fx.pay).Scan(&n)
		if n != 0 {
			t.Fatalf("%s: %d refunds written", provider, n)
		}
	}
}

// legacyWebhookLeavesEngineRefundAlone (PAY-03 review M3): the legacy
// POST /v1/refunds/webhook never moves a refund the engine drives — a
// correctly signed "failed" for a refund Stripe accepted is acknowledged
// with processed:false and writes nothing (no state change, no event row).
func legacyWebhookLeavesEngineRefundAlone(t *testing.T, ts *httptest.Server, pool *pgxpool.Pool, refundID string) {
	t.Helper()
	ctx := context.Background()
	body := []byte(fmt.Sprintf(`{"refund_id":%q,"provider_refund_id":"re_pay03_ok","event_type":"mock.refund.failed"}`, refundID))
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/refunds/webhook", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signStripe("whsec_pay03", body))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"processed":false`) || !strings.Contains(string(raw), "refund engine") {
		t.Fatalf("legacy webhook on an engine refund: %d %s; want 200 processed:false", resp.StatusCode, raw)
	}
	var state string
	var events int
	_ = pool.QueryRow(ctx, `SELECT state FROM refunds WHERE id = $1`, refundID).Scan(&state)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM refund_events WHERE refund_id = $1`, refundID).Scan(&events)
	if state != "succeeded" || events != 0 {
		t.Fatalf("after the legacy webhook: state %s, %d event rows; want succeeded and nothing written", state, events)
	}
}

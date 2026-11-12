//go:build integration

// checkout_invalid_email_integration_test.go — F-BR-03, through the real
// router and a live database.
//
// A buyer typed "assistant+fc043@abhteam.comRUN1109-FC FC-0" as their e-mail.
// checkout/start took the hold and created the order, then Stripe answered
// 400 email_invalid, arena answered 503 checkout.payment_start_failed and the
// seats stayed held for ~33 minutes. Now:
//
//   - a malformed address is refused with 400 checkout.invalid_email before
//     anything is written;
//   - a definitive provider refusal after the hold gives the hold back and
//     closes the order at once (an unknown outcome still leaves both).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestCheckoutInvalidEmail
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// refusingStripe answers every POST /v1/checkout/sessions with status and
// body, and counts the calls.
func refusingStripe(t *testing.T, status int, body string) (baseURL string, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/checkout/sessions") {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", calls
}

// startCheckoutAs drives checkout/start for two GA tickets with the given
// buyer e-mail.
func (f *hostedOrgFixture) startCheckoutAs(t *testing.T, srv *Server, email string) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        2,
		"return_url": hostedTicketsBaseURL + "/shows",
		"buyer":      map[string]any{"email": email, "name": "Invalid Email Buyer", "phone": f.buyerPhone},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// errorOf decodes an error envelope's code and details.
func errorOf(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode error envelope %s: %v", body, err)
	}
	return env.Error.Code, env.Error.Details
}

// countOrgRows counts the org's orders and checkout sessions.
func countOrgRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *hostedOrgFixture) (orders, checkouts int) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE org_id = $1`, f.orgID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM checkout_sessions WHERE org_id = $1`, f.orgID).Scan(&checkouts); err != nil {
		t.Fatalf("count checkout_sessions: %v", err)
	}
	return orders, checkouts
}

// TestCheckoutInvalidEmail_RefusedBeforeAnyHold is case (a): a malformed
// address answers 400 checkout.invalid_email, the provider is never called,
// and no hold, no checkout and no order exist afterwards.
func TestCheckoutInvalidEmail_RefusedBeforeAnyHold(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "bademail")
	defer f.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	before := f.footprint(t, ctx)

	for _, email := range []string{
		"assistant+fc043@abhteam.comRUN1109-FC FC-0",
		"assistant+fc043@abhteam.comRUN1109-FC",
		"buyer@localhost",
	} {
		code, body := f.startCheckoutAs(t, srv, email)
		if code != http.StatusBadRequest {
			t.Fatalf("%q: checkout/start = %d, want 400; body: %s", email, code, body)
		}
		errCode, details := errorOf(t, body)
		if errCode != "checkout.invalid_email" || details["field"] != "holder_email" {
			t.Fatalf("%q: error = %q %v, want checkout.invalid_email on holder_email", email, errCode, details)
		}
	}

	if len(stripe.sessions) != 0 {
		t.Errorf("the provider was called %d times for a malformed address", len(stripe.sessions))
	}
	f.assertNoInventoryTaken(t, ctx, before)
	if orders, checkouts := countOrgRows(t, ctx, pool, f); orders != 0 || checkouts != 0 {
		t.Errorf("orders = %d, checkout sessions = %d after refused addresses, want 0 and 0", orders, checkouts)
	}
}

// TestCheckoutInvalidEmail_ProviderRefusalReleasesTheHold is case (b): the
// address passes arena's check, Stripe refuses it after the hold committed.
// The answer is 400 checkout.invalid_email and the places, the checkout and
// the order are closed at once, not after the payment window.
func TestCheckoutInvalidEmail_ProviderRefusalReleasesTheHold(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "stripebad")
	defer f.cleanup()

	base, calls := refusingStripe(t, http.StatusBadRequest,
		`{"error":{"type":"invalid_request_error","code":"email_invalid","param":"customer_email","message":"Invalid email address"}}`)
	srv := buildHostedCheckoutServer(t, pool, base)
	before := f.footprint(t, ctx)

	code, body := f.startCheckoutAs(t, srv, f.buyerMail)
	if code != http.StatusBadRequest {
		t.Fatalf("checkout/start = %d, want 400; body: %s", code, body)
	}
	if errCode, details := errorOf(t, body); errCode != "checkout.invalid_email" || details["field"] != "holder_email" {
		t.Fatalf("error = %q %v, want checkout.invalid_email on holder_email", errCode, details)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}

	after := f.footprint(t, ctx)
	if after.unavailable != before.unavailable || after.capacityHeld != before.capacityHeld {
		t.Errorf("after the refusal: held places %d, held capacity %d; want %d and %d — the hold was not given back",
			after.unavailable, after.capacityHeld, before.unavailable, before.capacityHeld)
	}
	var resState, csState, orderStatus string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM reservations WHERE session_id = $1`, f.sessionID).Scan(&resState); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT state FROM checkout_sessions WHERE org_id = $1`, f.orgID).Scan(&csState); err != nil {
		t.Fatalf("read checkout session: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM orders WHERE org_id = $1`, f.orgID).Scan(&orderStatus); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if resState != "cancelled" || csState != "abandoned" || orderStatus != "cancelled" {
		t.Errorf("reservation %q, checkout %q, order %q; want cancelled, abandoned, cancelled",
			resState, csState, orderStatus)
	}
	var reason string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(e.payload->>'reason', '') FROM order_events e JOIN orders o ON o.id = e.order_id
		  WHERE o.org_id = $1 AND e.type = 'cancelled'`, f.orgID).Scan(&reason); err != nil {
		t.Fatalf("read the order's cancelled event: %v", err)
	}
	if reason != "payment_start_refused" {
		t.Errorf("cancelled reason = %q, want payment_start_refused", reason)
	}
	var intents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment_intents WHERE org_id = $1`, f.orgID).Scan(&intents); err != nil {
		t.Fatalf("count payment intents: %v", err)
	}
	if intents != 0 {
		t.Errorf("payment intents = %d, want 0 — nothing was created at the provider", intents)
	}
}

// TestCheckoutInvalidEmail_UnknownOutcomeKeepsTheHold guards the other side:
// a provider 5xx is an unknown outcome (the page may exist), so the answer
// stays 503 checkout.payment_start_failed and the hold is left to the sweeps.
func TestCheckoutInvalidEmail_UnknownOutcomeKeepsTheHold(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "stripe5xx")
	defer f.cleanup()

	base, _ := refusingStripe(t, http.StatusInternalServerError,
		`{"error":{"type":"api_error","message":"Something went wrong on Stripe's end."}}`)
	srv := buildHostedCheckoutServer(t, pool, base)
	before := f.footprint(t, ctx)

	code, body := f.startCheckoutAs(t, srv, f.buyerMail)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("checkout/start = %d, want 503; body: %s", code, body)
	}
	if errCode, _ := errorOf(t, body); errCode != "checkout.payment_start_failed" {
		t.Fatalf("error = %q, want checkout.payment_start_failed", errCode)
	}
	after := f.footprint(t, ctx)
	if after.capacityHeld == before.capacityHeld && after.unavailable == before.unavailable {
		t.Error("the hold was released after an unknown provider outcome; it must wait for the sweeps")
	}
	var orderStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE org_id = $1`, f.orgID).Scan(&orderStatus); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if orderStatus != "pending_payment" {
		t.Errorf("order status = %q, want pending_payment", orderStatus)
	}
}

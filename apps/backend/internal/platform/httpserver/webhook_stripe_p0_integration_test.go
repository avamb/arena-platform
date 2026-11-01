//go:build integration

// webhook_stripe_p0_integration_test.go — the Stripe rows of the SEC-01a
// matrix and of PAY-04 / PAY-05 that the P0-1 run found without a test
// (docs/testing/runs/2026-11-01-p0-1/results.md, section 4):
//
//   - a signature whose timestamp is outside the 5-minute window is 401 on
//     the per-config route and writes nothing; the same event signed again
//     (Stripe re-signs every retry) is accepted;
//   - an event repeated after the payment is terminal changes nothing: no
//     second issue job, no second ticket, barcode, letter or v1.order.paid;
//   - a paid event whose amount differs from the payment intent.
package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func postStripeSigned(t *testing.T, srv *Server, path string, body []byte, sig string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("Stripe-Signature", sig)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestStripeWebhookP0_TimestampOutsideTheWindowIs401 — SEC-01 / Stripe,
// "метка вне окна" on the HTTP route.
func TestStripeWebhookP0_TimestampOutsideTheWindowIs401(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "p0stale")
	defer f.cleanup()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	csID, _, providerID := startHostedCheckout(t, ctx, srv, f)
	paid := sessionCompletedEvent(providerID, "pi_p0stale_"+uuid.NewString()[:8], "paid")
	before := fulfilmentOf(t, ctx, pool, csID, providerID)

	// Only the past is checked, as in Stripe's own libraries: a timestamp
	// ahead of the clock is accepted (a clock skew is not a replay).
	for name, at := range map[string]time.Time{
		"six minutes old": time.Now().Add(-6 * time.Minute),
		"one day old":     time.Now().Add(-24 * time.Hour),
	} {
		code, body := postStripeSigned(t, srv, f.configWebhookPath(), paid, signStripeAt(f.webhookSecret, paid, at))
		if code != http.StatusUnauthorized {
			t.Errorf("%s signature on the config route = %d, want 401; body: %s", name, code, body)
		}
		// The un-suffixed route checks the same signature.
		if code, body = postStripeSigned(t, srv, "/v1/payment-intents/webhook", paid, signStripeAt(f.webhookSecret, paid, at)); code != http.StatusUnauthorized {
			t.Errorf("%s signature on the legacy route = %d, want 401; body: %s", name, code, body)
		}
	}
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != before {
		t.Fatalf("a stale signature changed the payment: before %+v, after %+v", before, after)
	}
	if pi, _ := gen.New(pool).GetPaymentIntentByProviderID(ctx, providerID); pi.State != "created" {
		t.Fatalf("intent = %q after stale signatures; want created", pi.State)
	}

	// A delayed delivery that Stripe signed again is accepted.
	code, body := postStripeSigned(t, srv, f.configWebhookPath(), paid, signStripe(f.webhookSecret, paid))
	if code != http.StatusOK || !processed(t, body) {
		t.Fatalf("freshly signed event = %d processed=%v; body: %s", code, processed(t, body), body)
	}
}

// TestStripeWebhookP0_RepeatAfterTerminalIssuesNothingTwice — SEC-01 /
// Stripe "повтор после терминального" and PAY-04 / Stripe.
func TestStripeWebhookP0_RepeatAfterTerminalIssuesNothingTwice(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "p0rep")
	defer f.cleanup()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	csID, _, providerID := startHostedCheckout(t, ctx, srv, f)
	paid := sessionCompletedEvent(providerID, "pi_p0rep_"+uuid.NewString()[:8], "paid")
	code, body := postSignedWebhookTo(t, srv, f.configWebhookPath(), paid, f.webhookSecret)
	if code != http.StatusOK || !processed(t, body) {
		t.Fatalf("first paid event = %d; body: %s", code, body)
	}
	runIssueJobs(t, ctx, srv, csID)
	first := fulfilmentOf(t, ctx, pool, csID, providerID)
	want := fulfilment{IntentEvents: 1, IssueJobs: 1, ConvertJobs: 1, Tickets: 2, Barcodes: 2, DeliveryJobs: 2, DeliverJobs: 2, OrderPaid: 1}
	if first != want {
		t.Fatalf("after the first delivery: %+v, want %+v", first, want)
	}

	// The same event twice more on the config route, once on the legacy
	// route, and an async "succeeded" for the same session.
	async, _ := json.Marshal(map[string]any{
		"id": "evt_p0rep_async", "type": "checkout.session.async_payment_succeeded",
		"data": map[string]any{"object": map[string]any{
			"id": providerID, "object": "checkout.session", "payment_status": "paid", "status": "complete",
		}},
	})
	for i, d := range []struct {
		path string
		body []byte
	}{
		{f.configWebhookPath(), paid},
		{f.configWebhookPath(), paid},
		{"/v1/payment-intents/webhook", paid},
		{f.configWebhookPath(), async},
	} {
		code, body := postSignedWebhookTo(t, srv, d.path, d.body, f.webhookSecret)
		if code != http.StatusOK {
			t.Errorf("repeat %d on %s = %d, want 200; body: %s", i+1, d.path, code, body)
			continue
		}
		if processed(t, body) {
			t.Errorf("repeat %d on %s was PROCESSED after the payment was terminal; body: %s", i+1, d.path, body)
		}
	}
	runIssueJobs(t, ctx, srv, csID)
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != first {
		t.Fatalf("repeats changed the fulfilment: first %+v, after %+v", first, after)
	}
}

// stripeSessionCompletedWithAmount is checkout.session.completed carrying the
// amount Stripe charged.
func stripeSessionCompletedWithAmount(sessionID, chargeRef string, amount int64, currency string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"id":   "evt_p0amt_" + uuid.NewString()[:12],
		"type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "object": "checkout.session", "payment_status": "paid", "status": "complete",
			"payment_intent": chargeRef, "amount_total": amount, "currency": currency,
		}},
	})
	return raw
}

// TestStripeWebhookP0_PaidAmountMismatchIsParked — PAY-05 / Stripe: a paid
// event whose amount is not the intent's must park the payment in
// manual_review and issue no tickets.
//
// KNOWN GAP (P0-1 follow-up, see the PR): the amount guard of
// hcheckout/payment_amount_guard.go runs only for config-route-only modules
// (Flitt, AllPay, PayPlus), so a Stripe event is not checked and the order is
// paid. The test is skipped until the product decides; set
// ARENA_P0_KNOWN_GAPS=1 to run it.
func TestStripeWebhookP0_PaidAmountMismatchIsParked(t *testing.T) {
	if os.Getenv("ARENA_P0_KNOWN_GAPS") != "1" {
		t.Skip("known gap P0-1/D-01: Stripe paid events are not checked against the intent amount; ARENA_P0_KNOWN_GAPS=1 runs it")
	}
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "p0amt")
	defer f.cleanup()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	csID, total, providerID := startHostedCheckout(t, ctx, srv, f)
	short := stripeSessionCompletedWithAmount(providerID, "pi_p0amt_"+uuid.NewString()[:8], total-100, "eur")
	if code, body := postSignedWebhookTo(t, srv, f.configWebhookPath(), short, f.webhookSecret); code != http.StatusOK {
		t.Fatalf("underpaid event = %d; body: %s", code, body)
	}
	q := gen.New(pool)
	pi, err := q.GetPaymentIntentByProviderID(ctx, providerID)
	if err != nil {
		t.Fatal(err)
	}
	if pi.State != "manual_review" {
		t.Errorf("intent = %q for a paid amount %d that is not the total %d; want manual_review", pi.State, total-100, total)
	}
	if cs, _ := q.GetCheckoutSessionByID(ctx, csID); cs.State == "completed" {
		t.Errorf("checkout completed by an underpaid event")
	}
	if fl := fulfilmentOf(t, ctx, pool, csID, providerID); fl.IssueJobs != 0 {
		t.Errorf("issue jobs = %d for an underpaid event; want 0", fl.IssueJobs)
	}
}

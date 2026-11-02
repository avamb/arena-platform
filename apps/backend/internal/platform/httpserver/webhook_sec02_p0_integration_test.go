//go:build integration

// webhook_sec02_p0_integration_test.go — SEC-02 of the P0 gates
// (docs/testing/runs/2026-11-01-p0-1/results.md, section 4): a wrong and an
// empty signature on the three other webhook routes are refused, and the
// state behind them does not change.
//
// Protocols, read from the code:
//   - POST /v1/refunds/webhook: Stripe-Signature (t=,v1=), the org's
//     webhook secret, else STRIPE_WEBHOOK_SECRET; 5-minute window.
//   - POST /v1/billing/stripe/webhook: Stripe-Signature with
//     BILLING_STRIPE_WEBHOOK_SECRET; 5-minute window.
//   - POST /v1/billing/bank/webhook/{provider}: X-Arena-Signature:
//     sha256=<hex HMAC-SHA256 of the raw body> with BILLING_BANK_WEBHOOK_SECRET.
//     No timestamp in the protocol, so no staleness case; a replay is
//     answered by the (provider, external id) dedup.
package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const sec02PlatformStripeSecret = "whsec_sec02_platform"

// TestSEC02_RefundWebhookRefusesWrongAndEmptySignatures — /v1/refunds/webhook.
func TestSEC02_RefundWebhookRefusesWrongAndEmptySignatures(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "sec02r")
	defer f.cleanup()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL(), func(c *config.Config) {
		c.StripeWebhookSecret = sec02PlatformStripeSecret
	})

	// A paid payment and one refund row of it.
	csID, total, providerID := startHostedCheckout(t, ctx, srv, f)
	if code, body := postSignedWebhookTo(t, srv, f.configWebhookPath(),
		sessionCompletedEvent(providerID, "pi_sec02_"+uuid.NewString()[:8], "paid"), f.webhookSecret); code != http.StatusOK {
		t.Fatalf("paid event = %d; body: %s", code, body)
	}
	var intentID, orderID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM payment_intents WHERE provider_payment_id = $1`, providerID).Scan(&intentID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM orders WHERE checkout_session_id = $1`, csID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	var refundID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement, requested_by)
		VALUES ($1, $2, $3, $4, 'EUR', 'sec02 test', 'requested', 'provider', 'sec02') RETURNING id`,
		intentID, f.orgID, orderID, total).Scan(&refundID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM refund_events WHERE refund_id = $1`, refundID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM refunds WHERE id = $1`, refundID)
	}()

	providerRefundID := "re_sec02_" + uuid.NewString()[:12]
	body, _ := json.Marshal(map[string]any{
		"provider_refund_id": providerRefundID,
		"event_type":         "refund.failed",
		"refund_id":          refundID.String(),
	})
	snapshot := func() string {
		t.Helper()
		var state string
		var events int
		if err := pool.QueryRow(ctx, `SELECT r.state,
			(SELECT count(*) FROM refund_events e WHERE e.refund_id = r.id)
			FROM refunds r WHERE r.id = $1`, refundID).Scan(&state, &events); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s/%d", state, events)
	}
	before := snapshot()

	for _, c := range []struct{ name, sig string }{
		{"empty signature", ""},
		{"another secret", signStripe("whsec_someone_else", body)},
		{"the platform secret while the org has its own", signStripe(sec02PlatformStripeSecret, body)},
		{"six minutes old", signStripeAt(f.webhookSecret, body, time.Now().Add(-6*time.Minute))},
		{"not a signature", "garbage"},
	} {
		if code, resp := postStripeSigned(t, srv, "/v1/refunds/webhook", body, c.sig); code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401; body: %s", c.name, code, resp)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("refused deliveries changed the refund: %s -> %s", before, after)
	}

	// The gate is the signature: the org's own secret passes it. An event
	// type the route does not map is acknowledged and moves nothing.
	unknown, _ := json.Marshal(map[string]any{
		"provider_refund_id": providerRefundID, "event_type": "refund.p0_not_mapped", "refund_id": refundID.String(),
	})
	if code, resp := postStripeSigned(t, srv, "/v1/refunds/webhook", unknown, signStripe(f.webhookSecret, unknown)); code != http.StatusOK {
		t.Fatalf("correctly signed delivery = %d, want 200; body: %s", code, resp)
	}
	if after := snapshot(); after != before {
		t.Fatalf("an unmapped event changed the refund: %s -> %s", before, after)
	}
}

// TestSEC02_BillingStripeWebhookRefusesWrongEmptyAndStaleSignatures —
// /v1/billing/stripe/webhook.
func TestSEC02_BillingStripeWebhookRefusesWrongEmptyAndStaleSignatures(t *testing.T) {
	e := newAcctEnv(t)
	e.cardFee(t)
	p := e.pack(t, 2)
	code, inv := e.order(t, e.ownerA, str(p["id"]), "stripe_card", "sec02-card-"+uuid.NewString())
	if code != http.StatusCreated {
		t.Fatalf("order: %d %v", code, inv["_raw"])
	}
	invID := str(inv["id"])
	var session string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT stripe_checkout_session_id FROM invoices WHERE id = $1`, invID).Scan(&session); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"id":%q,"type":"checkout.session.completed","data":{"object":{"id":%q,"payment_status":"paid","amount_total":%d,"currency":"eur","payment_intent":"pi_sec02","metadata":{"arena_invoice_id":%q}}}}`,
		"evt_sec02_"+uuid.NewString(), session, num(inv["total_minor"]), invID)
	state := func() string {
		t.Helper()
		var st string
		var events int
		if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT state,
			(SELECT count(*) FROM billing_stripe_events WHERE invoice_id = $1) FROM invoices WHERE id = $1`, invID).Scan(&st, &events); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s/%d/%d", st, events, e.lots(t, invID))
	}
	before := state()
	for _, c := range []struct{ name, sig string }{
		{"empty signature", ""},
		{"another secret", signStripe("whsec_wrong", []byte(body))},
		{"six minutes old", signStripeAt(acctStripeWebhookSecret, []byte(body), time.Now().Add(-6*time.Minute))},
		{"not a signature", "t=1,v1=00"},
	} {
		hdr := map[string]string{}
		if c.sig != "" {
			hdr["Stripe-Signature"] = c.sig
		}
		if code, res := e.req(t, http.MethodPost, "/v1/billing/stripe/webhook", "", hdr, body); code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401; body: %v", c.name, code, res["_raw"])
		}
	}
	if after := state(); after != before {
		t.Fatalf("refused deliveries changed the invoice: %s -> %s", before, after)
	}
	if code, res := e.stripeEvent(t, acctStripeWebhookSecret, body); code != http.StatusOK || res["outcome"] != "paid" {
		t.Fatalf("correctly signed event = %d %v", code, res["_raw"])
	}
}

// TestSEC02_BankWebhookRefusesWrongAndEmptySignatures —
// /v1/billing/bank/webhook/{provider}.
func TestSEC02_BankWebhookRefusesWrongAndEmptySignatures(t *testing.T) {
	e := newAcctEnv(t)
	p := e.pack(t, 1)
	code, inv := e.order(t, e.ownerA, str(p["id"]), "bank_transfer", "sec02-bank-"+uuid.NewString())
	if code != http.StatusCreated {
		t.Fatalf("order: %d %v", code, inv["_raw"])
	}
	invID, ref := str(inv["id"]), str(inv["payment_reference"])
	txID := "tx-sec02-" + uuid.NewString()
	body := transferBody(txID, ref, "EUR", num(inv["total_minor"]))
	mac := func(secret, b string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(b))
		return hex.EncodeToString(m.Sum(nil))
	}
	stored := func() string {
		t.Helper()
		var st string
		var txs int
		if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT state,
			(SELECT count(*) FROM billing_bank_transactions WHERE external_id = $2) FROM invoices WHERE id = $1`, invID, txID).Scan(&st, &txs); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s/%d/%d", st, txs, e.lots(t, invID))
	}
	before := stored()
	tampered := strings.Replace(body, fmt.Sprint(num(inv["total_minor"])), fmt.Sprint(num(inv["total_minor"])+1), 1)
	for _, c := range []struct{ name, sig, body string }{
		{"empty signature", "", body},
		{"another secret", "sha256=" + mac("wrong-bank-secret", body), body},
		{"no sha256= prefix", mac(acctBankSecret, body), body},
		{"not hex", "sha256=zz", body},
		{"body changed after signing", "sha256=" + mac(acctBankSecret, body), tampered},
	} {
		hdr := map[string]string{}
		if c.sig != "" {
			hdr["X-Arena-Signature"] = c.sig
		}
		if code, res := e.req(t, http.MethodPost, "/v1/billing/bank/webhook/testbank", "", hdr, c.body); code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401; body: %v", c.name, code, res["_raw"])
		}
	}
	if after := stored(); after != before {
		t.Fatalf("refused deliveries changed the invoice: %s -> %s", before, after)
	}
	if code, res := e.bank(t, "testbank", body, acctBankSecret); code != http.StatusOK || num(res["matched"]) != 1 {
		t.Fatalf("correctly signed transfer = %d %v", code, res["_raw"])
	}
}

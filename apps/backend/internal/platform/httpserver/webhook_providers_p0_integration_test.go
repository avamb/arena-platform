//go:build integration

// webhook_providers_p0_integration_test.go — the Flitt, AllPay and PayPlus
// rows of the SEC-01a matrix and of PAY-04 / PAY-05 that the P0-1 run found
// without a test (docs/testing/runs/2026-11-01-p0-1/results.md, section 4).
// Each provider is tested only against ITS protocol: none of them signs a
// timestamp, so there is no staleness case here.
package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/flitt"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/payplus"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func flittP0Server(t *testing.T, f *flittFixture) *Server {
	t.Helper()
	pool := integrationPool(t)
	stub := newStubFlittServer(t, f.paymentKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.flittAPIBaseURL = stub.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"
	return srv
}

// TestFlittWebhookP0_ForeignMerchantIDIs401 — SEC-01 / Flitt, "чужой
// merchant_id": a callback signed with the org's own payment key but naming
// another merchant is refused before anything is read or written.
func TestFlittWebhookP0_ForeignMerchantIDIs401(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	a := newFlittFixture(t, ctx, pool, "p0fma", "EUR")
	defer a.cleanup()
	b := newFlittFixture(t, ctx, pool, "p0fmb", "EUR")
	defer b.cleanup()
	srv := flittP0Server(t, a)

	csID, total, providerID := startHostedCheckout(t, ctx, srv, a.hostedOrgFixture)
	before := fulfilmentOf(t, ctx, pool, csID, providerID)

	for name, merchant := range map[string]string{
		"merchant of another organization": b.merchantID,
		"unknown merchant":                 "9999999",
	} {
		body := a.callback(t, csID.String(), "approved", total, "EUR", a.paymentKey, merchant)
		if code, resp := postFlittCallback(t, srv, a.configWebhookPath(), body); code != http.StatusUnauthorized {
			t.Errorf("%s under org A's key = %d, want 401; body: %s", name, code, resp)
		}
	}
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != before {
		t.Fatalf("a foreign merchant moved the payment: before %+v, after %+v", before, after)
	}
	if pi, _ := gen.New(pool).GetPaymentIntentByProviderID(ctx, providerID); pi.State != "created" {
		t.Fatalf("intent = %q; want created", pi.State)
	}
}

// TestFlittWebhookP0_RepeatedApprovedIssuesNothingTwice — SEC-01 / Flitt
// "повтор approved" and PAY-04 / Flitt.
func TestFlittWebhookP0_RepeatedApprovedIssuesNothingTwice(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newFlittFixture(t, ctx, pool, "p0frep", "EUR")
	defer f.cleanup()
	srv := flittP0Server(t, f)

	csID, total, providerID := startHostedCheckout(t, ctx, srv, f.hostedOrgFixture)
	approved := f.callback(t, csID.String(), "approved", total, "EUR", f.paymentKey, f.merchantID)
	code, body := postFlittCallback(t, srv, f.configWebhookPath(), approved)
	if code != http.StatusOK || !processed(t, body) {
		t.Fatalf("first approved = %d; body: %s", code, body)
	}
	runIssueJobs(t, ctx, srv, csID)
	first := fulfilmentOf(t, ctx, pool, csID, providerID)
	want := fulfilment{IntentEvents: 1, IssueJobs: 1, ConvertJobs: 1, Tickets: 2, Barcodes: 2, DeliveryJobs: 2, DeliverJobs: 2, OrderPaid: 1}
	if first != want {
		t.Fatalf("after the first callback: %+v, want %+v", first, want)
	}

	// Flitt retries until it sees 200: the same callback twice, then a
	// re-signed copy with another payment_id (a second notification of the
	// same approved order).
	resigned := map[string]any{}
	_ = json.Unmarshal(approved, &resigned)
	resigned["payment_id"] = json.Number("805243693")
	delete(resigned, "signature")
	resigned["signature"] = flitt.Sign(f.paymentKey, resigned)
	again, _ := json.Marshal(resigned)
	for i, b := range [][]byte{approved, approved, again} {
		code, resp := postFlittCallback(t, srv, f.configWebhookPath(), b)
		if code != http.StatusOK {
			t.Errorf("repeat %d = %d, want 200 (Flitt retries anything else); body: %s", i+1, code, resp)
			continue
		}
		if processed(t, resp) {
			t.Errorf("repeat %d was PROCESSED again; body: %s", i+1, resp)
		}
	}
	runIssueJobs(t, ctx, srv, csID)
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != first {
		t.Fatalf("repeats changed the fulfilment: first %+v, after %+v", first, after)
	}
}

// TestFlittWebhookP0_PaidAmountMismatchIsParked — PAY-05 / Flitt: the
// amount, not only the currency, is checked.
func TestFlittWebhookP0_PaidAmountMismatchIsParked(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newFlittFixture(t, ctx, pool, "p0famt", "EUR")
	defer f.cleanup()
	srv := flittP0Server(t, f)

	csID, total, providerID := startHostedCheckout(t, ctx, srv, f.hostedOrgFixture)
	short := f.callback(t, csID.String(), "approved", total-100, "EUR", f.paymentKey, f.merchantID)
	if code, body := postFlittCallback(t, srv, f.configWebhookPath(), short); code != http.StatusOK {
		t.Fatalf("underpaid callback = %d; body: %s", code, body)
	}
	q := gen.New(pool)
	pi, err := q.GetPaymentIntentByProviderID(ctx, providerID)
	if err != nil {
		t.Fatal(err)
	}
	if pi.State != "manual_review" || pi.FailureCode == nil || *pi.FailureCode != "paid_amount_mismatch" {
		t.Fatalf("intent = %s %v; want manual_review paid_amount_mismatch", pi.State, pi.FailureCode)
	}
	if cs, _ := q.GetCheckoutSessionByID(ctx, csID); cs.State == "completed" {
		t.Errorf("checkout completed by an underpaid callback")
	}
	if fl := fulfilmentOf(t, ctx, pool, csID, providerID); fl.IssueJobs != 0 || fl.Tickets != 0 {
		t.Errorf("underpaid callback queued %d issue jobs and %d tickets; want none", fl.IssueJobs, fl.Tickets)
	}
}

// TestAllPayWebhookP0_ForeignKeyOnForeignConfigIs401 — SEC-01 / AllPay,
// "чужой ключ на чужом конфиге": a webhook for org A's order signed with org
// B's key is refused on org A's config, and a webhook signed with A's key is
// refused on B's config; neither moves anything. Without any sign it is 401
// too.
func TestAllPayWebhookP0_ForeignKeyOnForeignConfigIs401(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	a := newAllPayFixture(t, ctx, pool, "p0apa")
	defer a.cleanup()
	b := newAllPayFixture(t, ctx, pool, "p0apb")
	defer b.cleanup()
	stub := newStubAllPayServer(t, a, b)
	srv := allpayServer(t, pool, stub)

	csID, total, providerID := startHostedCheckout(t, ctx, srv, a.hostedOrgFixture)
	stub.markPaid(t, csID.String())
	before := fulfilmentOf(t, ctx, pool, csID, providerID)

	underB := b.webhook(t, csID.String(), 1, total, b.apiKey)
	underA := a.webhook(t, csID.String(), 1, total, a.apiKey)
	unsigned := map[string]any{}
	_ = json.Unmarshal(underA, &unsigned)
	delete(unsigned, "sign")
	noSign, _ := json.Marshal(unsigned)
	for _, c := range []struct {
		name string
		path string
		body []byte
	}{
		{"org B's key on org A's config", a.configWebhookPath(), underB},
		{"org A's key on org B's config", b.configWebhookPath(), underA},
		{"no sign on org A's config", a.configWebhookPath(), noSign},
	} {
		if code, resp := postAllPayWebhook(t, srv, c.path, c.body); code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401; body: %s", c.name, code, resp)
		}
	}
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != before {
		t.Fatalf("a foreign key moved the payment: before %+v, after %+v", before, after)
	}
	if pi, _ := gen.New(pool).GetPaymentIntentByProviderID(ctx, providerID); pi.State != "created" {
		t.Fatalf("intent = %q; want created", pi.State)
	}
	// The org's own key still pays.
	if code, resp := postAllPayWebhook(t, srv, a.configWebhookPath(), underA); code != http.StatusOK || !processed(t, resp) {
		t.Fatalf("org A's own webhook = %d; body: %s", code, resp)
	}
}

// TestPayPlusWebhookP0_WrongHashIs401 — SEC-01 / PayPlus, "неверный hash" on
// the HTTP route: another key, an empty header and a body changed after
// signing are all refused and move nothing.
func TestPayPlusWebhookP0_WrongHashIs401(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPayPlusFixture(t, ctx, pool, "p0pph")
	defer f.cleanup()
	stub := newStubPayPlusServer(t, f.apiKey, f.secretKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.payplusAPIBaseURL = stub.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"

	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/p0")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d; body: %s", code, body)
	}
	var start hostedStartResponse
	_ = json.Unmarshal(body, &start)
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, _ := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
	pageUID := strings.TrimPrefix(start.RedirectURL, "https://paymentsdev.payplus.test/")
	before := fulfilmentOf(t, ctx, pool, csID, pageUID)

	paid := payplusCallback(t, pageUID, uuid.NewString(), "000", *cs.Total, csID.String())
	// The same callback with another amount, carrying the hash of the first.
	tampered := payplusCallback(t, pageUID, uuid.NewString(), "000", *cs.Total+500, csID.String())
	cases := []struct {
		name string
		body []byte
		hash string
	}{
		{"another secret", paid, payplus.Sign("pp_secret_of_someone_else", paid)},
		{"empty hash header", paid, ""},
		{"garbage hash", paid, "not-base64"},
		{"body changed after signing", tampered, payplus.Sign(f.secretKey, paid)},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, f.configWebhookPath(), bytes.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "PayPlus")
		if c.hash != "" {
			req.Header.Set("hash", c.hash)
		}
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401; body: %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if after := fulfilmentOf(t, ctx, pool, csID, pageUID); after != before {
		t.Fatalf("a wrong hash moved the payment: before %+v, after %+v", before, after)
	}
	if pi, _ := gen.New(pool).GetPaymentIntentByProviderID(ctx, pageUID); pi.State != "created" {
		t.Fatalf("intent = %q; want created", pi.State)
	}
	if code, resp := postPayPlusCallback(t, srv, f.configWebhookPath(), paid, f.secretKey); code != http.StatusOK {
		t.Fatalf("correctly signed callback = %d; body: %s", code, resp)
	}
	if pi, _ := gen.New(pool).GetPaymentIntentByProviderID(ctx, pageUID); pi.State != "succeeded" {
		t.Fatalf("intent = %q after the signed callback; want succeeded", pi.State)
	}
}

//go:build integration

// embedded_checkout_integration_test.go — spec 65 stage E2, through the real
// router with only Stripe stubbed (newStubStripe):
//
//   - an embedded (ui_mode=elements) checkout answers client_secret and the
//     publishable key, and the checkout.session.completed webhook pays it;
//   - a widget without capabilities, a channel set to stripe_ui=hosted and a
//     deployment with STRIPE_ELEMENTS_ENABLED off all get the hosted page;
//   - POST /v1/public/checkout/{token}/hosted expires the embedded session
//     FIRST, refuses while it is paid, and the "old session paid after the
//     new one was created" race pays the order once, closes the other page
//     and parks a second payment for the ops alert;
//   - payment_intent.payment_failed by pi_… then completed by cs_… pays;
//   - the status hands out the client secret only while pending with a
//     live hold, never stores it, and is sent with no-store.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

const embeddedAPIBase = "https://api.arena-integration.test"

// embeddedFixture is a hosted-checkout organizer whose Stripe config also
// holds a publishable key.
func embeddedFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) (*hostedOrgFixture, string) {
	t.Helper()
	f := newHostedOrgFixture(t, ctx, pool, label)
	pk := "pk_test_" + f.orgID.String()[:8]
	if _, err := pool.Exec(ctx,
		`UPDATE payment_provider_configs SET secrets = secrets || jsonb_build_object('publishable_key', $2::text) WHERE id = $1`,
		f.configID, pk); err != nil {
		f.cleanup()
		t.Fatalf("set publishable_key: %v", err)
	}
	return f, pk
}

func buildEmbeddedServer(t *testing.T, pool *pgxpool.Pool, stripeBase string, enabled bool) *Server {
	t.Helper()
	return buildHostedCheckoutServer(t, pool, stripeBase, func(c *config.Config) {
		c.StripeElementsEnabled = enabled
		c.APIPublicURL = embeddedAPIBase
	})
}

type embeddedStartResponse struct {
	RedirectURL   string `json:"redirect_url"`
	CheckoutToken string `json:"checkout_token"`
	CheckoutSess  struct {
		ID string `json:"id"`
	} `json:"checkout_session"`
	Payment *struct {
		Provider       string `json:"provider"`
		Mode           string `json:"mode"`
		ClientSecret   string `json:"client_secret"`
		PublishableKey string `json:"publishable_key"`
	} `json:"payment"`
}

// startOne starts a one-ticket checkout with the given capabilities.
func startOne(t *testing.T, srv *Server, f *hostedOrgFixture, caps []string) embeddedStartResponse {
	t.Helper()
	payload := map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        1,
		"return_url": hostedTicketsBaseURL + "/shows/embedded",
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Embedded Buyer",
			"phone": f.buyerPhone,
		},
	}
	if caps != nil {
		payload["capabilities"] = caps
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var out embeddedStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if strings.Contains(rec.Body.String(), `"payment":`) != (out.Payment != nil) {
		t.Fatalf("payment key presence mismatch: %s", rec.Body.String())
	}
	return out
}

type embeddedStatus struct {
	Status     string  `json:"status"`
	PaymentURL *string `json:"payment_url"`
	Payment    *struct {
		Provider       string `json:"provider"`
		Mode           string `json:"mode"`
		ClientSecret   string `json:"client_secret"`
		PublishableKey string `json:"publishable_key"`
	} `json:"payment"`
}

// getEmbeddedStatus reads the order status WITH ?include=payment, the way
// the widget asks for the embedded form.
func getEmbeddedStatus(t *testing.T, srv *Server, token string) embeddedStatus {
	t.Helper()
	st, _ := getEmbeddedStatusAt(t, srv, "/v1/public/checkout/"+token+"?include=payment")
	return st
}

func getEmbeddedStatusAt(t *testing.T, srv *Server, path string) (embeddedStatus, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var out embeddedStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return out, rec.Body.String()
}

func postHostedFallback(t *testing.T, srv *Server, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/checkout/"+token+"/hosted", nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("/hosted Cache-Control = %q, want no-store", cc)
	}
	return rec.Code, out
}

func intentsOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID string) []gen.PaymentIntentRow {
	t.Helper()
	id := uuid.MustParse(csID)
	rows, err := gen.New(pool).ListPaymentIntentsByCheckout(ctx, id)
	if err != nil {
		t.Fatalf("ListPaymentIntentsByCheckout: %v", err)
	}
	return rows
}

func countIssueJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets'
		AND payload->>'checkout_session_id' = $1`, csID).Scan(&n); err != nil {
		t.Fatalf("count issue jobs: %v", err)
	}
	return n
}

func TestEmbeddedCheckout_StartReturnsClientSecretAndCompletedWebhookPays(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, pk := embeddedFixture(t, ctx, pool, "ema")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := startOne(t, srv, f, []string{"stripe_elements"})
	if start.Payment == nil {
		t.Fatal("no payment block for an embeddable checkout")
	}
	if start.RedirectURL != "" {
		t.Errorf("redirect_url = %q for an embedded payment; want empty", start.RedirectURL)
	}
	if start.Payment.Provider != "stripe" || start.Payment.Mode != "elements" || start.Payment.PublishableKey != pk ||
		!strings.Contains(start.Payment.ClientSecret, "_secret_") {
		t.Fatalf("payment block = %+v", *start.Payment)
	}

	form := stripe.lastForm(t)
	if form.Get("ui_mode") != "elements" || form.Has("success_url") || form.Has("cancel_url") {
		t.Errorf("elements form: ui_mode=%q success_url=%v cancel_url=%v", form.Get("ui_mode"), form.Has("success_url"), form.Has("cancel_url"))
	}
	if form.Get("allowed_payment_method_types[0]") != "card" || form.Get("allowed_payment_method_types[1]") != "link" || form.Has("payment_method_types[0]") {
		t.Errorf("payment methods: %v", form)
	}
	ret := form.Get("return_url")
	if !strings.HasPrefix(ret, embeddedAPIBase+"/v1/public/payment-return?") || !strings.Contains(ret, "checkout_token="+start.CheckoutToken) {
		t.Errorf("return_url = %q; want arena's payment-return hop with the checkout_token", ret)
	}
	if form.Get("customer_email") != f.buyerMail || form.Get("client_reference_id") != start.CheckoutSess.ID ||
		form.Get("expires_at") == "" || form.Get("metadata[arena_checkout_session_id]") != start.CheckoutSess.ID {
		t.Errorf("session fields: %v", form)
	}

	intents := intentsOf(t, ctx, pool, start.CheckoutSess.ID)
	if len(intents) != 1 {
		t.Fatalf("intents = %d, want 1", len(intents))
	}
	pi := intents[0]
	if pi.ProviderPaymentID == nil || !strings.HasPrefix(*pi.ProviderPaymentID, "cs_") || pi.HostedCheckoutURL != nil {
		t.Fatalf("intent = %+v; want the cs_ id and no hosted page", pi)
	}
	if pi.ClientSecret != nil {
		t.Fatal("the client secret was stored on payment_intents; it must never be")
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment_intents WHERE client_secret = $1 OR hosted_checkout_url = $1`,
		start.Payment.ClientSecret).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("client secret found in the database: %d (%v)", leaked, err)
	}

	st := getEmbeddedStatus(t, srv, start.CheckoutToken)
	if st.Status != "pending" || st.PaymentURL != nil || st.Payment == nil ||
		st.Payment.ClientSecret != start.Payment.ClientSecret || st.Payment.PublishableKey != pk {
		t.Fatalf("pending status = %+v (payment %+v)", st, st.Payment)
	}

	code, body := postSignedWebhook(t, srv, sessionCompletedEvent(*pi.ProviderPaymentID, "pi_ema_"+uuid.NewString()[:8], "paid"), f.webhookSecret)
	if code != http.StatusOK || !strings.Contains(string(body), `"checkout_completed":true`) {
		t.Fatalf("paid webhook = %d %s", code, body)
	}
	ord, err := gen.New(pool).GetOrderByCheckoutSession(ctx, uuid.MustParse(start.CheckoutSess.ID))
	if err != nil || ord.Status != "paid" {
		t.Fatalf("order after payment: %+v (%v)", ord.Status, err)
	}
	if n := countIssueJobs(t, ctx, pool, start.CheckoutSess.ID); n != 1 {
		t.Errorf("issue_tickets jobs = %d, want 1", n)
	}
	st = getEmbeddedStatus(t, srv, start.CheckoutToken)
	if st.Status != "paid" || st.Payment != nil {
		t.Fatalf("paid status = %+v; the payment block must be gone", st)
	}
}

func TestEmbeddedCheckout_HostedForOldClientsChannelsAndSwitch(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "emb")
	defer f.cleanup()
	stripe := newStubStripe(t)

	assertHosted := func(name string, srv *Server, caps []string) {
		t.Helper()
		start := startOne(t, srv, f, caps)
		if start.Payment != nil || !strings.HasPrefix(start.RedirectURL, "https://checkout.stripe.test/") {
			t.Errorf("%s: want the hosted page, got redirect %q payment %+v", name, start.RedirectURL, start.Payment)
		}
		if form := stripe.lastForm(t); form.Has("ui_mode") || form.Get("success_url") == "" {
			t.Errorf("%s: the hosted form changed: %v", name, form)
		}
		if intents := intentsOf(t, ctx, pool, start.CheckoutSess.ID); len(intents) != 1 || intents[0].HostedCheckoutURL == nil {
			t.Errorf("%s: want one hosted intent, got %+v", name, intents)
		}
	}

	on := buildEmbeddedServer(t, pool, stripe.baseURL(), true)
	assertHosted("old widget without capabilities", on, nil)
	assertHosted("unknown capability only", on, []string{"something_else"})

	off := buildEmbeddedServer(t, pool, stripe.baseURL(), false)
	assertHosted("STRIPE_ELEMENTS_ENABLED off", off, []string{"stripe_elements"})

	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = '{"checkout":{"stripe_ui":"hosted"}}'::jsonb WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("set stripe_ui: %v", err)
	}
	assertHosted("channel stripe_ui=hosted", on, []string{"stripe_elements"})
}

func TestEmbeddedCheckout_HostedFallbackExpiresFirstAndDoublePaymentPaysOnceAndAlerts(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "emc")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := startOne(t, srv, f, []string{"stripe_elements"})
	oldCS := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	createdBefore := len(stripe.sessions)

	code, out := postHostedFallback(t, srv, start.CheckoutToken)
	if code != http.StatusOK {
		t.Fatalf("/hosted = %d %v", code, out)
	}
	hostedURL, _ := out["redirect_url"].(string)
	if !strings.HasPrefix(hostedURL, "https://checkout.stripe.test/") {
		t.Fatalf("redirect_url = %q", hostedURL)
	}
	// Expired FIRST: the embedded session was already expired when the
	// hosted one was created.
	if e, _ := stripe.elementsSession(oldCS); e.status != "expired" {
		t.Fatalf("embedded session status = %q, want expired", e.status)
	}
	if len(stripe.sessions) != createdBefore+1 {
		t.Fatalf("stripe sessions created by /hosted = %d, want 1", len(stripe.sessions)-createdBefore)
	}
	if ex := stripe.expired(); len(ex) != 1 || ex[0] != oldCS {
		t.Fatalf("expired before the hosted page: %v, want only %s", ex, oldCS)
	}
	newForm := stripe.lastForm(t)
	if newForm.Has("ui_mode") || newForm.Get("success_url") == "" {
		t.Errorf("fallback page is not a hosted page: %v", newForm)
	}

	intents := intentsOf(t, ctx, pool, start.CheckoutSess.ID)
	if len(intents) != 2 {
		t.Fatalf("intents = %d, want 2", len(intents))
	}
	newPI, oldPI := intents[0], intents[1]
	if newPI.HostedCheckoutURL == nil || *newPI.HostedCheckoutURL != hostedURL {
		t.Fatalf("new intent = %+v", newPI)
	}
	if oldPI.State != "created" || oldPI.FailureCode == nil || *oldPI.FailureCode != "superseded" {
		t.Fatalf("old intent = state %q failure %v; want still created and superseded", oldPI.State, oldPI.FailureCode)
	}

	st := getEmbeddedStatus(t, srv, start.CheckoutToken)
	if st.Payment != nil || st.PaymentURL == nil || *st.PaymentURL != hostedURL {
		t.Fatalf("status after the switch = %+v", st)
	}
	// A repeated call answers the same page and creates nothing.
	if code, again := postHostedFallback(t, srv, start.CheckoutToken); code != http.StatusOK || again["redirect_url"] != hostedURL {
		t.Fatalf("repeat /hosted = %d %v", code, again)
	}
	if len(stripe.sessions) != createdBefore+1 {
		t.Fatal("a repeated /hosted created another session")
	}

	// The OLD (superseded) session is paid after the new one was created.
	stripe.markElements(oldCS, "complete", "paid")
	code, body := postSignedWebhook(t, srv, sessionCompletedEvent(oldCS, "pi_emc_old_"+uuid.NewString()[:8], "paid"), f.webhookSecret)
	if code != http.StatusOK || !strings.Contains(string(body), `"checkout_completed":true`) {
		t.Fatalf("old session paid webhook = %d %s", code, body)
	}
	ord, err := gen.New(pool).GetOrderByCheckoutSession(ctx, uuid.MustParse(start.CheckoutSess.ID))
	if err != nil || ord.Status != "paid" {
		t.Fatalf("order = %q (%v), want paid by the superseded session", ord.Status, err)
	}
	newCS := *newPI.ProviderPaymentID
	if ex := stripe.expired(); len(ex) != 2 || ex[1] != newCS {
		t.Errorf("the hosted page was not closed after the old session paid: expired %v", ex)
	}

	// Both got paid anyway: the second payment is parked, nothing ships twice.
	code, body = postSignedWebhook(t, srv, sessionCompletedEvent(newCS, "pi_emc_new_"+uuid.NewString()[:8], "paid"), f.webhookSecret)
	if code != http.StatusOK || strings.Contains(string(body), `"checkout_completed":true`) {
		t.Fatalf("second payment webhook = %d %s; it must not complete the checkout again", code, body)
	}
	second, err := gen.New(pool).GetPaymentIntentByID(ctx, newPI.ID)
	if err != nil || second.State != "manual_review" || second.FailureCode == nil || *second.FailureCode != "double_payment" {
		t.Fatalf("second payment intent = %+v (%v); want manual_review/double_payment", second, err)
	}
	if n := countIssueJobs(t, ctx, pool, start.CheckoutSess.ID); n != 1 {
		t.Errorf("issue_tickets jobs = %d, want exactly 1", n)
	}
	cs, _ := gen.New(pool).GetCheckoutSessionByID(ctx, uuid.MustParse(start.CheckoutSess.ID))
	if cs.PaymentIntentID == nil || *cs.PaymentIntentID != oldPI.ID.String() {
		t.Errorf("checkout paid by %v, want the first payment %s", cs.PaymentIntentID, oldPI.ID)
	}

	// The ops alert: the watchdog reports the parked double payment.
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ops_alerts WHERE fingerprint LIKE '%' || $1 || '%'`, newPI.ID.String())
	}()
	notifier := &embeddedRecordingNotifier{}
	if err := opswatchdog.NewHandler(opswatchdog.Options{Pool: pool, Notifier: notifier})(ctx, nil); err != nil {
		t.Fatalf("watchdog run: %v", err)
	}
	if !notifier.contains("double_payment") {
		t.Errorf("no ops alert names the double payment; sent: %v", notifier.all())
	}
}

func TestEmbeddedCheckout_HostedFallbackRefusedWhilePaidAndSecretGoneAfterHold(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "emd")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := startOne(t, srv, f, []string{"stripe_elements"})
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	created := len(stripe.sessions)

	stripe.markElements(cs, "complete", "paid")
	code, out := postHostedFallback(t, srv, start.CheckoutToken)
	if code != http.StatusConflict {
		t.Fatalf("/hosted on a paid session = %d %v, want 409", code, out)
	}
	if errObj, _ := out["error"].(map[string]any); errObj == nil || errObj["code"] != "checkout.payment_in_progress" {
		t.Fatalf("/hosted error = %v, want checkout.payment_in_progress", out)
	}
	if len(stripe.sessions) != created {
		t.Fatal("a hosted page was created although the embedded session is paid")
	}
	if intents := intentsOf(t, ctx, pool, start.CheckoutSess.ID); len(intents) != 1 || intents[0].FailureCode != nil {
		t.Fatalf("intents after the refusal = %+v", intents)
	}

	// While being paid (status open, the expiry refused), the same answer.
	stripe.markElements(cs, "open", "unpaid")
	if st := getEmbeddedStatus(t, srv, start.CheckoutToken); st.Payment == nil {
		t.Fatal("an open embedded session lost its payment block")
	}
	// The hold expires: no secret any more.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() - interval '1 minute'
		WHERE id = (SELECT reservation_id FROM checkout_sessions WHERE id = $1)`, uuid.MustParse(start.CheckoutSess.ID)); err != nil {
		t.Fatalf("expire hold: %v", err)
	}
	if st := getEmbeddedStatus(t, srv, start.CheckoutToken); st.Payment != nil {
		t.Fatal("the client secret was handed out after the hold expired")
	}
	if code, _ := postHostedFallback(t, srv, start.CheckoutToken); code != http.StatusConflict {
		t.Errorf("/hosted after the hold expired = %d, want 409", code)
	}
}

func TestEmbeddedCheckout_PaymentFailedByPIThenCompletedByCSPays(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "eme")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := startOne(t, srv, f, []string{"stripe_elements"})
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	piRef := "pi_eme_" + uuid.NewString()[:10]

	failed, _ := json.Marshal(map[string]any{
		"id": "evt_eme_" + uuid.NewString()[:8], "type": "payment_intent.payment_failed",
		"data": map[string]any{"object": map[string]any{
			"id": piRef, "object": "payment_intent", "status": "requires_payment_method",
			"amount": 2500, "currency": "eur",
			"last_payment_error": map[string]any{"code": "card_declined", "message": "Your card was declined."},
		}},
	})
	for _, path := range []string{"/v1/payment-intents/webhook", f.configWebhookPath()} {
		code, body := postSignedWebhookTo(t, srv, path, failed, f.webhookSecret)
		if code >= 500 || strings.Contains(string(body), `"processed":true`) {
			t.Fatalf("payment_failed on %s = %d %s; it must move nothing", path, code, body)
		}
	}
	if pi := intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0]; pi.State != "created" {
		t.Fatalf("intent state after a declined card = %q, want created (the session stays open)", pi.State)
	}
	if st := getEmbeddedStatus(t, srv, start.CheckoutToken); st.Status != "pending" || st.Payment == nil {
		t.Fatalf("status after the decline = %+v; the buyer must be able to try another card", st)
	}

	code, body := postSignedWebhook(t, srv, sessionCompletedEvent(cs, piRef, "paid"), f.webhookSecret)
	if code != http.StatusOK || !strings.Contains(string(body), `"checkout_completed":true`) {
		t.Fatalf("completed webhook = %d %s", code, body)
	}
	pi, err := gen.New(pool).GetPaymentIntentByProviderID(ctx, cs)
	if err != nil || pi.State != "succeeded" || pi.ProviderChargeRef == nil || *pi.ProviderChargeRef != piRef {
		t.Fatalf("intent = %+v (%v)", pi, err)
	}
}

// embeddedRecordingNotifier captures the watchdog's messages.
type embeddedRecordingNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (n *embeddedRecordingNotifier) Send(_ context.Context, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, text)
	return nil
}

func (n *embeddedRecordingNotifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.sent...)
}

func (n *embeddedRecordingNotifier) contains(s string) bool {
	for _, m := range n.all() {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// The checkout/start answer is kept for a repeated Idempotency-Key (П-19);
// the kept copy must not hold the client secret (spec 65 §3.7).
func TestEmbeddedCheckout_IdempotentReplayNeverStoresTheSecret(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "emf")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	key := uuid.NewString()
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE key = $1`, key)
	}()
	payload, _ := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(), "tier_id": f.tierID.String(), "qty": 1,
		"return_url":   hostedTicketsBaseURL + "/shows/embedded",
		"buyer":        map[string]any{"email": f.buyerMail, "name": "Embedded Buyer", "phone": f.buyerPhone},
		"capabilities": []string{"stripe_elements"},
	})
	send := func() embeddedStartResponse {
		req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("checkout/start = %d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Idempotency-Persisted") != "" {
			t.Error("the internal persisted marker leaked to the client")
		}
		var out embeddedStartResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	first := send()
	if first.Payment == nil || first.Payment.ClientSecret == "" {
		t.Fatalf("first answer = %+v", first.Payment)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT response_body::text FROM idempotency_keys WHERE key = $1`, key).Scan(&stored); err != nil {
		t.Fatalf("no idempotency row for the key: %v", err)
	}
	if strings.Contains(stored, first.Payment.ClientSecret) || strings.Contains(stored, "_secret_") {
		t.Fatalf("the stored idempotent answer holds the client secret: %s", stored)
	}
	replay := send()
	if replay.CheckoutToken != first.CheckoutToken || replay.Payment == nil || replay.Payment.ClientSecret != "" {
		t.Fatalf("replay = %+v / %+v; want the same order without the secret", replay.CheckoutToken, replay.Payment)
	}
	if st := getEmbeddedStatus(t, srv, first.CheckoutToken); st.Payment == nil || st.Payment.ClientSecret != first.Payment.ClientSecret {
		t.Fatal("the status does not hand the secret back after a replay")
	}
	if len(stripe.sessions) != 1 {
		t.Errorf("stripe sessions = %d, want 1 (the replay must not create another)", len(stripe.sessions))
	}
}

// The order status reads the client secret back from Stripe only when the
// request asks for it with ?include=payment; plain polling makes no provider
// call and carries no payment block.
func TestEmbeddedCheckout_StatusReadsPaymentOnlyWithInclude(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, pk := embeddedFixture(t, ctx, pool, "emi")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := startOne(t, srv, f, []string{"stripe_elements"})
	if start.Payment == nil {
		t.Fatal("embedded start answered no payment block")
	}
	reads := stripe.embeddedReads()

	for _, path := range []string{
		"/v1/public/checkout/" + start.CheckoutToken,
		"/v1/public/checkout/" + start.CheckoutToken + "?include=tickets",
	} {
		st, body := getEmbeddedStatusAt(t, srv, path)
		if st.Status != "pending" || st.Payment != nil || strings.Contains(body, `"payment":`) || strings.Contains(body, "_secret_") {
			t.Fatalf("%s: status = %+v body %s; want pending without a payment block", path, st, body)
		}
	}
	if got := stripe.embeddedReads(); got != reads {
		t.Fatalf("plain status polling read the session from Stripe %d times, want 0", got-reads)
	}

	st, _ := getEmbeddedStatusAt(t, srv, "/v1/public/checkout/"+start.CheckoutToken+"?include=tickets,payment")
	if st.Payment == nil || st.Payment.ClientSecret != start.Payment.ClientSecret || st.Payment.PublishableKey != pk {
		t.Fatalf("?include=payment status = %+v", st.Payment)
	}
	if got := stripe.embeddedReads(); got != reads+1 {
		t.Fatalf("?include=payment made %d Stripe reads, want 1", got-reads)
	}
}

// sessionExpiredEvent is Stripe's checkout.session.expired for an unpaid
// session.
func sessionExpiredEvent(sessionID string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"id":   "evt_emx_" + uuid.New().String()[:12],
		"type": "checkout.session.expired",
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "object": "checkout.session", "status": "expired",
			"payment_status": "unpaid", "ui_mode": "elements", "payment_intent": nil,
		}},
	})
	return raw
}

// /hosted after a partial failure: the embedded session was expired but the
// hosted page was not created (a Stripe outage). A retry must create the
// hosted page — both while the embedded row is still open and after the
// checkout.session.expired webhook failed it with session_expired — instead
// of answering 409 not_payable.
func TestEmbeddedCheckout_HostedFallbackRecoversAfterPartialFailure(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "emr")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	for _, viaWebhook := range []bool{false, true} {
		start := startOne(t, srv, f, []string{"stripe_elements"})
		embeddedCS := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID

		stripe.failNextHostedCreates(1)
		if code, out := postHostedFallback(t, srv, start.CheckoutToken); code != http.StatusServiceUnavailable {
			t.Fatalf("webhook=%v: /hosted during the outage = %d %v, want 503", viaWebhook, code, out)
		}
		if e, _ := stripe.elementsSession(embeddedCS); e.status != "expired" {
			t.Fatalf("webhook=%v: embedded session = %q, want expired before the failed create", viaWebhook, e.status)
		}
		if intents := intentsOf(t, ctx, pool, start.CheckoutSess.ID); len(intents) != 1 {
			t.Fatalf("webhook=%v: intents after the failed create = %d, want 1", viaWebhook, len(intents))
		}

		if viaWebhook {
			code, body := postSignedWebhook(t, srv, sessionExpiredEvent(embeddedCS), f.webhookSecret)
			if code != http.StatusOK {
				t.Fatalf("expired webhook = %d %s", code, body)
			}
			pi := intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0]
			if pi.State != "failed" || pi.FailureCode == nil || *pi.FailureCode != "session_expired" {
				t.Fatalf("embedded intent after the expired webhook = %q %v, want failed/session_expired", pi.State, pi.FailureCode)
			}
		}

		code, out := postHostedFallback(t, srv, start.CheckoutToken)
		if code != http.StatusOK {
			t.Fatalf("webhook=%v: retry /hosted = %d %v, want 200", viaWebhook, code, out)
		}
		hostedURL, _ := out["redirect_url"].(string)
		if !strings.HasPrefix(hostedURL, "https://checkout.stripe.test/") {
			t.Fatalf("webhook=%v: redirect_url = %q", viaWebhook, hostedURL)
		}
		intents := intentsOf(t, ctx, pool, start.CheckoutSess.ID)
		if len(intents) != 2 || intents[0].HostedCheckoutURL == nil || *intents[0].HostedCheckoutURL != hostedURL {
			t.Fatalf("webhook=%v: intents after the retry = %+v", viaWebhook, intents)
		}
		// The status now offers the hosted page.
		if st := getEmbeddedStatus(t, srv, start.CheckoutToken); st.Payment != nil || st.PaymentURL == nil || *st.PaymentURL != hostedURL {
			t.Fatalf("webhook=%v: status after the retry = %+v", viaWebhook, st)
		}
		// A third call answers the same page and creates nothing new.
		created := len(stripe.sessions)
		if code, again := postHostedFallback(t, srv, start.CheckoutToken); code != http.StatusOK || again["redirect_url"] != hostedURL {
			t.Fatalf("webhook=%v: repeat /hosted = %d %v", viaWebhook, code, again)
		}
		if len(stripe.sessions) != created {
			t.Fatalf("webhook=%v: a repeated /hosted created another session", viaWebhook)
		}
	}
}

// An embedded checkout.session.* event carries the session's client secret;
// the copy kept in payment_intent_events.event_payload must not.
func TestEmbeddedCheckout_WebhookPayloadIsStoredWithoutClientSecret(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "ems")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := startOne(t, srv, f, []string{"stripe_elements"})
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	secret := start.Payment.ClientSecret

	var ev map[string]any
	_ = json.Unmarshal(sessionCompletedEvent(cs, "pi_ems_"+uuid.NewString()[:8], "paid"), &ev)
	obj := ev["data"].(map[string]any)["object"].(map[string]any)
	obj["client_secret"] = secret
	obj["ui_mode"] = "elements"
	body, _ := json.Marshal(ev)
	stripe.markElements(cs, "complete", "paid")
	if code, resp := postSignedWebhook(t, srv, body, f.webhookSecret); code != http.StatusOK || !strings.Contains(string(resp), `"checkout_completed":true`) {
		t.Fatalf("completed webhook = %d %s", code, resp)
	}

	var stored string
	if err := pool.QueryRow(ctx, `SELECT event_payload::text FROM payment_intent_events
		WHERE provider_payment_id = $1 AND event_type = 'checkout.session.completed'`, cs).Scan(&stored); err != nil {
		t.Fatalf("no stored event: %v", err)
	}
	if strings.Contains(stored, secret) || strings.Contains(stored, "client_secret") {
		t.Fatalf("the stored webhook payload holds the client secret: %s", stored)
	}
	if !strings.Contains(stored, cs) || !strings.Contains(stored, "paid") {
		t.Fatalf("the stored payload lost the event itself: %s", stored)
	}
}

//go:build integration

// flitt_checkout_integration_test.go — live-DB, real-router coverage of the
// widget's Flitt-hosted payment flow (spec 08_architecture/29_*), only Flitt
// itself stubbed:
//
//	POST /v1/public/feeds/{feed_token}/checkout/start   (→ stub Flitt /checkout/url)
//	  → payment_intents row (provider 'flitt') keyed by OUR order_id
//	  → POST /v1/payment-intents/webhook/{config_id}    (Flitt callback, signed in
//	                                                     the BODY with the payment key)
//	  → GET  /v1/public/checkout/{checkout_token}       (→ "paid")
//
// What is pinned here and nowhere else: every currency Flitt may be asked for
// reaches it unchanged, a declined attempt does NOT close the order, a callback
// is only believed under the key and merchant id of the config in the URL, and
// the legacy un-suffixed route refuses a Flitt body outright.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestFlitt
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/flitt"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// stubFlittServer answers POST /checkout/url and /status/order_id and records
// what the order depended on.
type stubFlittServer struct {
	server *httptest.Server
	mu     sync.Mutex
	orders []map[string]any
	key    string
}

func newStubFlittServer(t *testing.T, paymentKey string) *stubFlittServer {
	t.Helper()
	s := &stubFlittServer{key: paymentKey}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			Request map[string]any `json:"request"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&env); err != nil {
			t.Errorf("stub flitt: body is not {request:{...}}: %v", err)
		}
		// A real Flitt refuses an order whose signature is wrong; so does the stub.
		if sig, _ := env.Request["signature"].(string); sig != flitt.Sign(s.key, env.Request) {
			_, _ = w.Write([]byte(`{"response":{"response_status":"failure","error_code":1014,"error_message":"Invalid signature"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkout/url"):
			s.mu.Lock()
			s.orders = append(s.orders, env.Request)
			s.mu.Unlock()
			oid, _ := env.Request["order_id"].(string)
			_, _ = fmt.Fprintf(w, `{"response":{"response_status":"success","checkout_url":"https://pay.flitt.test/merchants/x/index.html?token=%s","payment_id":"%d"}}`,
				oid, time.Now().UnixNano()%1_000_000_000)
		case strings.HasSuffix(r.URL.Path, "/status/order_id"):
			_, _ = w.Write([]byte(`{"response":{"response_status":"failure","error_code":1018,"error_message":"Order not found"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubFlittServer) baseURL() string { return s.server.URL }

func (s *stubFlittServer) last(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.orders) == 0 {
		t.Fatal("stub flitt received no checkout/url requests")
	}
	return s.orders[len(s.orders)-1]
}

// flittFixture is a hostedOrgFixture re-pointed at Flitt, with its own
// merchant id and payment key.
type flittFixture struct {
	*hostedOrgFixture
	merchantID string
	paymentKey string
}

func newFlittFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label, currency string) *flittFixture {
	t.Helper()
	f := &flittFixture{hostedOrgFixture: newHostedOrgFixture(t, ctx, pool, label)}
	suffix := f.orgID.String()[:8]
	f.merchantID = fmt.Sprintf("%d", 1_000_000+time.Now().UnixNano()%8_000_000)
	f.paymentKey = "flitt_key_" + label + "_" + suffix

	secrets, _ := json.Marshal(map[string]string{"merchant_id": f.merchantID, "payment_key": f.paymentKey})
	steps := []struct {
		sql  string
		args []any
	}{
		{`UPDATE sales_channels SET provider = 'flitt' WHERE id = $1`, []any{f.channelID}},
		{`UPDATE payment_provider_configs SET provider = 'flitt', secrets = $2::jsonb WHERE id = $1`, []any{f.configID, string(secrets)}},
		{`UPDATE sessions SET currency = $2 WHERE id = $1`, []any{f.sessionID, currency}},
		{`UPDATE ticket_tiers SET currency = $2 WHERE session_id = $1`, []any{f.sessionID, currency}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("flittFixture step %d: %v", i, err)
		}
	}
	return f
}

// callback builds a Flitt callback body for the order and signs it.
func (f *flittFixture) callback(t *testing.T, orderID, status string, amount int64, currency, key, merchantID string) []byte {
	t.Helper()
	m := map[string]any{
		"order_id":             orderID,
		"merchant_id":          json.Number(merchantID),
		"order_status":         status,
		"response_status":      "success",
		"amount":               fmt.Sprintf("%d", amount),
		"currency":             currency,
		"actual_amount":        fmt.Sprintf("%d", amount),
		"actual_currency":      currency,
		"payment_id":           json.Number("805243692"),
		"tran_type":            "purchase",
		"payment_system":       "card",
		"sender_email":         f.buyerMail,
		"reversal_amount":      "0",
		"response_code":        "",
		"response_description": "",
		"additional_info":      `{"capture_status":"captured"}`,
	}
	if status == "declined" {
		m["response_code"] = json.Number("1005")
		m["response_description"] = "Declined by bank"
	}
	m["signature"] = flitt.Sign(key, m)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func postFlittCallback(t *testing.T, srv *Server, path string, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestFlitt_StartAndCallbackPayTheOrder_EveryCurrency runs the whole purchase
// for the currencies the first customers sell in, proving the currency reaches
// Flitt unchanged and a signed approved callback pays the order.
func TestFlitt_StartAndCallbackPayTheOrder_EveryCurrency(t *testing.T) {
	pool := integrationPool(t)

	for _, currency := range []string{"EUR", "GEL", "USD"} {
		t.Run(currency, func(t *testing.T) {
			ctx := t.Context()
			f := newFlittFixture(t, ctx, pool, "fl"+strings.ToLower(currency), currency)
			defer f.cleanup()

			stub := newStubFlittServer(t, f.paymentKey)
			srv := buildHostedCheckoutServer(t, pool, "")
			srv.flittAPIBaseURL = stub.baseURL()
			srv.cfg.AppPublicURL = "https://api.arena-integration.test"
			q := gen.New(pool)

			// ── 1. checkout/start → a Flitt order ─────────────────────────────
			returnURL := hostedTicketsBaseURL + "/shows/summer"
			code, body := f.startCheckoutWithLocale(t, srv, returnURL, "cs")
			if code != http.StatusCreated {
				t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
			}
			var start hostedStartResponse
			if err := json.Unmarshal(body, &start); err != nil {
				t.Fatalf("decode: %v (%s)", err, body)
			}
			if !strings.HasPrefix(start.RedirectURL, "https://pay.flitt.test/") {
				t.Fatalf("redirect_url = %q; want the Flitt-hosted page", start.RedirectURL)
			}
			csID := uuid.MustParse(start.CheckoutSession.ID)
			cs, err := q.GetCheckoutSessionByID(ctx, csID)
			if err != nil || cs.Total == nil {
				t.Fatalf("checkout session: %v / %+v", err, cs)
			}

			// ── 2. What Flitt was asked for ───────────────────────────────────
			ord := stub.last(t)
			if got := ord["currency"]; got != currency {
				t.Errorf("flitt currency = %v; want %s passed through unchanged", got, currency)
			}
			if got := fmt.Sprint(ord["amount"]); got != fmt.Sprintf("%d", *cs.Total) {
				t.Errorf("flitt amount = %s; want the platform total %d", got, *cs.Total)
			}
			if got := ord["order_id"]; got != csID.String() {
				t.Errorf("flitt order_id = %v; want the checkout session id %s", got, csID)
			}
			if got := fmt.Sprint(ord["merchant_id"]); got != f.merchantID {
				t.Errorf("flitt merchant_id = %s; want %s", got, f.merchantID)
			}
			if got := ord["lang"]; got != "cs" {
				t.Errorf("flitt lang = %v; want cs", got)
			}
			wantCB := "https://api.arena-integration.test/v1/payment-intents/webhook/" + f.configID.String()
			if got := ord["server_callback_url"]; got != wantCB {
				t.Errorf("flitt server_callback_url = %v; want %s", got, wantCB)
			}
			// The buyer comes back THROUGH arena (so Flitt's POST/GET setting is
			// irrelevant), and arena then 303s to the buyer's own page.
			wantReturn := "https://api.arena-integration.test/v1/public/payment-return?" +
				url.Values{"r": {returnURL}, "checkout_token": {start.CheckoutToken}}.Encode()
			if got := ord["response_url"]; got != wantReturn {
				t.Errorf("flitt response_url = %v; want %s", got, wantReturn)
			}
			if got := ord["cancel_url"]; got != wantReturn {
				t.Errorf("flitt cancel_url = %v; want %s", got, wantReturn)
			}
			ru, _ := url.Parse(fmt.Sprint(ord["response_url"]))
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				req := httptest.NewRequest(method, ru.RequestURI(), strings.NewReader("order_status=approved"))
				if method == http.MethodPost {
					// What Flitt's browser redirect really sends.
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				rec := httptest.NewRecorder()
				srv.router.ServeHTTP(rec, req)
				if want := returnURL + "?checkout_token=" + start.CheckoutToken; rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
					t.Errorf("%s response_url = %d Location %q; want 303 %q", method, rec.Code, rec.Header().Get("Location"), want)
				}
			}
			// The Flitt order must die before arena releases the seats.
			lifetime, err := ord["lifetime"].(json.Number).Int64()
			if err != nil || lifetime < 1800 || lifetime > 1900 {
				t.Errorf("flitt lifetime = %v; want ≈ the widget payment window (1860s)", ord["lifetime"])
			}

			// ── 3. The intent is keyed by OUR order id and recorded as flitt ──
			pi, err := q.GetPaymentIntentByProviderID(ctx, csID.String())
			if err != nil {
				t.Fatalf("payment intent by order id: %v", err)
			}
			if pi.Provider != "flitt" || pi.State != "created" || pi.Amount != *cs.Total || !strings.EqualFold(pi.Currency, currency) {
				t.Fatalf("payment intent = %+v; want provider flitt, state created, %d %s", pi, *cs.Total, currency)
			}

			path := f.configWebhookPath()

			// ── 4. A DECLINED attempt must not close the order ────────────────
			code, body = postFlittCallback(t, srv, path, f.callback(t, csID.String(), "declined", *cs.Total, currency, f.paymentKey, f.merchantID))
			if code != http.StatusOK {
				t.Fatalf("declined callback = %d; body: %s", code, body)
			}
			if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
				t.Fatalf("a declined attempt moved the intent to %q; the buyer's retry on the same page would then be ignored", after.State)
			}

			// ── 5. Bad signature, wrong merchant, wrong key ───────────────────
			bad := f.callback(t, csID.String(), "approved", *cs.Total, currency, "not-the-key", f.merchantID)
			if code, _ = postFlittCallback(t, srv, path, bad); code != http.StatusUnauthorized {
				t.Errorf("callback signed with the wrong key = %d; want 401", code)
			}
			wrongMerchant := f.callback(t, csID.String(), "approved", *cs.Total, currency, f.paymentKey, "424242")
			if code, _ = postFlittCallback(t, srv, path, wrongMerchant); code != http.StatusUnauthorized {
				t.Errorf("callback for another merchant = %d; want 401", code)
			}
			if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
				t.Fatalf("a rejected callback moved the intent to %q", after.State)
			}

			// ── 6. The legacy un-suffixed route must refuse a Flitt body ──────
			good := f.callback(t, csID.String(), "approved", *cs.Total, currency, f.paymentKey, f.merchantID)
			if code, _ = postFlittCallback(t, srv, "/v1/payment-intents/webhook", good); code == http.StatusOK {
				t.Errorf("legacy webhook route accepted a Flitt callback; nothing there can vouch for it")
			}
			if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
				t.Fatalf("the legacy route moved the intent to %q", after.State)
			}

			// ── 7. The signed approved callback pays the order ────────────────
			code, body = postFlittCallback(t, srv, path, good)
			if code != http.StatusOK {
				t.Fatalf("approved callback = %d; body: %s", code, body)
			}
			var resp map[string]any
			_ = json.Unmarshal(body, &resp)
			if processed, _ := resp["processed"].(bool); !processed {
				t.Fatalf("approved callback processed = %v; body: %s", resp["processed"], body)
			}
			piAfter, _ := q.GetPaymentIntentByProviderID(ctx, csID.String())
			if piAfter.State != "succeeded" {
				t.Errorf("intent state = %q; want succeeded", piAfter.State)
			}
			if piAfter.ProviderChargeRef == nil || *piAfter.ProviderChargeRef != "805243692" {
				t.Errorf("provider_charge_ref = %v; want Flitt payment_id 805243692", piAfter.ProviderChargeRef)
			}
			if csAfter, _ := q.GetCheckoutSessionByID(ctx, csID); csAfter.State != "completed" {
				t.Errorf("checkout state = %q; want completed", csAfter.State)
			}
			if order, _ := q.GetOrderByCheckoutSession(ctx, csID); order.Status != "paid" {
				t.Errorf("order status = %q; want paid", order.Status)
			}
			if st := f.getStatus(t, srv, start.CheckoutToken); st.Status != "paid" {
				t.Errorf("public status = %q; want paid", st.Status)
			}

			// ── 8. Flitt retries callbacks: a replay is a harmless no-op ──────
			code, body = postFlittCallback(t, srv, path, good)
			if code != http.StatusOK {
				t.Errorf("replayed callback = %d; want 200 so Flitt stops retrying; body: %s", code, body)
			}
			var issueJobs int
			_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs
			   WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`,
				csID.String()).Scan(&issueJobs)
			if issueJobs != 1 {
				t.Errorf("checkout.issue_tickets jobs = %d after a replay; want exactly 1", issueJobs)
			}
		})
	}
}

// TestFlitt_TwoOrganizationsAreIsolated: two organizers on the same integration,
// each with their own merchant id and payment key. Org B signing a perfectly
// valid callback with ITS key for org A's order must not move it, and a
// callback for a payment arena never created is acknowledged, not 404'd.
func TestFlitt_TwoOrganizationsAreIsolated(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	a := newFlittFixture(t, ctx, pool, "fa", "EUR")
	defer a.cleanup()
	b := newFlittFixture(t, ctx, pool, "fb", "GEL")
	defer b.cleanup()

	stubA := newStubFlittServer(t, a.paymentKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.flittAPIBaseURL = stubA.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"
	q := gen.New(pool)

	code, body := a.startCheckout(t, srv, hostedTicketsBaseURL+"/a")
	if code != http.StatusCreated {
		t.Fatalf("org A checkout/start = %d; body: %s", code, body)
	}
	var start hostedStartResponse
	_ = json.Unmarshal(body, &start)
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, _ := q.GetCheckoutSessionByID(ctx, csID)

	// Org B's own, valid, signed callback — addressed to ORG A's order.
	forged := b.callback(t, csID.String(), "approved", *cs.Total, "EUR", b.paymentKey, b.merchantID)
	code, body = postFlittCallback(t, srv, b.configWebhookPath(), forged)
	if code != http.StatusOK {
		t.Fatalf("org B callback for org A's order = %d; want the quiet 200 'not ours'; body: %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if processed, _ := resp["processed"].(bool); processed {
		t.Fatalf("org B moved org A's payment: %s", body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
		t.Fatalf("org A's intent is %q after org B's callback; want created", after.State)
	}

	// Org A's key under org B's config id: signature fails.
	underA := a.callback(t, csID.String(), "approved", *cs.Total, "EUR", a.paymentKey, a.merchantID)
	if code, _ = postFlittCallback(t, srv, b.configWebhookPath(), underA); code != http.StatusUnauthorized {
		t.Errorf("org A's callback delivered to org B's config = %d; want 401", code)
	}

	// A verified callback for an order arena never created is acknowledged.
	foreign := a.callback(t, uuid.NewString(), "approved", 1000, "EUR", a.paymentKey, a.merchantID)
	code, body = postFlittCallback(t, srv, a.configWebhookPath(), foreign)
	if code != http.StatusOK {
		t.Errorf("foreign order callback = %d; want 200 so Flitt does not retry for days; body: %s", code, body)
	}

	// Org A's own callback still pays it.
	own := a.callback(t, csID.String(), "approved", *cs.Total, "EUR", a.paymentKey, a.merchantID)
	if code, body = postFlittCallback(t, srv, a.configWebhookPath(), own); code != http.StatusOK {
		t.Fatalf("org A's own callback = %d; body: %s", code, body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "succeeded" {
		t.Fatalf("org A's intent = %q; want succeeded", after.State)
	}
}

// TestFlitt_ExpiredOrderFailsTheIntent: Flitt reports an unpaid order dead.
func TestFlitt_ExpiredOrderFailsTheIntent(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newFlittFixture(t, ctx, pool, "fx", "EUR")
	defer f.cleanup()

	stub := newStubFlittServer(t, f.paymentKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.flittAPIBaseURL = stub.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"
	q := gen.New(pool)

	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/x")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d; body: %s", code, body)
	}
	var start hostedStartResponse
	_ = json.Unmarshal(body, &start)
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, _ := q.GetCheckoutSessionByID(ctx, csID)

	exp := f.callback(t, csID.String(), "expired", *cs.Total, "EUR", f.paymentKey, f.merchantID)
	if code, body = postFlittCallback(t, srv, f.configWebhookPath(), exp); code != http.StatusOK {
		t.Fatalf("expired callback = %d; body: %s", code, body)
	}
	after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String())
	if after.State != "failed" {
		t.Fatalf("intent after expiry = %q; want failed", after.State)
	}
}

// TestFlitt_MissingCredentialTakesNoInventory: a Flitt config without a
// payment key is refused BEFORE the hold is taken (the pre-flight), like Stripe.
func TestFlitt_MissingCredentialTakesNoInventory(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newFlittFixture(t, ctx, pool, "fm", "EUR")
	defer f.cleanup()

	if _, err := pool.Exec(ctx,
		`UPDATE payment_provider_configs SET secrets = '{"merchant_id":"1"}'::jsonb WHERE id = $1`, f.configID); err != nil {
		t.Fatal(err)
	}
	srv := buildHostedCheckoutServer(t, pool, "")
	before := f.footprint(t, ctx)
	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/m")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("checkout/start without a payment key = %d, want 422; body: %s", code, body)
	}
	if !strings.Contains(string(body), "checkout.payment_not_configured") {
		t.Errorf("body = %s; want checkout.payment_not_configured", body)
	}
	f.assertNoInventoryTaken(t, ctx, before)
}

// TestFlitt_FrenchBuyerGetsFrenchPageAndFrenchEmailLocale: a buyer from France
// (browser tag fr-FR) must reach Flitt as lang=fr AND be stored as buyer_locale
// fr, which is what picks the French e-mail template (templates.SupportedLocales).
func TestFlitt_FrenchBuyerGetsFrenchPageAndFrenchEmailLocale(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newFlittFixture(t, ctx, pool, "ffr", "EUR")
	defer f.cleanup()

	stub := newStubFlittServer(t, f.paymentKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.flittAPIBaseURL = stub.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"

	code, body := f.startCheckoutWithLocale(t, srv, hostedTicketsBaseURL+"/fr", "fr-FR")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d; body: %s", code, body)
	}
	var start hostedStartResponse
	_ = json.Unmarshal(body, &start)
	if got := stub.last(t)["lang"]; got != "fr" {
		t.Errorf("flitt lang = %v; want fr", got)
	}
	var stored *string
	if err := pool.QueryRow(ctx, `SELECT buyer_locale FROM checkout_sessions WHERE id = $1`,
		uuid.MustParse(start.CheckoutSession.ID)).Scan(&stored); err != nil {
		t.Fatalf("read buyer_locale: %v", err)
	}
	if stored == nil || *stored != "fr" {
		t.Errorf("buyer_locale = %v; want fr so the ticket e-mail renders in French", stored)
	}
}

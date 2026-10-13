//go:build integration

// allpay_checkout_integration_test.go — live-DB, real-router coverage of the
// widget's AllPay-hosted payment flow (spec 08_architecture/37_*), only AllPay
// itself stubbed:
//
//	POST /v1/public/feeds/{feed_token}/checkout/start   (→ stub AllPay ?show=getpayment)
//	  → payment_intents row (provider 'allpay') keyed by OUR order_id
//	  → POST /v1/payment-intents/webhook/{config_id}    (AllPay webhook, signed in
//	                                                     the BODY with the API key)
//	  → GET  /v1/public/checkout/{checkout_token}       (→ "paid")
//
// What is pinned here and nowhere else: the price reaches AllPay in MAJOR
// units, a test config forces test_mode, a failed attempt (status 0) does NOT
// close the order, a webhook is only believed under the key of the config in
// the URL, another organization's valid webhook cannot move our payment, and
// the legacy un-suffixed route refuses an AllPay body outright.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestAllPay
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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/allpay"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// stubAllPayServer answers ?show=getpayment / checkkeys like allpay.to and
// records the orders it was asked to create. A wrong sign is error 3, as on
// the real service.
type stubAllPayServer struct {
	server *httptest.Server
	key    string
	mu     sync.Mutex
	orders []map[string]any
}

func newStubAllPayServer(t *testing.T, apiKey string) *stubAllPayServer {
	t.Helper()
	s := &stubAllPayServer{key: apiKey}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var body map[string]any
		if err := dec.Decode(&body); err != nil {
			t.Errorf("stub allpay: body is not a JSON object: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if sign, _ := body["sign"].(string); sign != allpay.Sign(s.key, body) {
			_, _ = w.Write([]byte(`{"error_code":3,"error_msg":"Signature is incorrect"}`))
			return
		}
		switch r.URL.Query().Get("show") {
		case "getpayment":
			s.mu.Lock()
			s.orders = append(s.orders, body)
			s.mu.Unlock()
			oid, _ := body["order_id"].(string)
			_, _ = fmt.Fprintf(w, `{"payment_url":"https://allpay.test/~login/pay/?payment_id=%s&code=xyz"}`, oid)
		case "checkkeys":
			_, _ = w.Write([]byte(`{"last_paid_order_id":"-1","last_paid_order_date":"-1"}`))
		default:
			_, _ = w.Write([]byte(`{"error_code":20,"error_msg":"Incorrect method"}`))
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubAllPayServer) baseURL() string { return s.server.URL + "/app" }

func (s *stubAllPayServer) last(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.orders) == 0 {
		t.Fatal("stub allpay received no getpayment requests")
	}
	return s.orders[len(s.orders)-1]
}

// allpayFixture is a hostedOrgFixture re-pointed at AllPay in ILS, with its
// own API login and key.
type allpayFixture struct {
	*hostedOrgFixture
	login  string
	apiKey string
}

func newAllPayFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) *allpayFixture {
	t.Helper()
	f := &allpayFixture{hostedOrgFixture: newHostedOrgFixture(t, ctx, pool, label)}
	suffix := f.orgID.String()[:8]
	f.login = "pp" + suffix
	f.apiKey = "allpay_key_" + label + "_" + suffix

	secrets, _ := json.Marshal(map[string]string{"merchant_id": f.login, "secret_key": f.apiKey})
	steps := []struct {
		sql  string
		args []any
	}{
		{`UPDATE sales_channels SET provider = 'allpay' WHERE id = $1`, []any{f.channelID}},
		{`UPDATE payment_provider_configs SET provider = 'allpay', secrets = $2::jsonb WHERE id = $1`, []any{f.configID, string(secrets)}},
		{`UPDATE sessions SET currency = 'ILS' WHERE id = $1`, []any{f.sessionID}},
		{`UPDATE ticket_tiers SET currency = 'ILS' WHERE session_id = $1`, []any{f.sessionID}},
		{`UPDATE venues SET timezone = 'Asia/Jerusalem' WHERE id = $1`, []any{f.venueID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("allpayFixture step %d: %v", i, err)
		}
	}
	return f
}

// webhook builds an AllPay payment webhook for the order and signs it.
func (f *allpayFixture) webhook(t *testing.T, orderID string, status int, totalMinor int64, key string) []byte {
	t.Helper()
	price := json.Number(allpay.MinorToMajor(totalMinor))
	m := map[string]any{
		"order_id":         orderID,
		"status":           json.Number(fmt.Sprint(status)),
		"amount":           price,
		"currency":         "ILS",
		"currency_display": "ILS",
		"currency_payment": "ILS",
		"currency_course":  json.Number("1"),
		"name":             "HOCO Event",
		"items": []any{map[string]any{
			"name": "HOCO Event", "qty": json.Number("1"), "price": price, "vat": json.Number("0"),
		}},
		"inst":         json.Number("1"),
		"card_mask":    "455743******3431",
		"card_brand":   "Visa",
		"foreign_card": json.Number("0"),
		"client_email": f.buyerMail,
	}
	if status == 0 {
		delete(m, "items")
		m["error"] = "Declined by the card issuer"
		m["decline_code"] = json.Number("20004")
		m["decline_reason"] = "Declined by the card issuer"
	}
	m["sign"] = allpay.Sign(key, m)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func postAllPayWebhook(t *testing.T, srv *Server, path string, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func allpayServer(t *testing.T, pool *pgxpool.Pool, stub *stubAllPayServer) *Server {
	t.Helper()
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.allpayAPIBaseURL = stub.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"
	return srv
}

// TestAllPay_StartAndWebhookPayTheOrder runs the whole purchase: the page is
// created with the platform total in shekels, a failed attempt leaves the
// order open, forged and misaddressed webhooks move nothing, the signed paid
// webhook pays the order and a replay is a no-op.
func TestAllPay_StartAndWebhookPayTheOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newAllPayFixture(t, ctx, pool, "ap")
	defer f.cleanup()

	stub := newStubAllPayServer(t, f.apiKey)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	// ── 1. checkout/start → an AllPay page ────────────────────────────────
	returnURL := hostedTicketsBaseURL + "/shows/haifa"
	code, body := f.startCheckoutWithLocale(t, srv, returnURL, "he-IL")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !strings.HasPrefix(start.RedirectURL, "https://allpay.test/") {
		t.Fatalf("redirect_url = %q; want the AllPay page", start.RedirectURL)
	}
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Total == nil {
		t.Fatalf("checkout session: %v / %+v", err, cs)
	}

	// ── 2. What AllPay was asked for ──────────────────────────────────────
	ord := stub.last(t)
	str := func(v any) string { return fmt.Sprint(v) }
	if got := str(ord["login"]); got != f.login {
		t.Errorf("login = %s; want %s", got, f.login)
	}
	if got := ord["order_id"]; got != csID.String() {
		t.Errorf("order_id = %v; want the checkout session id %s", got, csID)
	}
	if got := ord["currency"]; got != "ILS" {
		t.Errorf("currency = %v; want ILS", got)
	}
	items, _ := ord["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v; want one line for the whole cart", ord["items"])
	}
	item, _ := items[0].(map[string]any)
	if got, want := str(item["price"]), allpay.MinorToMajor(*cs.Total); got != want {
		t.Errorf("price = %s; want the platform total %d in shekels (%s)", got, *cs.Total, want)
	}
	if got := ord["lang"]; got != "HE" {
		t.Errorf("lang = %v; want HE", got)
	}
	if got := str(ord["test_mode"]); got != "1" {
		t.Errorf("test_mode = %s; a config filed as test must never charge a real card", got)
	}
	wantCB := "https://api.arena-integration.test/v1/payment-intents/webhook/" + f.configID.String()
	if got := ord["webhook_url"]; got != wantCB {
		t.Errorf("webhook_url = %v; want %s", got, wantCB)
	}
	wantReturn := "https://api.arena-integration.test/v1/public/payment-return?" +
		url.Values{"r": {returnURL}, "checkout_token": {start.CheckoutToken}}.Encode()
	if got := ord["success_url"]; got != wantReturn {
		t.Errorf("success_url = %v; want %s", got, wantReturn)
	}
	if got := ord["backlink_url"]; got != wantReturn {
		t.Errorf("backlink_url = %v; want %s", got, wantReturn)
	}
	// The AllPay page must die before arena releases the places: its expire
	// is the widget payment window (1860 s), the hold lives the grace longer.
	exp, err := ord["expire"].(json.Number).Int64()
	if left := exp - time.Now().Unix(); err != nil || left < 1800 || left > 1900 {
		t.Errorf("expire = %v; want ≈ now + the widget payment window (1860 s)", ord["expire"])
	}

	// ── 3. The intent is keyed by OUR order id and recorded as allpay ─────
	pi, err := q.GetPaymentIntentByProviderID(ctx, csID.String())
	if err != nil {
		t.Fatalf("payment intent by order id: %v", err)
	}
	if pi.Provider != "allpay" || pi.State != "created" || pi.Amount != *cs.Total || !strings.EqualFold(pi.Currency, "ILS") {
		t.Fatalf("payment intent = %+v; want provider allpay, state created, %d ILS", pi, *cs.Total)
	}

	path := f.configWebhookPath()

	// ── 4. A FAILED attempt must not close the order ──────────────────────
	code, body = postAllPayWebhook(t, srv, path, f.webhook(t, csID.String(), 0, *cs.Total, f.apiKey))
	if code != http.StatusOK {
		t.Fatalf("failed-attempt webhook = %d; body: %s", code, body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
		t.Fatalf("a failed attempt moved the intent to %q; the buyer's next card would then be ignored", after.State)
	}

	// ── 5. Wrong key ──────────────────────────────────────────────────────
	if code, _ = postAllPayWebhook(t, srv, path, f.webhook(t, csID.String(), 1, *cs.Total, "not-the-key")); code != http.StatusUnauthorized {
		t.Errorf("webhook signed with the wrong key = %d; want 401", code)
	}

	// ── 6. The legacy un-suffixed route must refuse an AllPay body ────────
	good := f.webhook(t, csID.String(), 1, *cs.Total, f.apiKey)
	if code, body = postAllPayWebhook(t, srv, "/v1/payment-intents/webhook", good); code == http.StatusOK {
		t.Errorf("legacy webhook route accepted an AllPay webhook; nothing there can vouch for it")
	} else if !strings.Contains(string(body), "webhook.allpay_requires_config_route") {
		t.Errorf("legacy route body = %s; want webhook.allpay_requires_config_route", body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
		t.Fatalf("a refused webhook moved the intent to %q", after.State)
	}

	// ── 7. The signed paid webhook pays the order ─────────────────────────
	code, body = postAllPayWebhook(t, srv, path, good)
	if code != http.StatusOK {
		t.Fatalf("paid webhook = %d; body: %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if processed, _ := resp["processed"].(bool); !processed {
		t.Fatalf("paid webhook processed = %v; body: %s", resp["processed"], body)
	}
	piAfter, _ := q.GetPaymentIntentByProviderID(ctx, csID.String())
	if piAfter.State != "succeeded" {
		t.Errorf("intent state = %q; want succeeded", piAfter.State)
	}
	if piAfter.ProviderChargeRef == nil || *piAfter.ProviderChargeRef != csID.String() {
		t.Errorf("provider_charge_ref = %v; want the order id (AllPay refunds by it)", piAfter.ProviderChargeRef)
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

	// ── 8. AllPay retries webhooks: a replay is a harmless no-op ──────────
	if code, body = postAllPayWebhook(t, srv, path, good); code != http.StatusOK {
		t.Errorf("replayed webhook = %d; want 200 so AllPay stops retrying; body: %s", code, body)
	}
	var issueJobs int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs
	   WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`,
		csID.String()).Scan(&issueJobs)
	if issueJobs != 1 {
		t.Errorf("checkout.issue_tickets jobs = %d after a replay; want exactly 1", issueJobs)
	}
}

// TestAllPay_LiveConfigLeavesTestModeToTheIntegration: a config filed as live
// sends no test_mode, so the AllPay integration's own setting decides.
func TestAllPay_LiveConfigLeavesTestModeToTheIntegration(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newAllPayFixture(t, ctx, pool, "apl")
	defer f.cleanup()
	if _, err := pool.Exec(ctx, `UPDATE payment_provider_configs SET mode = 'live' WHERE id = $1`, f.configID); err != nil {
		t.Fatal(err)
	}
	stub := newStubAllPayServer(t, f.apiKey)
	srv := allpayServer(t, pool, stub)

	if code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/live"); code != http.StatusCreated {
		t.Fatalf("checkout/start = %d; body: %s", code, body)
	}
	if v, has := stub.last(t)["test_mode"]; has {
		t.Errorf("test_mode = %v on a live config; want it left out", v)
	}
}

// TestAllPay_TwoOrganizationsAreIsolated: org B signing a perfectly valid
// webhook with ITS key for org A's order must not move it, org A's webhook
// under org B's config id fails the signature, and a webhook for an order
// arena never created is acknowledged, not 404'd.
func TestAllPay_TwoOrganizationsAreIsolated(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	a := newAllPayFixture(t, ctx, pool, "apa")
	defer a.cleanup()
	b := newAllPayFixture(t, ctx, pool, "apb")
	defer b.cleanup()

	stubA := newStubAllPayServer(t, a.apiKey)
	srv := allpayServer(t, pool, stubA)
	q := gen.New(pool)

	code, body := a.startCheckout(t, srv, hostedTicketsBaseURL+"/a")
	if code != http.StatusCreated {
		t.Fatalf("org A checkout/start = %d; body: %s", code, body)
	}
	var start hostedStartResponse
	_ = json.Unmarshal(body, &start)
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, _ := q.GetCheckoutSessionByID(ctx, csID)

	forged := b.webhook(t, csID.String(), 1, *cs.Total, b.apiKey)
	code, body = postAllPayWebhook(t, srv, b.configWebhookPath(), forged)
	if code != http.StatusOK {
		t.Fatalf("org B webhook for org A's order = %d; want the quiet 200 'not ours'; body: %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if processed, _ := resp["processed"].(bool); processed {
		t.Fatalf("org B moved org A's payment: %s", body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "created" {
		t.Fatalf("org A's intent is %q after org B's webhook; want created", after.State)
	}

	underA := a.webhook(t, csID.String(), 1, *cs.Total, a.apiKey)
	if code, _ = postAllPayWebhook(t, srv, b.configWebhookPath(), underA); code != http.StatusUnauthorized {
		t.Errorf("org A's webhook delivered to org B's config = %d; want 401", code)
	}

	foreign := a.webhook(t, uuid.NewString(), 1, 5000, a.apiKey)
	if code, body = postAllPayWebhook(t, srv, a.configWebhookPath(), foreign); code != http.StatusOK {
		t.Errorf("foreign order webhook = %d; want 200 so AllPay does not retry; body: %s", code, body)
	}

	if code, body = postAllPayWebhook(t, srv, a.configWebhookPath(), underA); code != http.StatusOK {
		t.Fatalf("org A's own webhook = %d; body: %s", code, body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, csID.String()); after.State != "succeeded" {
		t.Fatalf("org A's intent = %q; want succeeded", after.State)
	}
}

// TestAllPay_UnsupportedCurrencyTakesNoPage: AllPay charges only in ILS, USD
// or EUR; anything else would be converted to shekels behind arena's back, so
// no page is created.
func TestAllPay_UnsupportedCurrencyTakesNoPage(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newAllPayFixture(t, ctx, pool, "apc")
	defer f.cleanup()
	for _, sql := range []string{
		`UPDATE sessions SET currency = 'CZK' WHERE id = $1`,
		`UPDATE ticket_tiers SET currency = 'CZK' WHERE session_id = $1`,
	} {
		if _, err := pool.Exec(ctx, sql, f.sessionID); err != nil {
			t.Fatal(err)
		}
	}
	stub := newStubAllPayServer(t, f.apiKey)
	srv := allpayServer(t, pool, stub)

	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/czk")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "checkout.payment_start_failed") {
		t.Fatalf("checkout/start in CZK = %d %s; want 503 checkout.payment_start_failed", code, body)
	}
	stub.mu.Lock()
	n := len(stub.orders)
	stub.mu.Unlock()
	if n != 0 {
		t.Fatalf("AllPay was asked for %d pages in CZK", n)
	}
}

// TestAllPay_MissingKeyTakesNoInventory: an AllPay config without the API key
// is refused BEFORE the hold is taken (the pre-flight), like Stripe and Flitt.
func TestAllPay_MissingKeyTakesNoInventory(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newAllPayFixture(t, ctx, pool, "apm")
	defer f.cleanup()

	if _, err := pool.Exec(ctx,
		`UPDATE payment_provider_configs SET secrets = '{"merchant_id":"pp1"}'::jsonb WHERE id = $1`, f.configID); err != nil {
		t.Fatal(err)
	}
	srv := buildHostedCheckoutServer(t, pool, "")
	before := f.footprint(t, ctx)
	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/m")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("checkout/start without an API key = %d, want 422; body: %s", code, body)
	}
	if !strings.Contains(string(body), "checkout.payment_not_configured") {
		t.Errorf("body = %s; want checkout.payment_not_configured", body)
	}
	f.assertNoInventoryTaken(t, ctx, before)
}

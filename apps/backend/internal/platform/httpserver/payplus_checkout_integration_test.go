//go:build integration

// payplus_checkout_integration_test.go — live-DB, real-router coverage of a
// TICKET purchase and refund through PayPlus (spec 47 §3), only PayPlus
// itself stubbed (stubPayPlusServer below; apps/widget/scripts/payplus-stub.cjs
// is the same contract for a running stand):
//
//	POST /v1/public/feeds/{feed_token}/checkout/start   (→ stub PaymentPages/generateLink)
//	  → payment_intents row (provider 'payplus') keyed by the page_request_uid
//	  → POST /v1/payment-intents/webhook/{config_id}    (callback, `hash` header =
//	                                                     base64 HMAC-SHA256 under the secret key)
//	  → GET  /v1/public/checkout/{checkout_token}       (→ "paid")
//	POST /v1/organizations/{org}/orders/{order}/refunds (→ stub RefundByTransactionUID)
//
// What is pinned: the amount reaches PayPlus in MAJOR units and the callback's
// major amount is converted back once; a declined attempt does not close the
// payment; a wrong hash is 401; the legacy un-suffixed route refuses a PayPlus
// body; a replay is a no-op; a partial refund goes back through the charge's
// transaction uid, read back from the link when the callback never stored it.
//
// Run on a scratch database (AGENTS.md "Reproduce the CI Integration job on a
// FRESH database"):
//
//	DATABASE_URL=... go test -tags integration -run TestPayPlus ./apps/backend/internal/platform/httpserver/
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/payplus"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// stubPayPlusServer answers the PayPlus REST calls the adapter makes and keeps
// a ledger of charges and refunds. Amounts are stored in arena minor units.
type stubPayPlusServer struct {
	server *httptest.Server
	apiKey string
	secret string

	mu      sync.Mutex
	links   []map[string]any
	charges map[string]stubPPCharge // transaction uid → charge
	byPage  map[string]string       // page_request_uid → charge uid
	refunds []stubPPRefund
	seq     int
}

type stubPPCharge struct {
	uid, page, currency string
	amount              int64
}

type stubPPRefund struct {
	uid, charge, moreInfo string
	amount                int64
}

func newStubPayPlusServer(t *testing.T, apiKey, secret string) *stubPayPlusServer {
	t.Helper()
	s := &stubPayPlusServer{apiKey: apiKey, secret: secret, charges: map[string]stubPPCharge{}, byPage: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubPayPlusServer) baseURL() string { return s.server.URL + "/api/v1.0" }

func (s *stubPayPlusServer) charge(uid, page string, amount int64, currency string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.charges[uid] = stubPPCharge{uid: uid, page: page, amount: amount, currency: currency}
	s.byPage[page] = uid
}

func (s *stubPayPlusServer) lastLink(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.links) == 0 {
		t.Fatal("stub payplus received no generateLink requests")
	}
	return s.links[len(s.links)-1]
}

func (s *stubPayPlusServer) refundsOf(charge string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, r := range s.refunds {
		if r.charge == charge {
			out = append(out, r.amount)
		}
	}
	return out
}

func stubPPJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func stubPPOK(data any) map[string]any {
	return map[string]any{"results": map[string]any{"status": "success", "code": 0, "description": "operation has been success"}, "data": data}
}

func stubPPFail(desc string) map[string]any {
	return map[string]any{"results": map[string]any{"status": "error", "code": 1, "description": desc}}
}

func (s *stubPayPlusServer) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&body)
	if r.Header.Get("api-key") != s.apiKey || r.Header.Get("secret-key") != s.secret {
		stubPPJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "MISSING API_KEY"})
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/api/v1.0/") {
	case "PaymentPages/generateLink":
		s.mu.Lock()
		s.links = append(s.links, body)
		s.seq++
		page := fmt.Sprintf("%s-%04d", uuid.NewString()[:23], s.seq)
		s.mu.Unlock()
		stubPPJSON(w, http.StatusOK, stubPPOK(map[string]any{
			"page_request_uid": page, "payment_page_link": "https://paymentsdev.payplus.test/" + page,
		}))
	case "PaymentPages/ipn":
		if txUID, _ := body["transaction_uid"].(string); txUID != "" {
			// The charge with its related refunds: what a repeated refund
			// call reads before it may send again.
			s.mu.Lock()
			c, ok := s.charges[txUID]
			rel := []any{}
			for _, rf := range s.refunds {
				if rf.charge == txUID {
					rel = append(rel, map[string]any{"transaction_type": "Refund", "transaction": map[string]any{
						"uid": rf.uid, "status_code": "000", "amount": json.Number(payplus.MinorToMajor(rf.amount)), "more_info": rf.moreInfo,
					}})
				}
			}
			s.mu.Unlock()
			if !ok {
				stubPPJSON(w, http.StatusOK, stubPPFail("transaction not found"))
				return
			}
			stubPPJSON(w, http.StatusOK, stubPPOK(map[string]any{"transaction_type": "Charge", "transaction": map[string]any{
				"uid": c.uid, "payment_request_uid": c.page, "status_code": "000",
				"amount": json.Number(payplus.MinorToMajor(c.amount)), "currency": c.currency,
			}, "related_transactions": rel}))
			return
		}
		page := fmt.Sprint(body["payment_request_uid"])
		s.mu.Lock()
		uid, ok := s.byPage[page]
		c := s.charges[uid]
		s.mu.Unlock()
		if !ok {
			stubPPJSON(w, http.StatusOK, stubPPFail("payment request not found"))
			return
		}
		stubPPJSON(w, http.StatusOK, stubPPOK(map[string]any{"transaction": map[string]any{
			"uid": c.uid, "payment_request_uid": c.page, "status_code": "000",
			"amount": json.Number(payplus.MinorToMajor(c.amount)), "currency": c.currency,
		}}))
	case "Transactions/View":
		mi := fmt.Sprint(body["more_info"])
		out := []any{}
		s.mu.Lock()
		for _, rf := range s.refunds {
			if rf.moreInfo == mi {
				out = append(out, map[string]any{"transaction": map[string]any{
					"transaction_uid": rf.uid, "transaction_type": "Refund", "status_code": "000",
					"amount": json.Number(payplus.MinorToMajor(rf.amount)), "more_info": rf.moreInfo,
				}})
			}
		}
		s.mu.Unlock()
		stubPPJSON(w, http.StatusOK, stubPPOK(out))
	case "Transactions/RefundByTransactionUID":
		uid := fmt.Sprint(body["transaction_uid"])
		amt, _ := payplus.MajorToMinor(fmt.Sprint(body["amount"]))
		s.mu.Lock()
		c, ok := s.charges[uid]
		var done int64
		for _, rf := range s.refunds {
			if rf.charge == uid {
				done += rf.amount
			}
		}
		if !ok || amt <= 0 || done+amt > c.amount {
			s.mu.Unlock()
			stubPPJSON(w, http.StatusOK, stubPPFail("can not refund this transaction"))
			return
		}
		s.seq++
		rf := stubPPRefund{uid: fmt.Sprintf("pp-refund-%04d", s.seq), charge: uid, moreInfo: fmt.Sprint(body["more_info"]), amount: amt}
		s.refunds = append(s.refunds, rf)
		s.mu.Unlock()
		stubPPJSON(w, http.StatusOK, stubPPOK(map[string]any{"transaction": map[string]any{
			"uid": rf.uid, "type": "internal_page", "status_code": "000",
			"amount": json.Number(payplus.MinorToMajor(amt)), "currency": c.currency, "more_info": rf.moreInfo,
		}}))
	default:
		http.NotFound(w, r)
	}
}

// payplusFixture is a hostedOrgFixture re-pointed at PayPlus and shekels.
type payplusFixture struct {
	*hostedOrgFixture
	apiKey, secretKey, pageUID string
}

func newPayPlusFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) *payplusFixture {
	t.Helper()
	f := &payplusFixture{hostedOrgFixture: newHostedOrgFixture(t, ctx, pool, label)}
	f.apiKey = uuid.NewString()
	f.secretKey = "pp_secret_" + label + "_" + f.orgID.String()[:8]
	f.pageUID = uuid.NewString()
	secrets, _ := json.Marshal(map[string]string{"api_key": f.apiKey, "secret_key": f.secretKey, "payment_page_uid": f.pageUID})
	for i, s := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE sales_channels SET provider = 'payplus' WHERE id = $1`, []any{f.channelID}},
		{`UPDATE payment_provider_configs SET provider = 'payplus', secrets = $2::jsonb WHERE id = $1`, []any{f.configID, string(secrets)}},
		{`UPDATE sessions SET currency = 'ILS' WHERE id = $1`, []any{f.sessionID}},
		{`UPDATE ticket_tiers SET currency = 'ILS' WHERE session_id = $1`, []any{f.sessionID}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("payplusFixture step %d: %v", i, err)
		}
	}
	return f
}

// callback builds a PayPlus charge callback as PayPlus's service sends it:
// JSON.stringify output, the amount in major units.
func payplusCallback(t *testing.T, pageUID, txUID, statusCode string, amountMinor int64, moreInfo string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"transaction_type": "Charge",
		"transaction": map[string]any{
			"uid": txUID, "payment_request_uid": pageUID, "number": "it001", "type": "internal_page",
			"date": "2026-10-28 12:00:00", "status_code": statusCode,
			"amount":   json.Number(payplus.MinorToMajor(amountMinor)),
			"currency": "ILS", "credit_terms": "regular", "approval_number": "002341", "more_info": moreInfo,
		},
		"data": map[string]any{"customer_uid": "c-1", "card_information": map[string]any{"four_digits": "9844"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func postPayPlusCallback(t *testing.T, srv *Server, path string, body []byte, secret string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PayPlus")
	if secret != "" {
		req.Header.Set("hash", payplus.Sign(secret, body))
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestPayPlus_TicketPurchaseThroughHostedCheckout(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPayPlusFixture(t, ctx, pool, "pp")
	defer f.cleanup()

	stub := newStubPayPlusServer(t, f.apiKey, f.secretKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.payplusAPIBaseURL = stub.baseURL()
	srv.cfg.AppPublicURL = "https://api.arena-integration.test"
	q := gen.New(pool)

	// ── 1. checkout/start → a PayPlus payment link ──────────────────────────
	returnURL := hostedTicketsBaseURL + "/shows/wine"
	code, body := f.startCheckoutWithLocale(t, srv, returnURL, "he")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !strings.HasPrefix(start.RedirectURL, "https://paymentsdev.payplus.test/") {
		t.Fatalf("redirect_url = %q; want the PayPlus page", start.RedirectURL)
	}
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Total == nil {
		t.Fatalf("checkout session: %v / %+v", err, cs)
	}
	pageUID := strings.TrimPrefix(start.RedirectURL, "https://paymentsdev.payplus.test/")

	// ── 2. What PayPlus was asked for ───────────────────────────────────────
	link := stub.lastLink(t)
	if got, want := fmt.Sprint(link["amount"]), payplus.MinorToMajor(*cs.Total); got != want {
		t.Errorf("payplus amount = %s; want %s, the platform total %d in MAJOR units", got, want, *cs.Total)
	}
	if link["currency_code"] != "ILS" || link["payment_page_uid"] != f.pageUID || link["more_info"] != csID.String() {
		t.Errorf("link = currency %v page %v more_info %v", link["currency_code"], link["payment_page_uid"], link["more_info"])
	}
	if link["language_code"] != "he" || fmt.Sprint(link["charge_method"]) != "1" || link["send_failure_callback"] != true {
		t.Errorf("link language/charge_method/send_failure_callback = %v %v %v", link["language_code"], link["charge_method"], link["send_failure_callback"])
	}
	wantCB := "https://api.arena-integration.test/v1/payment-intents/webhook/" + f.configID.String()
	if link["refURL_callback"] != wantCB {
		t.Errorf("refURL_callback = %v; want %s", link["refURL_callback"], wantCB)
	}
	wantReturn := "https://api.arena-integration.test/v1/public/payment-return?" +
		url.Values{"r": {returnURL}, "checkout_token": {start.CheckoutToken}}.Encode()
	if link["refURL_success"] != wantReturn || link["refURL_cancel"] != wantReturn || link["refURL_failure"] != wantReturn {
		t.Errorf("refURLs = %v / %v / %v; want the payment-return hop %s", link["refURL_success"], link["refURL_cancel"], link["refURL_failure"], wantReturn)
	}
	if m := fmt.Sprint(link["expiry_datetime"]); m != "31" {
		t.Errorf("expiry_datetime = %s; want 31 minutes (the 1860 s widget window, rounded down)", m)
	}

	// ── 3. The intent is keyed by page_request_uid ──────────────────────────
	pi, err := q.GetPaymentIntentByProviderID(ctx, pageUID)
	if err != nil {
		t.Fatalf("payment intent by page_request_uid: %v", err)
	}
	if pi.Provider != "payplus" || pi.State != "created" || pi.Amount != *cs.Total || !strings.EqualFold(pi.Currency, "ILS") {
		t.Fatalf("payment intent = %+v; want payplus created %d ILS", pi, *cs.Total)
	}
	path := f.configWebhookPath()

	// ── 4. A declined attempt leaves the link open ──────────────────────────
	declined := payplusCallback(t, pageUID, uuid.NewString(), "003", *cs.Total, csID.String())
	if code, body = postPayPlusCallback(t, srv, path, declined, f.secretKey); code != http.StatusOK {
		t.Fatalf("declined callback = %d; body: %s", code, body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, pageUID); after.State != "created" {
		t.Fatalf("a declined attempt moved the intent to %q; the buyer's next card would be ignored", after.State)
	}

	// ── 5. Wrong hash, missing hash, legacy route ──────────────────────────
	txUID := uuid.NewString()
	good := payplusCallback(t, pageUID, txUID, "000", *cs.Total, csID.String())
	if code, _ = postPayPlusCallback(t, srv, path, good, "not-the-secret"); code != http.StatusUnauthorized {
		t.Errorf("callback signed with the wrong key = %d; want 401", code)
	}
	if code, _ = postPayPlusCallback(t, srv, path, good, ""); code != http.StatusUnauthorized {
		t.Errorf("callback without a hash = %d; want 401", code)
	}
	code, body = postPayPlusCallback(t, srv, "/v1/payment-intents/webhook", good, f.secretKey)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "webhook.payplus_requires_config_route") {
		t.Errorf("legacy route = %d %s; want 400 webhook.payplus_requires_config_route", code, body)
	}
	if after, _ := q.GetPaymentIntentByProviderID(ctx, pageUID); after.State != "created" {
		t.Fatalf("a refused callback moved the intent to %q", after.State)
	}

	// ── 6. The signed approved callback pays the order ─────────────────────
	code, body = postPayPlusCallback(t, srv, path, good, f.secretKey)
	if code != http.StatusOK {
		t.Fatalf("approved callback = %d; body: %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if processed, _ := resp["processed"].(bool); !processed {
		t.Fatalf("approved callback processed = %v; body: %s", resp["processed"], body)
	}
	piAfter, _ := q.GetPaymentIntentByProviderID(ctx, pageUID)
	if piAfter.State != "succeeded" {
		t.Errorf("intent = %q; want succeeded", piAfter.State)
	}
	if piAfter.ProviderChargeRef == nil || *piAfter.ProviderChargeRef != txUID {
		t.Errorf("provider_charge_ref = %v; want transaction.uid %s", piAfter.ProviderChargeRef, txUID)
	}
	if order, _ := q.GetOrderByCheckoutSession(ctx, csID); order.Status != "paid" {
		t.Errorf("order = %q; want paid", order.Status)
	}
	if st := f.getStatus(t, srv, start.CheckoutToken); st.Status != "paid" {
		t.Errorf("public status = %q; want paid", st.Status)
	}

	// ── 7. A replay is a no-op ─────────────────────────────────────────────
	if code, body = postPayPlusCallback(t, srv, path, good, f.secretKey); code != http.StatusOK {
		t.Errorf("replayed callback = %d; want 200; body: %s", code, body)
	}
	var issueJobs int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs
	   WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`, csID.String()).Scan(&issueJobs)
	if issueJobs != 1 {
		t.Errorf("checkout.issue_tickets jobs = %d after a replay; want exactly 1", issueJobs)
	}
}

// TestPayPlus_PaidAmountMismatchIsParked: a genuinely signed approved callback
// whose amount differs from the payment cannot complete the checkout.
func TestPayPlus_PaidAmountMismatchIsParked(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPayPlusFixture(t, ctx, pool, "ppm")
	defer f.cleanup()

	stub := newStubPayPlusServer(t, f.apiKey, f.secretKey)
	srv := buildHostedCheckoutServer(t, pool, "")
	srv.payplusAPIBaseURL = stub.baseURL()
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
	pageUID := strings.TrimPrefix(start.RedirectURL, "https://paymentsdev.payplus.test/")

	short := payplusCallback(t, pageUID, uuid.NewString(), "000", *cs.Total-100, csID.String())
	if code, body = postPayPlusCallback(t, srv, f.configWebhookPath(), short, f.secretKey); code != http.StatusOK {
		t.Fatalf("callback = %d; body: %s", code, body)
	}
	after, _ := q.GetPaymentIntentByProviderID(ctx, pageUID)
	if after.State != "manual_review" {
		t.Fatalf("intent = %s; want manual_review for a paid amount that differs", after.State)
	}
	if csAfter, _ := q.GetCheckoutSessionByID(ctx, csID); csAfter.State == "completed" {
		t.Error("checkout completed by an underpaid callback")
	}
}

// TestPayPlus_PartialRefundThroughRefundByTransactionUID refunds one of two
// tickets (a partial refund of the payment), then the other.
func TestPayPlus_PartialRefundThroughRefundByTransactionUID(t *testing.T) {
	apiKey, secret := uuid.NewString(), "pp_refund_"+uuid.NewString()[:8]
	stub := newStubPayPlusServer(t, apiKey, secret)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.PayPlusAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pprefund-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PPREFUND")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	ctx := context.Background()
	pool := srv.pgxPool

	secrets, _ := json.Marshal(map[string]string{"api_key": apiKey, "secret_key": secret, "payment_page_uid": uuid.NewString()})
	f := newPay03Order(t, pool, org, "payplus")
	if _, err := pool.Exec(ctx, `UPDATE payment_provider_configs SET provider = 'payplus', secrets = $2::jsonb
	    WHERE org_id = $1 AND provider = 'stripe'`, org, string(secrets)); err != nil {
		t.Fatal(err)
	}
	var page string
	if err := pool.QueryRow(ctx, `SELECT provider_payment_id FROM payment_intents WHERE id = $1`, f.pay).Scan(&page); err != nil {
		t.Fatal(err)
	}
	// The callback never stored the charge's transaction uid: the module reads
	// it back from the link (PaymentPages/ipn) before refunding.
	chargeUID := "pp-charge-" + uuid.NewString()[:8]
	stub.charge(chargeUID, page, 5000, "EUR")

	refundTicket := func(i int) string {
		t.Helper()
		body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"PayPlus refund test"}`, f.tickets[i])
		st, out := refundOrgPost(t, pf, "/v1/organizations/"+org.String()+"/orders/"+f.order.String()+"/refunds",
			key, "pprefund-"+uuid.NewString(), body)
		if st != http.StatusAccepted {
			t.Fatalf("refund ticket %d: %d %v", i, st, out)
		}
		return out["refunds"].([]any)[0].(map[string]any)["id"].(string)
	}
	refundRow := func(id string) (state, providerRefundID string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT state, COALESCE(provider_refund_id, '') FROM refunds WHERE id = $1`, id).
			Scan(&state, &providerRefundID); err != nil {
			t.Fatal(err)
		}
		return
	}

	// ── 1. Half of the payment: one ticket of two.
	first := refundTicket(0)
	if state, prid := refundRow(first); state != "succeeded" || !strings.HasPrefix(prid, "pp-refund-") {
		t.Fatalf("partial refund = %s %q; want succeeded with the PayPlus refund transaction uid", state, prid)
	}
	if got := stub.refundsOf(chargeUID); len(got) != 1 || got[0] != 2500 {
		t.Fatalf("PayPlus refunded %v; want [2500] through the charge's transaction uid", got)
	}
	var ref *string
	_ = pool.QueryRow(ctx, `SELECT provider_charge_ref FROM payment_intents WHERE id = $1`, f.pay).Scan(&ref)
	if ref == nil || *ref != chargeUID {
		t.Errorf("provider_charge_ref = %v; want %s stored after the read-back", ref, chargeUID)
	}
	if s, link := f.ticket(t, 0); s != "cancelled" || link == nil || link.String() != first {
		t.Errorf("ticket 0 = %s %v; want cancelled by %s", s, link, first)
	}
	if s, _ := f.ticket(t, 1); s != "active" {
		t.Errorf("ticket 1 = %s; a partial refund must leave it valid", s)
	}

	// ── 2. The rest.
	second := refundTicket(1)
	if state, _ := refundRow(second); state != "succeeded" {
		t.Fatalf("second refund = %s; want succeeded", state)
	}
	if got := stub.refundsOf(chargeUID); len(got) != 2 || got[1] != 2500 {
		t.Fatalf("PayPlus refunded %v; want [2500 2500]", got)
	}
	var orderStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&orderStatus)
	if orderStatus != "refunded" {
		t.Errorf("order = %s; want refunded", orderStatus)
	}
}

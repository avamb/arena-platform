//go:build integration

// checkout_prepare_integration_test.go — spec 65 early order ("prepare +
// attach"), through the real router with only Stripe stubbed:
//
//   - prepare -> attach -> checkout.session.completed pays the order with the
//     attached e-mail; the order is out of the funnel, the reminder and the
//     organizer's lists until the buyer is attached;
//   - a completed session of an order nobody attached fills the e-mail Stripe
//     collected;
//   - prepare is refused (promo, marketing tick, buyer, no capability, hosted
//     channel, switch off, free total, embedded failure) and leaves nothing
//     held and no order pending;
//   - attach validates like checkout/start, is idempotent for the same
//     e-mail, refuses another, and refuses an order that cannot be paid;
//   - cancel expires the embedded session at Stripe first, and refuses while
//     Stripe says it is paid;
//   - the same Idempotency-Key twice makes one order.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/cartreminder"
)

type prepareAnswer struct {
	embeddedStartResponse
	Prepared         bool   `json:"prepared"`
	ExpiresAt        string `json:"expires_at"`
	PaymentExpiresAt string `json:"payment_expires_at"`
}

// postPrepare sends checkout/start with prepare:true; extra fields are merged
// into the plain cart.
func postPrepare(t *testing.T, srv *Server, f *hostedOrgFixture, extra map[string]any, idemKey string) (int, []byte) {
	t.Helper()
	payload := map[string]any{
		"session_id":   f.sessionID.String(),
		"tier_id":      f.tierID.String(),
		"qty":          1,
		"return_url":   hostedTicketsBaseURL + "/shows/prepared",
		"capabilities": []string{"stripe_elements"},
		"prepare":      true,
	}
	for k, v := range extra {
		if v == nil {
			delete(payload, k)
			continue
		}
		payload[k] = v
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func prepareOK(t *testing.T, srv *Server, f *hostedOrgFixture) prepareAnswer {
	t.Helper()
	code, body := postPrepare(t, srv, f, nil, "")
	if code != http.StatusCreated {
		t.Fatalf("prepare = %d %s", code, body)
	}
	var out prepareAnswer
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode prepare: %v", err)
	}
	if !out.Prepared || out.Payment == nil || out.Payment.ClientSecret == "" || out.RedirectURL != "" {
		t.Fatalf("prepare answer = %s", body)
	}
	return out
}

func postJSON(t *testing.T, srv *Server, path string, payload any) (int, map[string]any, http.Header) {
	t.Helper()
	var rd *bytes.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, rd)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func prepErrCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func attachBuyer(t *testing.T, srv *Server, token string, buyer map[string]any, extra map[string]any) (int, map[string]any) {
	t.Helper()
	// A paid sale needs the tick (spec 68 5.1): sent unless extra names
	// "consent" itself (nil there sends none).
	payload := map[string]any{"buyer": buyer, "consent": map[string]any{"accepted": true}}
	for k, v := range extra {
		payload[k] = v
	}
	code, out, hdr := postJSON(t, srv, "/v1/public/checkout/"+token+"/buyer", payload)
	if cc := hdr.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("/buyer Cache-Control = %q, want no-store", cc)
	}
	return code, out
}

func orderOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID string) gen.OrderRow {
	t.Helper()
	ord, err := gen.New(pool).GetOrderByCheckoutSession(ctx, uuid.MustParse(csID))
	if err != nil {
		t.Fatalf("order of %s: %v", csID, err)
	}
	return ord
}

func liveHolds(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE session_id = $1 AND state IN ('draft', 'active')`,
		sessionID).Scan(&n); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	return n
}

func heldPlaces(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND status = 'held'`,
		sessionID).Scan(&n); err != nil {
		t.Fatalf("count held places: %v", err)
	}
	return n
}

func pendingOrders(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE org_id = $1 AND status = 'pending_payment'`,
		orgID).Scan(&n); err != nil {
		t.Fatalf("count pending orders: %v", err)
	}
	return n
}

// completedWithEmail is checkout.session.completed with the e-mail address
// Stripe collected.
func completedWithEmail(t *testing.T, sessionID, piRef, email string) []byte {
	t.Helper()
	var ev map[string]any
	if err := json.Unmarshal(sessionCompletedEvent(sessionID, piRef, "paid"), &ev); err != nil {
		t.Fatal(err)
	}
	obj := ev["data"].(map[string]any)["object"].(map[string]any)
	obj["customer_details"] = map[string]any{"email": email}
	raw, _ := json.Marshal(ev)
	return raw
}

func funnelStarted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *hostedOrgFixture) int64 {
	t.Helper()
	now := time.Now().UTC()
	tot, err := gen.New(pool).GetFunnelServerTotals(ctx, gen.FunnelWindow{
		EventID: f.eventID, OrgID: f.orgID,
		From: now.AddDate(0, 0, -1), To: now.AddDate(0, 0, 1), Zone: "Europe/Prague",
	})
	if err != nil {
		t.Fatalf("funnel totals: %v", err)
	}
	return tot.Started
}

func listedOrders(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *hostedOrgFixture) (search int64, flat int, summary int64) {
	t.Helper()
	q := gen.New(pool)
	n, err := q.CountOrdersByOrg(ctx, gen.OrderSearchParams{OrgID: f.orgID})
	if err != nil {
		t.Fatalf("CountOrdersByOrg: %v", err)
	}
	rows, err := q.ListOrdersByOrg(ctx, f.orgID, "", "", nil, nil, nil, 50, 0)
	if err != nil {
		t.Fatalf("ListOrdersByOrg: %v", err)
	}
	sum, err := q.ListSessionSummaryOrders(ctx, []uuid.UUID{f.sessionID})
	if err != nil {
		t.Fatalf("ListSessionSummaryOrders: %v", err)
	}
	for _, s := range sum {
		summary += s.Orders
	}
	return n, len(rows), summary
}

func TestCheckoutPrepare_AttachThenCompletedWebhookPaysWithBuyerEmail(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, pk := embeddedFixture(t, ctx, pool, "pra")
	defer f.cleanup()
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM checkout_reminders WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID)
	}()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	if start.Payment.PublishableKey != pk || start.ExpiresAt == "" || start.PaymentExpiresAt == "" {
		t.Fatalf("prepare answer = %+v", start)
	}
	form := stripe.lastForm(t)
	if form.Get("ui_mode") != "elements" || form.Has("customer_email") || form.Has("payment_intent_data[receipt_email]") {
		t.Fatalf("the prepared Stripe session must carry no buyer: %v", form)
	}
	ord := orderOf(t, ctx, pool, start.CheckoutSess.ID)
	if ord.Status != "pending_payment" || ord.BuyerEmail != nil || ord.CustomerID != nil || ord.Source != "public_feed" {
		t.Fatalf("prepared order = status %q email %v customer %v source %q", ord.Status, ord.BuyerEmail, ord.CustomerID, ord.Source)
	}

	// Unattached: not a started checkout anywhere the organizer looks.
	if n := funnelStarted(t, ctx, pool, f); n != 0 {
		t.Errorf("funnel started = %d with only an unattached order, want 0", n)
	}
	if s, l, sum := listedOrders(t, ctx, pool, f); s != 0 || l != 0 || sum != 0 {
		t.Errorf("organizer lists show the unattached order: search %d, list %d, summary %d", s, l, sum)
	}
	sweepAt := ord.CreatedAt.Add(cartreminder.DueAfter + time.Second)
	if ord.ExpiresAt == nil || ord.ExpiresAt.Before(sweepAt.Add(cartreminder.MinLeftAtPick)) {
		t.Fatalf("fixture window too short for the reminder check: expires %v", ord.ExpiresAt)
	}
	if n, err := cartreminder.SweepOrgs(ctx, pool, sweepAt, []uuid.UUID{f.orgID}); err != nil || n != 0 {
		t.Fatalf("reminder sweep queued %d (%v) for an unattached order, want 0", n, err)
	}

	tok := start.CheckoutToken
	// Validation, as checkout/start.
	if code, out := attachBuyer(t, srv, tok, map[string]any{"email": "not-an-address"}, nil); code != http.StatusBadRequest || prepErrCode(out) != "checkout.invalid_email" {
		t.Fatalf("invalid e-mail = %d %v", code, out)
	}
	if code, out := attachBuyer(t, srv, tok, map[string]any{"email": f.buyerMail, "phone": f.buyerPhone}, nil); code != http.StatusUnprocessableEntity || prepErrCode(out) != "checkout.buyer_name_required" {
		t.Fatalf("missing name = %d %v", code, out)
	}
	if code, out := attachBuyer(t, srv, tok, map[string]any{"email": f.buyerMail, "name": "Prepared Buyer"}, nil); code != http.StatusUnprocessableEntity || prepErrCode(out) != "checkout.buyer_phone_required" {
		t.Fatalf("missing phone = %d %v", code, out)
	}
	// The channel asks for the consent (complete seller profile + switch).
	if _, err := pool.Exec(ctx, `UPDATE organizations SET legal_name = 'Prepared Teatro S.L.',
		tax_id_scheme = 'eu_vat', tax_id = 'ESB12345678', legal_address_line1 = 'Calle Mayor 1',
		legal_address_city = 'Madrid', legal_address_postal_code = '28001', legal_address_country = 'ES'
		WHERE id = $1`, f.orgID); err != nil {
		t.Fatalf("legal data: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings =
		'{"hosted_page":{"enabled":true,"consent_required":true,"public_contact":{"email":"tickets@prepared.example"}}}'::jsonb
		WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("consent switch: %v", err)
	}
	buyer := map[string]any{"email": f.buyerMail, "name": "Prepared Buyer", "phone": f.buyerPhone}
	if code, out := attachBuyer(t, srv, tok, buyer, map[string]any{"consent": nil}); code != http.StatusUnprocessableEntity || prepErrCode(out) != "checkout.consent_required" {
		t.Fatalf("missing consent = %d %v", code, out)
	}
	if o := orderOf(t, ctx, pool, start.CheckoutSess.ID); o.BuyerEmail != nil {
		t.Fatal("a refused attach wrote the buyer")
	}

	code, out := attachBuyer(t, srv, tok, buyer, map[string]any{"consent": map[string]any{"accepted": true}, "locale": "cs-CZ"})
	if code != http.StatusOK || out["status"] != "pending" || out["checkout_token"] != tok || out["expires_at"] == nil || out["payment_expires_at"] == nil {
		t.Fatalf("attach = %d %v", code, out)
	}
	ord = orderOf(t, ctx, pool, start.CheckoutSess.ID)
	if ord.BuyerEmail == nil || *ord.BuyerEmail != f.buyerMail || ord.CustomerID == nil ||
		ord.BuyerName == nil || *ord.BuyerName != "Prepared Buyer" || ord.BuyerPhone == nil {
		t.Fatalf("attached order = email %v name %v phone %v customer %v", ord.BuyerEmail, ord.BuyerName, ord.BuyerPhone, ord.CustomerID)
	}
	var consents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM checkout_consents WHERE checkout_session_id = $1`,
		uuid.MustParse(start.CheckoutSess.ID)).Scan(&consents); err != nil || consents != 1 {
		t.Fatalf("checkout_consents = %d (%v), want 1", consents, err)
	}
	var resCustomer *uuid.UUID
	var locale *string
	if err := pool.QueryRow(ctx, `SELECT r.customer_id, cs.buyer_locale FROM checkout_sessions cs JOIN reservations r ON r.id = cs.reservation_id
		WHERE cs.id = $1`, uuid.MustParse(start.CheckoutSess.ID)).Scan(&resCustomer, &locale); err != nil {
		t.Fatalf("reservation customer: %v", err)
	}
	if resCustomer == nil || *resCustomer != *ord.CustomerID || locale == nil || *locale != "cs" {
		t.Fatalf("reservation customer %v (order %v), locale %v", resCustomer, ord.CustomerID, locale)
	}

	// Same e-mail again: 200, nothing written (no second consent row).
	if code, out := attachBuyer(t, srv, tok, map[string]any{"email": strings.ToUpper(f.buyerMail)}, nil); code != http.StatusOK || out["status"] != "pending" {
		t.Fatalf("repeat attach = %d %v", code, out)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM checkout_consents WHERE checkout_session_id = $1`,
		uuid.MustParse(start.CheckoutSess.ID)).Scan(&consents); err != nil || consents != 1 {
		t.Fatalf("checkout_consents after a repeat = %d (%v), want 1", consents, err)
	}
	// Another e-mail: refused.
	if code, out := attachBuyer(t, srv, tok, map[string]any{"email": "someone-else@arena-integration.test"}, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.buyer_already_set" {
		t.Fatalf("other e-mail = %d %v", code, out)
	}

	// Attached: counted as a started checkout and listed.
	if n := funnelStarted(t, ctx, pool, f); n != 1 {
		t.Errorf("funnel started after attach = %d, want 1", n)
	}
	if s, l, sum := listedOrders(t, ctx, pool, f); s != 1 || l != 1 || sum != 1 {
		t.Errorf("organizer lists after attach: search %d, list %d, summary %d; want 1 each", s, l, sum)
	}

	// Stripe pays; its collected address differs, the attached one stays.
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	code, body := postSignedWebhook(t, srv, completedWithEmail(t, cs, "pi_pra_"+uuid.NewString()[:8], "typed-in-stripe@arena-integration.test"), f.webhookSecret)
	if code != http.StatusOK || !strings.Contains(string(body), `"checkout_completed":true`) {
		t.Fatalf("paid webhook = %d %s", code, body)
	}
	ord = orderOf(t, ctx, pool, start.CheckoutSess.ID)
	if ord.Status != "paid" || ord.BuyerEmail == nil || *ord.BuyerEmail != f.buyerMail {
		t.Fatalf("paid order = %q email %v", ord.Status, ord.BuyerEmail)
	}
	if n := countIssueJobs(t, ctx, pool, start.CheckoutSess.ID); n != 1 {
		t.Errorf("issue_tickets jobs = %d, want 1", n)
	}
	// Paid: the attach is refused as not payable.
	if code, out := attachBuyer(t, srv, tok, map[string]any{"email": f.buyerMail}, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.not_payable" {
		t.Fatalf("attach after payment = %d %v", code, out)
	}
}

func TestCheckoutPrepare_CompletedWebhookFillsMissingEmail(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prb")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	collected := "collected-" + f.orgID.String()[:8] + "@arena-integration.test"
	code, body := postSignedWebhook(t, srv, completedWithEmail(t, cs, "pi_prb_"+uuid.NewString()[:8], collected), f.webhookSecret)
	if code != http.StatusOK || !strings.Contains(string(body), `"checkout_completed":true`) {
		t.Fatalf("paid webhook = %d %s", code, body)
	}
	ord := orderOf(t, ctx, pool, start.CheckoutSess.ID)
	if ord.Status != "paid" || ord.BuyerEmail == nil || *ord.BuyerEmail != collected {
		t.Fatalf("paid order = %q email %v, want %s", ord.Status, ord.BuyerEmail, collected)
	}
	// Paid: listed and counted even though the buyer was never attached.
	if n := funnelStarted(t, ctx, pool, f); n != 1 {
		t.Errorf("funnel started = %d, want 1", n)
	}
}

func TestCheckoutPrepare_RefusalsLeaveNothingHeld(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prc")
	defer f.cleanup()
	stripe := newStubStripe(t)
	on := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	assertRefused := func(name string, srv *Server, extra map[string]any, wantStatus int) {
		t.Helper()
		before := len(stripe.sessions)
		code, body := postPrepare(t, srv, f, extra, "")
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		if code != wantStatus || prepErrCode(out) != "checkout.prepare_unavailable" {
			t.Errorf("%s: prepare = %d %s, want %d checkout.prepare_unavailable", name, code, body, wantStatus)
		}
		if n := liveHolds(t, ctx, pool, f.sessionID); n != 0 {
			t.Errorf("%s: %d holds left", name, n)
		}
		if n := heldPlaces(t, ctx, pool, f.sessionID); n != 0 {
			t.Errorf("%s: %d places held", name, n)
		}
		if n := pendingOrders(t, ctx, pool, f.orgID); n != 0 {
			t.Errorf("%s: %d orders left pending", name, n)
		}
		if name != "embedded session refused" && len(stripe.sessions) != before {
			t.Errorf("%s: a Stripe session was created", name)
		}
	}

	assertRefused("promo code", on, map[string]any{"promo_code": "SAVE10"}, http.StatusUnprocessableEntity)
	assertRefused("marketing tick", on, map[string]any{"marketing_consent": map[string]any{"accepted": true}}, http.StatusUnprocessableEntity)
	assertRefused("buyer present", on, map[string]any{"buyer": map[string]any{"email": f.buyerMail}}, http.StatusUnprocessableEntity)
	assertRefused("holder_email present", on, map[string]any{"holder_email": f.buyerMail}, http.StatusUnprocessableEntity)
	assertRefused("consent present", on, map[string]any{"consent": map[string]any{"accepted": true}}, http.StatusUnprocessableEntity)
	assertRefused("no capability", on, map[string]any{"capabilities": nil}, http.StatusUnprocessableEntity)

	off := buildEmbeddedServer(t, pool, stripe.baseURL(), false)
	assertRefused("STRIPE_ELEMENTS_ENABLED off", off, nil, http.StatusConflict)

	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = '{"checkout":{"stripe_ui":"hosted"}}'::jsonb WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("set stripe_ui: %v", err)
	}
	assertRefused("hosted-only channel", on, nil, http.StatusConflict)
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = '{}'::jsonb WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("reset stripe_ui: %v", err)
	}

	// The embedded session cannot be created: no hosted page, the order is
	// cancelled and the places go back at once.
	stripe.failNextElementsCreates(1)
	assertRefused("embedded session refused", on, nil, http.StatusConflict)
	var cancelled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE org_id = $1 AND status = 'cancelled'`, f.orgID).Scan(&cancelled); err != nil || cancelled != 1 {
		t.Errorf("cancelled orders after the refused session = %d (%v), want 1", cancelled, err)
	}
	for _, form := range stripe.sessions {
		if form.Get("ui_mode") != "elements" {
			t.Errorf("a hosted page was created for a prepared order: %v", form)
		}
	}
	// It is not in the organizer's lists either (unattached, never paid).
	if s, l, sum := listedOrders(t, ctx, pool, f); s != 0 || l != 0 || sum != 0 {
		t.Errorf("organizer lists show a cancelled unattached order: search %d, list %d, summary %d", s, l, sum)
	}

	// A free cart would complete without a buyer.
	if _, err := pool.Exec(ctx, `UPDATE ticket_tiers SET pricing_mode = 'free', price_amount = 0 WHERE id = $1`, f.tierID); err != nil {
		t.Fatalf("free tier: %v", err)
	}
	assertRefused("free total", on, nil, http.StatusConflict)
}

func TestCheckoutPrepare_CancelExpiresStripeFirst(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prd")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	// Sheet closed: Stripe expires the session, then the places go back.
	start := prepareOK(t, srv, f)
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	if n := heldPlaces(t, ctx, pool, f.sessionID); n != 1 {
		t.Fatalf("held places after prepare = %d, want 1", n)
	}
	code, out, _ := postJSON(t, srv, "/v1/public/checkout/"+start.CheckoutToken+"/cancel", nil)
	if code != http.StatusOK || out["status"] != "cancelled" || out["cancelled"] != true {
		t.Fatalf("cancel = %d %v", code, out)
	}
	if ex := stripe.expired(); len(ex) != 1 || ex[0] != cs {
		t.Fatalf("expired at Stripe = %v, want [%s]", ex, cs)
	}
	if e, _ := stripe.elementsSession(cs); e.status != "expired" {
		t.Fatalf("stub session status = %q, want expired", e.status)
	}
	if n := heldPlaces(t, ctx, pool, f.sessionID); n != 0 {
		t.Errorf("held places after cancel = %d, want 0", n)
	}
	if o := orderOf(t, ctx, pool, start.CheckoutSess.ID); o.Status != "cancelled" {
		t.Errorf("order after cancel = %q", o.Status)
	}
	// A repeated cancel answers the same and calls nothing that changes.
	if code, out, _ := postJSON(t, srv, "/v1/public/checkout/"+start.CheckoutToken+"/cancel", nil); code != http.StatusOK || out["cancelled"] != true {
		t.Fatalf("repeat cancel = %d %v", code, out)
	}
	// A cancelled order takes no buyer.
	if code, out := attachBuyer(t, srv, start.CheckoutToken, map[string]any{"email": f.buyerMail}, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.not_payable" {
		t.Fatalf("attach after cancel = %d %v", code, out)
	}

	// Stripe refuses: the session is paid. Nothing is cancelled.
	second := prepareOK(t, srv, f)
	cs2 := *intentsOf(t, ctx, pool, second.CheckoutSess.ID)[0].ProviderPaymentID
	stripe.markElements(cs2, "complete", "paid")
	code, out, _ = postJSON(t, srv, "/v1/public/checkout/"+second.CheckoutToken+"/cancel", nil)
	if code != http.StatusConflict || prepErrCode(out) != "checkout.payment_in_progress" {
		t.Fatalf("cancel of a paid session = %d %v", code, out)
	}
	if o := orderOf(t, ctx, pool, second.CheckoutSess.ID); o.Status != "pending_payment" {
		t.Errorf("order after a refused cancel = %q, want pending_payment", o.Status)
	}
	if n := heldPlaces(t, ctx, pool, f.sessionID); n != 1 {
		t.Errorf("held places after a refused cancel = %d, want 1", n)
	}
}

func TestCheckoutPrepare_AttachRefusedAfterTheHoldExpired(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "pre")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() - interval '1 minute'
		WHERE id = (SELECT reservation_id FROM checkout_sessions WHERE id = $1)`, uuid.MustParse(start.CheckoutSess.ID)); err != nil {
		t.Fatalf("expire hold: %v", err)
	}
	if code, out := attachBuyer(t, srv, start.CheckoutToken, map[string]any{"email": f.buyerMail, "name": "N", "phone": f.buyerPhone}, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.not_payable" {
		t.Fatalf("attach after the hold expired = %d %v", code, out)
	}
	if code, out := attachBuyer(t, srv, "no-such-token-"+uuid.NewString(), map[string]any{"email": f.buyerMail}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown token = %d %v", code, out)
	}
}

// The hold is still live but the payment window (hold expiry minus the
// grace, the Stripe session's own expiry) is over: the attach is refused.
func TestCheckoutPrepare_AttachRefusedAfterThePaymentWindow(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prg")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	// The default grace is 120 s: 60 s of hold left is past payment_expires_at.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '60 seconds'
		WHERE id = (SELECT reservation_id FROM checkout_sessions WHERE id = $1)`, uuid.MustParse(start.CheckoutSess.ID)); err != nil {
		t.Fatalf("shorten hold: %v", err)
	}
	if code, out := attachBuyer(t, srv, start.CheckoutToken, map[string]any{"email": f.buyerMail, "name": "N", "phone": f.buyerPhone}, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.not_payable" {
		t.Fatalf("attach inside the grace = %d %v", code, out)
	}
	if o := orderOf(t, ctx, pool, start.CheckoutSess.ID); o.BuyerEmail != nil {
		t.Fatal("a refused attach wrote the buyer")
	}
}

// No active feed publishes the order's event any more: nobody can tell the
// channel's name, phone and consent rules, so the attach is refused instead
// of going on with every rule switched off.
func TestCheckoutPrepare_AttachRefusedWithoutFeedContext(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prh")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	if _, err := pool.Exec(ctx, `UPDATE agent_feed_tokens SET is_active = false WHERE sales_channel_id = $1`, f.channelID); err != nil {
		t.Fatalf("switch the feed off: %v", err)
	}
	// Name and phone left out on purpose: with the flags read as false this
	// attach would have passed.
	if code, out := attachBuyer(t, srv, start.CheckoutToken, map[string]any{"email": f.buyerMail}, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.not_payable" {
		t.Fatalf("attach without a feed context = %d %v", code, out)
	}
	if o := orderOf(t, ctx, pool, start.CheckoutSess.ID); o.BuyerEmail != nil {
		t.Fatal("a refused attach wrote the buyer")
	}
}

// The unpaid-order reminder of a prepared order counts from the attach, not
// from the prepare: an order opened 30 minutes ago and attached just now is
// not due; 16 minutes after the attach it is.
func TestCheckoutPrepare_ReminderCountsFromAttach(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "pri")
	defer f.cleanup()
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = $1
			AND payload->>'order_id' IN (SELECT id::text FROM orders WHERE org_id = $2)`, cartreminder.JobTypeEmail, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM checkout_reminders WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID)
	}()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	if code, out := attachBuyer(t, srv, start.CheckoutToken, map[string]any{"email": f.buyerMail, "name": "Reminded Buyer", "phone": f.buyerPhone}, nil); code != http.StatusOK {
		t.Fatalf("attach = %d %v", code, out)
	}
	ord := orderOf(t, ctx, pool, start.CheckoutSess.ID)
	var attachedEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'buyer_attached'`, ord.ID).Scan(&attachedEvents); err != nil || attachedEvents != 1 {
		t.Fatalf("buyer_attached events = %d (%v), want 1", attachedEvents, err)
	}
	// Opened 30 minutes ago; the window is long enough for the pick.
	if _, err := pool.Exec(ctx, `UPDATE orders SET created_at = now() - interval '30 minutes',
		expires_at = now() + interval '1 hour' WHERE id = $1`, ord.ID); err != nil {
		t.Fatalf("backdate order: %v", err)
	}
	now := time.Now().UTC()
	if n, err := cartreminder.SweepOrgs(ctx, pool, now, []uuid.UUID{f.orgID}); err != nil || n != 0 {
		t.Fatalf("sweep right after the attach queued %d (%v), want 0", n, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE order_events SET created_at = now() - interval '16 minutes'
		WHERE order_id = $1 AND type = 'buyer_attached'`, ord.ID); err != nil {
		t.Fatalf("backdate attach: %v", err)
	}
	if n, err := cartreminder.SweepOrgs(ctx, pool, time.Now().UTC(), []uuid.UUID{f.orgID}); err != nil || n != 1 {
		t.Fatalf("sweep 16 minutes after the attach queued %d (%v), want 1", n, err)
	}
}

// The webhook fills a missing e-mail taking the checkout row before the
// order row, the order of the attach: a transaction that holds the checkout
// and then asks for the order (as the attach does) while the paid webhook
// runs must not deadlock.
func TestCheckoutPrepare_FillEmailLocksCheckoutBeforeOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prj")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	start := prepareOK(t, srv, f)
	csID := uuid.MustParse(start.CheckoutSess.ID)
	ord := orderOf(t, ctx, pool, start.CheckoutSess.ID)
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID

	// "The attach": checkout row locked first.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '10s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM checkout_sessions WHERE id = $1 FOR UPDATE`, csID); err != nil {
		t.Fatalf("lock checkout: %v", err)
	}

	type result struct {
		code int
		body []byte
	}
	done := make(chan result, 1)
	collected := "locked-" + f.orgID.String()[:8] + "@arena-integration.test"
	go func() {
		code, body := postSignedWebhook(t, srv, completedWithEmail(t, cs, "pi_prj_"+uuid.NewString()[:8], collected), f.webhookSecret)
		done <- result{code, body}
	}()
	// Let the webhook reach its first lock.
	time.Sleep(500 * time.Millisecond)
	// "The attach" now asks for the order row. Before the fix the webhook
	// already held it and waited for the checkout row: deadlock.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM orders WHERE id = $1 FOR UPDATE`, ord.ID); err != nil {
		t.Fatalf("lock order behind the webhook: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var res result
	select {
	case res = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the webhook did not finish")
	}
	if res.code != http.StatusOK || !strings.Contains(string(res.body), `"checkout_completed":true`) {
		t.Fatalf("paid webhook = %d %s", res.code, res.body)
	}
	if o := orderOf(t, ctx, pool, start.CheckoutSess.ID); o.Status != "paid" || o.BuyerEmail == nil || *o.BuyerEmail != collected {
		t.Fatalf("paid order = %q email %v", o.Status, o.BuyerEmail)
	}
}

func TestCheckoutPrepare_SameIdempotencyKeyOneOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prf")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	key := uuid.NewString()
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE key = $1`, key)
	}()
	code1, body1 := postPrepare(t, srv, f, nil, key)
	code2, body2 := postPrepare(t, srv, f, nil, key)
	if code1 != http.StatusCreated || code2 != http.StatusCreated {
		t.Fatalf("prepare twice = %d / %d: %s / %s", code1, code2, body1, body2)
	}
	var a, b prepareAnswer
	_ = json.Unmarshal(body1, &a)
	_ = json.Unmarshal(body2, &b)
	if a.CheckoutToken == "" || a.CheckoutToken != b.CheckoutToken || !b.Prepared {
		t.Fatalf("replay answered another checkout: %s vs %s", a.CheckoutToken, b.CheckoutToken)
	}
	var orders int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE org_id = $1`, f.orgID).Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("orders = %d (%v), want 1", orders, err)
	}
	if len(stripe.sessions) != 1 {
		t.Fatalf("stripe sessions = %d, want 1", len(stripe.sessions))
	}
}

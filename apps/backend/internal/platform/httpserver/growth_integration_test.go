//go:build integration

package httpserver

// growth_integration_test.go — spec 51-1 through the real router: the one
// reminder about an unpaid widget order, the resume link, and the discount
// for the marketing consent.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/cartreminder"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

type growthSender struct {
	mu   sync.Mutex
	sent []email.Message
	// failNext makes the next Send fail as a refused relay would.
	failNext bool
}

func (s *growthSender) Send(_ context.Context, m email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return errors.New("relay refused the letter")
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *growthSender) last(t *testing.T) email.Message {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("no letter was sent")
	}
	return s.sent[len(s.sent)-1]
}

func (s *growthSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

const growthAPIBase = "https://api.arena-integration.test"

func growthCleanup(t *testing.T, f *hostedOrgFixture) func() {
	return func() {
		ctx := context.Background()
		for _, s := range []string{
			`DELETE FROM worker_jobs WHERE job_type = 'checkout.reminder_email' AND payload->>'order_id' IN
			   (SELECT id::text FROM orders WHERE org_id = $1)`,
			`DELETE FROM worker_jobs WHERE job_type = 'marketing.confirm_email' AND payload->>'subscription_id' IN
			   (SELECT id::text FROM marketing_subscriptions WHERE org_id = $1)`,
			`DELETE FROM email_letters WHERE org_id = $1`,
			`DELETE FROM checkout_reminders WHERE org_id = $1`,
			`DELETE FROM marketing_consents WHERE org_id = $1`,
			`DELETE FROM marketing_subscriptions WHERE org_id = $1`,
			`DELETE FROM org_growth_settings WHERE org_id = $1`,
			`DELETE FROM promo_code_attempts WHERE org_id = $1`,
			`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
			`UPDATE checkout_sessions SET promo_code_id = NULL WHERE org_id = $1`,
			`UPDATE orders SET promo_code_id = NULL WHERE org_id = $1`,
			`DELETE FROM promo_codes WHERE org_id = $1`,
		} {
			if _, err := f.pool.Exec(ctx, s, f.orgID); err != nil {
				t.Logf("growth cleanup (%.60s): %v", s, err)
			}
		}
	}
}

func growthPost(t *testing.T, srv *Server, path string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func growthGet(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

// growthPostForm presses the button of a link's page: an empty
// multipart/form-data POST, as the page's form sends it.
func growthPostForm(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

// startWith drives checkout/start with an optional promo code and consent.
func (f *hostedOrgFixture) startWith(t *testing.T, srv *Server, mail, promo string, consent map[string]any) (int, []byte) {
	t.Helper()
	body := map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        2,
		"buyer":      map[string]any{"email": mail, "name": "Growth Buyer", "phone": f.buyerPhone},
		"return_url": hostedTicketsBaseURL + "/shows/growth",
	}
	if promo != "" {
		body["promo_code"] = promo
	}
	if consent != nil {
		body["marketing_consent"] = consent
	}
	return growthPost(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/start", body)
}

func TestCartReminder_SentOnceAndResumeLink(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "cr")
	defer f.cleanup()
	defer growthCleanup(t, f)()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	code, body := f.startWith(t, srv, f.buyerMail, "", nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	var orderID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM orders WHERE org_id = $1`, f.orgID).Scan(&orderID); err != nil {
		t.Fatalf("order: %v", err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(return_url, '') FROM checkout_sessions WHERE checkout_token = $1`,
		start.CheckoutToken).Scan(&stored); err != nil || stored != hostedTicketsBaseURL+"/shows/growth" {
		t.Fatalf("return_url stored = %q, %v", stored, err)
	}

	orgs := []uuid.UUID{f.orgID}
	now := time.Now().UTC()
	// Too early: the reminder waits 15 minutes.
	if n, err := cartreminder.SweepOrgs(ctx, pool, now, orgs); err != nil || n != 0 {
		t.Fatalf("early sweep = %d, %v", n, err)
	}
	// Switched off by the organizer: nothing is queued.
	if _, err := pool.Exec(ctx, `INSERT INTO org_growth_settings (org_id, cart_reminder_enabled) VALUES ($1, false)`, f.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET created_at = now() - interval '20 minutes' WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if n, err := cartreminder.SweepOrgs(ctx, pool, now, orgs); err != nil || n != 0 {
		t.Fatalf("disabled sweep = %d, %v", n, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE org_growth_settings SET cart_reminder_enabled = true WHERE org_id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	if n, err := cartreminder.SweepOrgs(ctx, pool, now, orgs); err != nil || n != 1 {
		t.Fatalf("due sweep = %d, %v", n, err)
	}
	if n, err := cartreminder.SweepOrgs(ctx, pool, now.Add(time.Minute), orgs); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v: one reminder per order", n, err)
	}

	sender := &growthSender{failNext: true}
	h := delivery.NewCartReminderHandler(delivery.GrowthLetterOptions{Sender: sender, DB: pool, APIBaseURL: growthAPIBase})
	payload, _ := json.Marshal(cartreminder.JobPayload{OrderID: orderID.String()})
	// A refused send gives the reminder back, so the worker's retry sends it.
	if err := h(ctx, payload); err == nil {
		t.Fatal("a refused send must fail the job so the worker retries it")
	}
	var claimed *time.Time
	if err := pool.QueryRow(ctx, `SELECT sent_at FROM checkout_reminders WHERE order_id = $1`, orderID).Scan(&claimed); err != nil || claimed != nil {
		t.Fatalf("after a refused send sent_at = %v, %v: the claim was not given back", claimed, err)
	}
	if err := h(ctx, payload); err != nil {
		t.Fatalf("reminder handler: %v", err)
	}
	msg := sender.last(t)
	resume := growthAPIBase + "/v1/public/checkout/" + start.CheckoutToken + "/resume?src=reminder"
	if msg.To != f.buyerMail || !strings.Contains(msg.HTMLBody, resume) {
		t.Fatalf("letter to %q without the resume link %q:\n%s", msg.To, resume, msg.HTMLBody)
	}
	var sentAt *time.Time
	var skipped *string
	if err := pool.QueryRow(ctx, `SELECT sent_at, skipped_reason FROM checkout_reminders WHERE order_id = $1`, orderID).
		Scan(&sentAt, &skipped); err != nil || sentAt == nil || skipped != nil {
		t.Fatalf("reminder row sent=%v skipped=%v err=%v", sentAt, skipped, err)
	}
	var letters int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_letters WHERE order_id = $1 AND kind = 'checkout_reminder'`, orderID).
		Scan(&letters); err != nil || letters != 1 {
		t.Fatalf("email_letters = %d, %v", letters, err)
	}
	// The same job run again while the order is still unpaid (a retry, or a
	// stale claim taken over) sends nothing: the reminder was taken before
	// the letter left.
	if err := h(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if sender.count() != 1 {
		t.Fatalf("a second run of the job sent %d reminders", sender.count())
	}

	// While unpaid, the link goes back to the provider's page.
	rec := growthGet(srv, "/v1/public/checkout/"+start.CheckoutToken+"/resume?src=reminder")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != start.RedirectURL {
		t.Fatalf("resume unpaid = %d %q, want 303 %q", rec.Code, rec.Header().Get("Location"), start.RedirectURL)
	}
	// Paid: back to the buyer's own page with the token.
	if _, err := pool.Exec(ctx, `UPDATE orders SET status = 'paid', paid_at = now() WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	rec = growthGet(srv, "/v1/public/checkout/"+start.CheckoutToken+"/resume")
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, hostedTicketsBaseURL+"/shows/growth") ||
		!strings.Contains(loc, "checkout_token="+start.CheckoutToken) {
		t.Fatalf("resume paid = %d %q", rec.Code, loc)
	}
	// A job that runs again after payment sends nothing more.
	before := sender.count()
	if err := h(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if sender.count() != before {
		t.Fatal("a second run sent another reminder")
	}
	// An unknown token never ends on a dead page.
	rec = growthGet(srv, "/v1/public/checkout/unknown-token-0000/resume")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), hostedTicketsBaseURL) {
		t.Fatalf("unknown token = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

type marketingOptInView struct {
	Enabled     bool   `json:"enabled"`
	TextVersion string `json:"text_version"`
	Discount    *struct {
		Type  string `json:"type"`
		Value int64  `json:"value"`
	} `json:"discount"`
}

var confirmTokenRe = regexp.MustCompile(`marketing/confirm\?token=([0-9a-f]{64})`)
var withdrawTokenRe = regexp.MustCompile(`marketing/withdraw\?token=([0-9A-Za-z_-]+)`)

func TestMarketingConsent_DiscountOnlyWithTheTick(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "mk")
	defer f.cleanup()
	defer growthCleanup(t, f)()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	reward := "THANKS" + strings.ToUpper(f.orgID.String()[:6])
	var codeID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO promo_codes (org_id, code, discount_type, discount_value, max_uses_per_customer, status)
		VALUES ($1, $2, 'percent', 10, 1, 'active') RETURNING id`, f.orgID, reward).Scan(&codeID); err != nil {
		t.Fatalf("promo code: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO org_growth_settings (org_id, cart_reminder_enabled, marketing_optin_enabled, consent_promo_code_id)
		VALUES ($1, true, true, $2)`, f.orgID, codeID); err != nil {
		t.Fatal(err)
	}

	// The feed session announces the tick and its discount.
	rec := growthGet(srv, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String())
	var feed struct {
		Event struct {
			Sessions []struct {
				MarketingOptIn marketingOptInView `json:"marketing_optin"`
			} `json:"sessions"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil || rec.Code != http.StatusOK || len(feed.Event.Sessions) == 0 {
		t.Fatalf("feed session = %d %v: %s", rec.Code, err, rec.Body.String())
	}
	optin := feed.Event.Sessions[0].MarketingOptIn
	if !optin.Enabled || optin.TextVersion != marketing.TextVersion || optin.Discount == nil || optin.Discount.Value != 10 {
		t.Fatalf("marketing_optin = %+v", optin)
	}
	tick := map[string]any{"accepted": true, "text_version": marketing.TextVersion}
	quote := func(consent map[string]any) (int, map[string]any) {
		b := map[string]any{"session_id": f.sessionID.String(),
			"ga_items": []map[string]any{{"tier_id": f.tierID.String(), "quantity": 2}}}
		if consent != nil {
			b["marketing_consent"] = consent
		}
		code, raw := growthPost(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", b)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return code, out
	}
	if code, out := quote(nil); code != http.StatusOK || out["marketing_discount_applied"] != false {
		t.Fatalf("quote without tick = %d %v", code, out)
	}
	if code, out := quote(tick); code != http.StatusOK || out["marketing_discount_applied"] != true {
		t.Fatalf("quote with tick = %d %v", code, out)
	}
	// An outdated text is refused rather than silently stored.
	if code, _ := quote(map[string]any{"accepted": true, "text_version": "old"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("outdated text = %d, want 422", code)
	}
	// The reward code typed without the tick is not applicable.
	if code, raw := f.startWith(t, srv, f.buyerMail, reward, nil); code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(raw), "promo.not_applicable") {
		t.Fatalf("typed reward without tick = %d %s", code, raw)
	}

	// With the tick: discounted order, pending subscription, one confirmation letter.
	code, raw := f.startWith(t, srv, f.buyerMail, "", tick)
	if code != http.StatusCreated || !strings.Contains(string(raw), `"marketing_discount_applied":true`) {
		t.Fatalf("start with tick = %d %s", code, raw)
	}
	var orderID uuid.UUID
	var subtotal, discount int64
	if err := pool.QueryRow(ctx, `SELECT o.id, cs.subtotal, COALESCE(cs.discount, 0) FROM orders o
		JOIN checkout_sessions cs ON cs.id = o.checkout_session_id WHERE o.org_id = $1`, f.orgID).
		Scan(&orderID, &subtotal, &discount); err != nil {
		t.Fatalf("order: %v", err)
	}
	if discount != subtotal/10 {
		t.Fatalf("discount = %d of %d, want 10 %%", discount, subtotal)
	}
	var subID uuid.UUID
	var status string
	if err := pool.QueryRow(ctx, `SELECT id, status FROM marketing_subscriptions WHERE org_id = $1`, f.orgID).
		Scan(&subID, &status); err != nil || status != marketing.StatusPending {
		t.Fatalf("subscription = %q, %v", status, err)
	}
	var grants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM marketing_consents WHERE subscription_id = $1 AND action = 'granted'`, subID).
		Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("granted journal rows = %d, %v", grants, err)
	}

	// A reward whose uses are all held by live unpaid orders is not offered:
	// the order transaction would refuse it, and a ticked buyer must never be
	// refused for a discount nobody can have. The first order above still
	// holds its places and its use of the code.
	if _, err := pool.Exec(ctx, `UPDATE promo_codes SET max_uses = 1 WHERE id = $1`, codeID); err != nil {
		t.Fatal(err)
	}
	// The fixture has five places; this second buyer needs two more.
	if _, err := gen.New(pool).InsertGAUnits(ctx, f.sessionID, "ga|t1", 5, &f.tierID, 2); err != nil {
		t.Fatalf("more places: %v", err)
	}
	if code, out := quote(tick); code != http.StatusOK || out["marketing_discount_applied"] != false {
		t.Fatalf("quote with the reward taken by a live order = %d %v", code, out)
	}
	if code, raw := f.startWith(t, srv, "second-"+f.buyerMail, "", tick); code != http.StatusCreated ||
		!strings.Contains(string(raw), `"marketing_discount_applied":false`) {
		t.Fatalf("start with the reward taken by a live order = %d %s", code, raw)
	}
	if _, err := pool.Exec(ctx, `UPDATE promo_codes SET max_uses = NULL WHERE id = $1`, codeID); err != nil {
		t.Fatal(err)
	}

	// The confirmation letter, then the link. It leaves from the
	// organization's verified sender, under its accent (2026-11-07).
	if _, err := pool.Exec(ctx, `UPDATE organizations SET sender_email = 'news@growth.test', sender_verification_status = 'verified',
		branding = COALESCE(branding, '{}'::jsonb) || '{"primary": "#822121"}'::jsonb WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	sender := &growthSender{}
	h := delivery.NewMarketingConfirmHandler(delivery.GrowthLetterOptions{Sender: sender, DB: pool, APIBaseURL: growthAPIBase})
	payload, _ := json.Marshal(marketing.JobPayload{SubscriptionID: subID.String()})
	if err := h(ctx, payload); err != nil {
		t.Fatalf("confirm letter: %v", err)
	}
	letter := sender.last(t)
	if letter.From != "news@growth.test" || letter.FromName == "" {
		t.Fatalf("confirmation From = %q %q, want the organization's verified sender and its name", letter.FromName, letter.From)
	}
	if !strings.Contains(letter.HTMLBody, "background:#822121") {
		t.Fatalf("confirmation letter lacks the organization's accent:\n%s", letter.HTMLBody)
	}
	// A consent ticked at checkout is described as one, not as a website subscription.
	if strings.Contains(letter.HTMLBody, "on the website") || strings.Contains(letter.HTMLBody, "на сайте на новости") {
		t.Fatalf("a checkout consent is described as a website subscription:\n%s", letter.HTMLBody)
	}
	m := confirmTokenRe.FindStringSubmatch(letter.HTMLBody)
	if m == nil {
		t.Fatalf("no confirmation link in:\n%s", letter.HTMLBody)
	}
	// Confirm: GET only asks (a mail scanner opens every link), POST confirms.
	rec = growthGet(srv, "/v1/public/marketing/confirm?token="+m[1])
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("confirm page = %d %s", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status); err != nil ||
		status != marketing.StatusPending {
		t.Fatalf("a GET confirmed the consent: %q, %v", status, err)
	}
	rec = growthPostForm(srv, "/v1/public/marketing/confirm?token="+m[1])
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "arena_marketing=confirmed") {
		t.Fatalf("confirm = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status); err != nil ||
		status != marketing.StatusConfirmed {
		t.Fatalf("after confirm status = %q, %v", status, err)
	}
	// A used link is not valid twice, neither its page nor its button.
	rec = growthGet(srv, "/v1/public/marketing/confirm?token="+m[1])
	if !strings.Contains(rec.Header().Get("Location"), "arena_marketing=invalid") {
		t.Fatalf("second confirm page = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = growthPostForm(srv, "/v1/public/marketing/confirm?token="+m[1])
	if !strings.Contains(rec.Header().Get("Location"), "arena_marketing=invalid") {
		t.Fatalf("second confirm = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The discount is once per buyer: after a paid order with the code the
	// tick is still taken but earns nothing.
	if _, err := pool.Exec(ctx, `UPDATE orders SET status = 'paid', paid_at = now() WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO promo_code_redemptions (promo_code_id, discount_amount, order_amount, order_id)
		VALUES ($1, $2, $3, $4)`, codeID, discount, subtotal, orderID); err != nil {
		t.Fatalf("redemption: %v", err)
	}
	code, raw = f.startWith(t, srv, f.buyerMail, "", tick)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "marketing.discount_used") {
		t.Fatalf("second discount = %d %s", code, raw)
	}
	code, raw = f.startWith(t, srv, f.buyerMail, "", map[string]any{
		"accepted": true, "text_version": marketing.TextVersion, "discount": false})
	if code != http.StatusCreated || !strings.Contains(string(raw), `"marketing_discount_applied":false`) {
		t.Fatalf("tick without discount = %d %s", code, raw)
	}

	// Withdraw: GET only asks, POST withdraws.
	w := withdrawTokenRe.FindStringSubmatch(letter.HTMLBody)
	if w == nil {
		t.Fatalf("no withdraw link in:\n%s", letter.HTMLBody)
	}
	rec = growthGet(srv, "/v1/public/marketing/withdraw?token="+w[1])
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("withdraw page = %d", rec.Code)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status); err != nil ||
		status != marketing.StatusConfirmed {
		t.Fatalf("a GET withdrew the consent: %q", status)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/public/marketing/withdraw?token="+w[1], strings.NewReader("--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	prec := httptest.NewRecorder()
	srv.router.ServeHTTP(prec, req)
	if prec.Code != http.StatusOK {
		t.Fatalf("withdraw POST = %d %s", prec.Code, prec.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status); err != nil ||
		status != marketing.StatusWithdrawn {
		t.Fatalf("after withdraw status = %q, %v", status, err)
	}
}

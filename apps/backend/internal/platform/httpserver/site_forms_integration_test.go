//go:build integration

package httpserver

// Proof of the static site's forms (spec 62 section 10.2, П-17 and П-10)
// through the real router and the real worker job: the contact form by feed
// token with a topic, a last name and a phone; the recipients picked by topic
// from the channel's settings; no address mailed twice on a retry; the
// Idempotency-Key replay; the scoped Turnstile expectation (action = form,
// hostnames = the channel's site origins); and the public subscription with
// double opt-in, confirmation and withdrawal.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/contactform"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsite"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

const (
	siteOrigin = "https://forms-site.example"
	siteMailA  = "owner-a@forms-site.example"
	siteMailB  = "owner-b@forms-site.example"
)

type siteFixture struct {
	f       *botInviteFixture
	channel uuid.UUID
	token   string
}

// newSiteFixture makes a channel that sells for a static site: its own feed
// token, the site origins and the topic recipients in its settings.
func newSiteFixture(t *testing.T, withOrigins bool) *siteFixture {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	ctx := context.Background()
	ch := uuid.New()
	settings := `{"contact_form":{"recipients":{"private_event":["` + siteMailA + `","` + siteMailB + `","not-an-address"]}}}`
	if withOrigins {
		settings = `{"site":{"origins":["` + siteOrigin + `","https://www.forms-site.example"]},` + settings[1:]
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'Site', $3::jsonb)`, ch, f.orgID, settings); err != nil {
		t.Fatal(err)
	}
	token := "ft_site_" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO agent_feed_tokens (sales_channel_id, token, label) VALUES ($1, $2, 'site')`, ch, token); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'message_id' IN (SELECT id::text FROM organizer_contact_messages WHERE org_id = $2)`, contactform.JobType, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'subscription_id' IN (SELECT id::text FROM marketing_subscriptions WHERE org_id = $2)`, marketing.JobTypeConfirmEmail, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM organizer_contact_messages WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM marketing_subscriptions WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM idempotency_keys WHERE scope LIKE $1`, "%:"+token)
		_, _ = pool.Exec(c, `DELETE FROM agent_feed_tokens WHERE token = $1`, token)
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE org_id = $1`, f.orgID)
	})
	return &siteFixture{f: f, channel: ch, token: token}
}

func sitePost(t *testing.T, h http.Handler, path, body, ip, idemKey string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	if ip != "" {
		req.RemoteAddr = ip + ":4000"
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func siteContactBody(topic, email string) string {
	b, _ := json.Marshal(map[string]any{
		"topic": topic, "name": "Anna", "last_name": "Cohen", "email": email, "phone": "+972 50 123 4567",
		"message": "We would like to book the hall for 30 guests.", "consent": true, "locale": "he",
	})
	return string(b)
}

// scriptedMail fails for the addresses in failFor and records the rest.
type scriptedMail struct {
	sent    []emailadapter.Message
	failFor map[string]bool
}

func (m *scriptedMail) Send(_ context.Context, msg emailadapter.Message) error {
	if m.failFor[msg.To] {
		return errors.New("provider refused")
	}
	m.sent = append(m.sent, msg)
	return nil
}

func TestSiteContact_TopicRecipientsAndRetryNeverRepeatAnAddress(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	sf := newSiteFixture(t, true)
	ctx := context.Background()
	path := "/v1/public/feeds/" + sf.token + "/organizer-contact"

	code, body := sitePost(t, srv.router, path, siteContactBody("private_event", "Anna@Example.com"), "198.51.100.61", "")
	if code != http.StatusAccepted || strings.Contains(body, "@") {
		t.Fatalf("send: %d %s", code, body)
	}
	var (
		id                          uuid.UUID
		topic, last, phone, page, e string
	)
	if err := pool.QueryRow(ctx, `SELECT id, topic, last_name, phone, page_slug, sender_email FROM organizer_contact_messages WHERE org_id = $1`, sf.f.orgID).
		Scan(&id, &topic, &last, &phone, &page, &e); err != nil {
		t.Fatal(err)
	}
	if topic != "private_event" || last != "Cohen" || phone != "+972 50 123 4567" || e != "anna@example.com" {
		t.Fatalf("stored: %s %s %s %s", topic, last, phone, e)
	}
	payload, _ := json.Marshal(contactform.Payload{MessageID: id.String()})

	// The second address fails: the first is mailed and remembered, the job errors.
	mail := &scriptedMail{failFor: map[string]bool{siteMailB: true}}
	h := contactform.New(contactform.Options{Pool: pool, Sender: mail})
	if err := h.Handle(ctx, payload); err == nil {
		t.Fatal("a failed address must fail the job so the worker retries it")
	}
	if len(mail.sent) != 1 || mail.sent[0].To != siteMailA || mail.sent[0].ReplyTo != "anna@example.com" {
		t.Fatalf("first attempt mails = %+v", mail.sent)
	}
	if !strings.Contains(mail.sent[0].TextBody, "Anna Cohen") || !strings.Contains(mail.sent[0].TextBody, "+972 50 123 4567") ||
		!strings.Contains(mail.sent[0].TextBody, "private_event") {
		t.Errorf("the letter lacks the sender's full name, phone or topic:\n%s", mail.sent[0].TextBody)
	}
	var lastErr string
	var sentAt *string
	if err := pool.QueryRow(ctx, `SELECT email_last_error, email_sent_at::text FROM organizer_contact_messages WHERE id = $1`, id).Scan(&lastErr, &sentAt); err != nil {
		t.Fatal(err)
	}
	if lastErr == "" || sentAt != nil || strings.Contains(lastErr, "@") {
		t.Errorf("failure record: error=%q sent_at=%v", lastErr, sentAt)
	}
	// The provider recovers: only the missing address is mailed, and then none.
	mail.failFor = nil
	if err := h.Handle(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 2 || mail.sent[1].To != siteMailB {
		t.Fatalf("retry mails = %+v", mail.sent)
	}
	var mailed bool
	if err := pool.QueryRow(ctx, `SELECT email_mailed AND email_last_error = '' FROM organizer_contact_messages WHERE id = $1`, id).Scan(&mailed); err != nil || !mailed {
		t.Errorf("after the retry: mailed=%v err=%v", mailed, err)
	}
}

func TestSiteContact_RefusalsAndIdempotency(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	sf := newSiteFixture(t, true)
	noSite := newSiteFixture(t, false)
	ctx := context.Background()
	path := "/v1/public/feeds/" + sf.token + "/organizer-contact"

	for name, b := range map[string]string{
		"unknown topic":        strings.Replace(siteContactBody("contact", "a@b.example"), `"topic":"contact"`, `"topic":"sales"`, 1),
		"no consent":           strings.Replace(siteContactBody("contact", "a@b.example"), `"consent":true`, `"consent":false`, 1),
		"bad phone":            strings.Replace(siteContactBody("contact", "a@b.example"), "+972 50 123 4567", "call me", 1),
		"private needs a text": strings.Replace(siteContactBody("private_event", "a@b.example"), "We would like to book the hall for 30 guests.", "", 1),
		"unknown key":          `{"name":"A","email":"a@b.example","consent":true,"x":1}`,
	} {
		if code, body := sitePost(t, srv.router, path, b, "198.51.100.62", ""); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// A channel without site origins, and an unknown token, have no form.
	for name, p := range map[string]string{
		"no origins":    "/v1/public/feeds/" + noSite.token + "/organizer-contact",
		"unknown token": "/v1/public/feeds/ft_nope_" + uuid.NewString()[:6] + "/organizer-contact",
	} {
		if code, body := sitePost(t, srv.router, p, siteContactBody("contact", "a@b.example"), "198.51.100.63", ""); code != http.StatusNotFound {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// The topic "contact" has no mapped recipients and the organization has an
	// owner, so it falls back and is accepted. The message is optional there.
	b, _ := json.Marshal(map[string]any{"name": "Ben", "email": "ben@example.com", "consent": true})
	if code, body := sitePost(t, srv.router, path, string(b), "198.51.100.64", ""); code != http.StatusAccepted {
		t.Errorf("contact without a message: %d %s", code, body)
	}

	// The same Idempotency-Key and body: one message. Another body: 409.
	body := siteContactBody("private_event", "idem@example.com")
	key := "site-form-" + uuid.NewString()
	for i := 0; i < 3; i++ {
		if code, out := sitePost(t, srv.router, path, body, "198.51.100.65", key); code != http.StatusAccepted {
			t.Fatalf("keyed send %d: %d %s", i, code, out)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizer_contact_messages WHERE org_id = $1 AND sender_email = 'idem@example.com'`, sf.f.orgID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("keyed retries stored %d messages (%v), want 1", n, err)
	}
	if code, _ := sitePost(t, srv.router, path, siteContactBody("private_event", "other@example.com"), "198.51.100.65", key); code != http.StatusConflict {
		t.Errorf("same key, another body: %d", code)
	}
}

// expectCapture is a scoped verifier that records what the handler demands.
type expectCapture struct {
	got honboarding.Expect
	ok  bool
}

func (c *expectCapture) Verify(context.Context, string, string) (bool, error) { return c.ok, nil }
func (c *expectCapture) VerifyScoped(_ context.Context, _, _ string, want honboarding.Expect) (bool, error) {
	c.got = want
	return c.ok, nil
}

func TestSiteForms_TurnstileIsScopedToTheFormAndTheSiteHost(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	sf := newSiteFixture(t, true)
	ctx := context.Background()
	capt := &expectCapture{ok: false}
	r := chi.NewRouter()
	g := hsite.Guard{Verifier: capt}
	r.Post("/v1/public/feeds/{feed_token}/organizer-contact", (&hsite.ContactHandler{Pool: pool, Guard: g, IPSalt: "s"}).HandleSend)
	r.Post("/v1/public/feeds/{feed_token}/marketing-subscriptions", (&hsite.SubscribeHandler{Pool: pool, Guard: g}).HandleSubscribe)

	wantHosts := []string{"forms-site.example", "www.forms-site.example"}
	code, body := sitePost(t, r, "/v1/public/feeds/"+sf.token+"/organizer-contact", siteContactBody("private_event", "a@b.example"), "198.51.100.66", "")
	if code != http.StatusForbidden || !strings.Contains(body, "contact.captcha_failed") {
		t.Errorf("failed captcha: %d %s", code, body)
	}
	if capt.got.Action != "private_event" || strings.Join(capt.got.Hostnames, ",") != strings.Join(wantHosts, ",") {
		t.Errorf("contact expectation = %+v", capt.got)
	}
	sub := `{"email":"club@example.com","consent":true,"text_version":"club-2026-11-06"}`
	if code, body := sitePost(t, r, "/v1/public/feeds/"+sf.token+"/marketing-subscriptions", sub, "198.51.100.66", ""); code != http.StatusForbidden || !strings.Contains(body, "subscription.captcha_failed") {
		t.Errorf("failed subscription captcha: %d %s", code, body)
	}
	if capt.got.Action != "club" {
		t.Errorf("subscription action = %q", capt.got.Action)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM organizer_contact_messages WHERE org_id = $1) + (SELECT count(*) FROM marketing_subscriptions WHERE org_id = $1)`, sf.f.orgID).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused captcha stored %d rows", n)
	}
	// Outage and missing secret are 503, never 502.
	for _, v := range []honboarding.Verifier{fakeVerifier{err: errors.New("cloudflare down")}, &honboarding.TurnstileVerifier{Required: true}} {
		r2 := chi.NewRouter()
		r2.Post("/v1/public/feeds/{feed_token}/organizer-contact", (&hsite.ContactHandler{Pool: pool, Guard: hsite.Guard{Verifier: v}}).HandleSend)
		if code, _ := sitePost(t, r2, "/v1/public/feeds/"+sf.token+"/organizer-contact", siteContactBody("contact", "a@b.example"), "198.51.100.66", ""); code != http.StatusServiceUnavailable {
			t.Errorf("verifier %T: %d", v, code)
		}
	}
}

func TestSiteSubscription_DoubleOptInConfirmAndWithdraw(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	sf := newSiteFixture(t, true)
	ctx := context.Background()
	path := "/v1/public/feeds/" + sf.token + "/marketing-subscriptions"
	body := func(email, extra string) string {
		return `{"email":"` + email + `","name":"Anna","phone":"+972501234567","locale":"he","consent":true,"text_version":"club-2026-11-06",` +
			`"page_url":"` + siteOrigin + `/club/"` + extra + `}`
	}

	for name, b := range map[string]string{
		"no consent":   strings.Replace(body("a@b.example", ""), `"consent":true`, `"consent":false`, 1),
		"bad e-mail":   body("nope", ""),
		"no version":   strings.Replace(body("a@b.example", ""), `"text_version":"club-2026-11-06",`, ``, 1),
		"bad version":  strings.Replace(body("a@b.example", ""), "club-2026-11-06", "bad version!", 1),
		"unknown key":  body("a@b.example", `,"x":1`),
		"bad phone":    strings.Replace(body("a@b.example", ""), "+972501234567", "abc", 1),
		"name too big": strings.Replace(body("a@b.example", ""), `"Anna"`, `"`+strings.Repeat("x", 101)+`"`, 1),
	} {
		if code, out := sitePost(t, srv.router, path, b, "198.51.100.71", ""); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	if code, _ := sitePost(t, srv.router, "/v1/public/feeds/ft_nope_"+uuid.NewString()[:6]+"/marketing-subscriptions", body("a@b.example", ""), "198.51.100.71", ""); code != http.StatusNotFound {
		t.Errorf("unknown token: %d", code)
	}

	code, out := sitePost(t, srv.router, path, body("Club@Example.com", ""), "198.51.100.72", "")
	if code != http.StatusAccepted || !strings.Contains(out, "check_email") || strings.Contains(out, "@") {
		t.Fatalf("subscribe: %d %s", code, out)
	}
	var (
		subID                      uuid.UUID
		status, source, version, p string
		phone, withdrawToken       string
	)
	if err := pool.QueryRow(ctx, `SELECT id, status, source, text_version, page_url, phone, unsubscribe_token FROM marketing_subscriptions WHERE org_id = $1 AND email_normalized = 'club@example.com'`, sf.f.orgID).
		Scan(&subID, &status, &source, &version, &p, &phone, &withdrawToken); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || source != "site" || version != "club-2026-11-06" || p != siteOrigin+"/club/" || phone != "+972501234567" {
		t.Fatalf("stored: %s %s %s %s %s", status, source, version, p, phone)
	}
	jobs := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'subscription_id' = $2`, marketing.JobTypeConfirmEmail, subID.String()).Scan(&n)
		return n
	}
	if jobs() != 1 {
		t.Fatalf("confirmation letters queued = %d", jobs())
	}
	// Asking again reveals nothing and queues no second letter inside 10 minutes.
	if code, _ := sitePost(t, srv.router, path, body("club@example.com", ""), "198.51.100.72", ""); code != http.StatusAccepted || jobs() != 1 {
		t.Errorf("repeat: %d, letters %d", code, jobs())
	}
	// A page outside the site's origins is dropped.
	if code, _ := sitePost(t, srv.router, path, strings.Replace(body("other@example.com", ""), siteOrigin, "https://evil.example", 1), "198.51.100.73", ""); code != http.StatusAccepted {
		t.Fatal("subscribe with a foreign page")
	}
	var foreign string
	_ = pool.QueryRow(ctx, `SELECT page_url FROM marketing_subscriptions WHERE org_id = $1 AND email_normalized = 'other@example.com'`, sf.f.orgID).Scan(&foreign)
	if foreign != "" {
		t.Errorf("a page outside the site origins was kept: %q", foreign)
	}

	// Each repeat is journaled as a fresh grant (evidence); only one letter is queued.
	// The worker mints the confirmation token at send time; stand in for it.
	tok, _ := marketing.NewToken()
	if _, err := pool.Exec(ctx, `UPDATE marketing_subscriptions SET confirm_token_hash = $2, confirm_sent_at = now() WHERE id = $1`, subID, marketing.HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	// A scanner's GET confirms nothing; the button (POST) does and returns to the site.
	req := httptest.NewRequest(http.MethodGet, "/v1/public/marketing/confirm?token="+tok, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	_ = pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status)
	if status != "pending" {
		t.Fatalf("a GET confirmed the address (%d)", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/public/marketing/confirm?token="+tok, nil)
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), siteOrigin+"/club/") {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var journal []string
	rows, err := pool.Query(ctx, `SELECT action || ':' || source || ':' || text_version FROM marketing_consents WHERE subscription_id = $1 ORDER BY created_at, id`, subID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		journal = append(journal, s)
	}
	rows.Close()
	if strings.Join(journal, "|") != "granted:site:club-2026-11-06|granted:site:club-2026-11-06|confirmed:confirm_link:club-2026-11-06" {
		t.Errorf("journal = %v", journal)
	}

	// Unsubscribe: the withdraw page asks, the button withdraws.
	req = httptest.NewRequest(http.MethodPost, "/v1/public/marketing/withdraw?token="+withdrawToken, nil)
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	_ = pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status)
	if status != "withdrawn" {
		t.Fatalf("withdraw: %d, status %s", rec.Code, status)
	}
	// Subscribing again after a withdrawal asks to confirm again.
	if _, err := pool.Exec(ctx, `UPDATE marketing_subscriptions SET confirm_sent_at = now() - interval '1 hour' WHERE id = $1`, subID); err != nil {
		t.Fatal(err)
	}
	if code, _ := sitePost(t, srv.router, path, body("club@example.com", ""), "198.51.100.74", ""); code != http.StatusAccepted {
		t.Fatal("resubscribe")
	}
	_ = pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE id = $1`, subID).Scan(&status)
	if status != "pending" || jobs() != 2 {
		t.Errorf("after resubscribing: status %s, letters %d", status, jobs())
	}
}

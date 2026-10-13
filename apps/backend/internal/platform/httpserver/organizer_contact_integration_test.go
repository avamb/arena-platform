//go:build integration

package httpserver

// Proof of the hosted page's "Write to the organizer" form (HP contact form)
// through the real router and the real worker job: the page offers the form
// without ever answering the address, a good message is stored and queued,
// malformed ones are refused, a foreign feed token and a page without a
// public e-mail answer 404, the per-IP and per-page limits hold, a failed or
// missing captcha is refused before anything is written, and the job mails
// the organizer with the buyer as Reply-To, posts to the organization's chat
// only while the switch is on, and never sends a leg twice.

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcontact"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
)

const contactOrgEmail = "tickets-hpform@organizer.example"

type contactFixture struct {
	f       *botInviteFixture
	slug    string
	channel uuid.UUID
	token   string
}

func newContactFixture(t *testing.T, srv *Server, withEmail bool) *contactFixture {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	hosted, _ := sellerChannelFixture(t, f, "HPform")
	ctx := context.Background()
	if withEmail {
		if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = jsonb_set(settings, '{hosted_page,public_contact}', jsonb_build_object('email', $2::text)) WHERE id = $1`, hosted, contactOrgEmail); err != nil {
			t.Fatal(err)
		}
	}
	token := "ft_hpform_" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO agent_feed_tokens (sales_channel_id, token, label) VALUES ($1, $2, 'hpform')`, hosted, token); err != nil {
		t.Fatal(err)
	}
	var slug string
	if err := pool.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, f.orgID).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'message_id' IN (SELECT id::text FROM organizer_contact_messages WHERE org_id = $2)`, contactform.JobType, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM organizer_contact_messages WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM agent_feed_tokens WHERE token = $1`, token)
	})
	return &contactFixture{f: f, slug: slug, channel: hosted, token: token}
}

func contactPost(t *testing.T, h http.Handler, slug, body, ip string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/organizer-contact/"+slug, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.RemoteAddr = ip + ":4000"
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func contactBody(email, token string) string {
	b, _ := json.Marshal(map[string]any{
		"name": "Anna Buyer", "email": email, "message": "Is the venue accessible by wheelchair?",
		"consent": true, "feed_token": token, "locale": "es",
	})
	return string(b)
}

func TestOrganizerContact_RouteStoresQueuesAndRefuses(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	cf := newContactFixture(t, srv, true)
	other := newContactFixture(t, srv, true)
	noMail := newContactFixture(t, srv, false)
	orgOnly := newContactFixture(t, srv, false)
	ownerOnly := newContactFixture(t, srv, false)
	ctx := context.Background()
	// noMail's messages would reach nobody (no address, no active owner);
	// orgOnly's organization has its own address and ownerOnly only its
	// owner's account e-mail, which the page must never show.
	const orgPrivate = "box-private@organizer.example"
	for _, org := range []uuid.UUID{noMail.f.orgID, ownerOnly.f.orgID} {
		if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = NULL WHERE id = $1`, org); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE memberships SET status = 'suspended' WHERE org_id = $1 AND role = 'org_admin'`, noMail.f.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = $2 WHERE id = $1`, orgOnly.f.orgID, orgPrivate); err != nil {
		t.Fatal(err)
	}

	// ── the promoter page offers the form and never answers the address ──────
	req := httptest.NewRequest(http.MethodGet, "/v1/public/pages/"+cf.slug, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"contact_form":{"available":true`) {
		t.Errorf("page: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), contactOrgEmail) {
		t.Fatalf("the page answers the organizer's address: %s", rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/public/pages/"+noMail.slug, nil)
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "contact_form") {
		t.Errorf("a page whose messages would reach nobody must not offer the form: %s", rec.Body.String())
	}
	// Without a public e-mail the organization's own address takes the
	// messages: the form is offered (ticket letters link it), the address is
	// never answered.
	req = httptest.NewRequest(http.MethodGet, "/v1/public/pages/"+orgOnly.slug, nil)
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"contact_form":{"available":true`) || strings.Contains(rec.Body.String(), orgPrivate) {
		t.Errorf("organization address only: %d %s", rec.Code, rec.Body.String())
	}
	// Owner decision 2026-10-13: with no address at all the owner's account
	// e-mail takes the messages; the page offers the form and never shows it.
	req = httptest.NewRequest(http.MethodGet, "/v1/public/pages/"+ownerOnly.slug, nil)
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"contact_form":{"available":true`) || strings.Contains(rec.Body.String(), ownerOnly.f.emails[0]) {
		t.Errorf("owner's account e-mail only: %d %s", rec.Code, rec.Body.String())
	}
	if code, body := contactPost(t, srv.router, orgOnly.slug, contactBody("ben@example.com", orgOnly.token), "198.51.100.17"); code != http.StatusAccepted || strings.Contains(body, orgPrivate) {
		t.Errorf("send to a page without a public e-mail: %d %s", code, body)
	}

	// ── a good message: 202, no address, stored and queued ────────────────────
	code, body := contactPost(t, srv.router, cf.slug, contactBody("Anna@Example.com", cf.token), "198.51.100.7")
	if code != http.StatusAccepted || strings.Contains(body, "@") {
		t.Fatalf("send: %d %s", code, body)
	}
	var id uuid.UUID
	var email, locale string
	if err := pool.QueryRow(ctx, `SELECT id, sender_email, locale FROM organizer_contact_messages WHERE org_id = $1`, cf.f.orgID).Scan(&id, &email, &locale); err != nil || email != "anna@example.com" || locale != "es" {
		t.Fatalf("stored: %v %s %s", err, email, locale)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'message_id' = $2`, contactform.JobType, id.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("queued jobs = %d (%v)", jobs, err)
	}

	// ── malformed messages change nothing ─────────────────────────────────────
	for name, b := range map[string]string{
		"no consent":   strings.Replace(contactBody("a@b.example", cf.token), `"consent":true`, `"consent":false`, 1),
		"bad e-mail":   contactBody("not-an-email", cf.token),
		"short":        strings.Replace(contactBody("a@b.example", cf.token), "Is the venue accessible by wheelchair?", "hi", 1),
		"unknown key":  `{"name":"A","email":"a@b.example","message":"0123456789","consent":true,"x":1}`,
		"no name":      strings.Replace(contactBody("a@b.example", cf.token), "Anna Buyer", " ", 1),
		"html in name": strings.Replace(contactBody("a@b.example", cf.token), "Anna Buyer", strings.Repeat("x", 101), 1),
	} {
		if code, body := contactPost(t, srv.router, cf.slug, b, "198.51.100.8"); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, _ := contactPost(t, srv.router, cf.slug, `{"message":"`+strings.Repeat("x", 17000)+`"}`, "198.51.100.8"); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: %d", code)
	}
	// Another organization's feed token, an unknown slug, a page without an e-mail.
	for name, c := range map[string][2]string{
		"foreign token": {cf.slug, other.token},
		"unknown slug":  {"no-such-page-" + uuid.NewString()[:6], ""},
		"no e-mail":     {noMail.slug, ""},
	} {
		if code, body := contactPost(t, srv.router, c[0], contactBody("a@b.example", c[1]), "198.51.100.9"); code != http.StatusNotFound || strings.Contains(body, contactOrgEmail) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM organizer_contact_messages WHERE org_id = ANY($1)`, []uuid.UUID{cf.f.orgID, other.f.orgID, noMail.f.orgID}).Scan(&n)
	if n != 1 {
		t.Fatalf("refused messages were stored: %d rows", n)
	}

	// ── per IP: the 6th message of an hour is refused ─────────────────────────
	for i := 0; i < hcontact.PerIPPerHour-1; i++ {
		if code, body := contactPost(t, srv.router, cf.slug, contactBody("a@b.example", cf.token), "198.51.100.7"); code != http.StatusAccepted {
			t.Fatalf("message %d: %d %s", i+2, code, body)
		}
	}
	if code, body := contactPost(t, srv.router, cf.slug, contactBody("a@b.example", cf.token), "198.51.100.7"); code != http.StatusTooManyRequests || !strings.Contains(body, "contact.rate_limited") {
		t.Errorf("6th from one IP: %d %s", code, body)
	}
	// ── per page: 30 an hour, whatever the IP ─────────────────────────────────
	if _, err := pool.Exec(ctx, `
		INSERT INTO organizer_contact_messages (org_id, channel_id, page_slug, sender_name, sender_email, message, ip_hash)
		SELECT $1, $2, 'x', 'n', 'a@b.example', '0123456789', 'filler-' || g FROM generate_series(1, $3::int) g`,
		cf.f.orgID, cf.channel, hcontact.PerChannelPerHour); err != nil {
		t.Fatal(err)
	}
	if code, body := contactPost(t, srv.router, cf.slug, contactBody("a@b.example", cf.token), "203.0.113.50"); code != http.StatusTooManyRequests {
		t.Errorf("page over its hourly limit: %d %s", code, body)
	}
}

type fakeVerifier struct {
	ok  bool
	err error
}

func (v fakeVerifier) Verify(context.Context, string, string) (bool, error) { return v.ok, v.err }

func TestOrganizerContact_CaptchaIsCheckedBeforeAnythingIsWritten(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	cf := newContactFixture(t, srv, true)
	route := func(v honboarding.Verifier) http.Handler {
		r := chi.NewRouter()
		r.Post("/v1/public/organizer-contact/{org_slug}", hcontact.New(pool, v, "salt", 0, nil).HandleSend)
		return r
	}
	if code, body := contactPost(t, route(fakeVerifier{ok: false}), cf.slug, contactBody("a@b.example", cf.token), "198.51.100.20"); code != http.StatusForbidden || !strings.Contains(body, "contact.captcha_failed") {
		t.Errorf("failed captcha: %d %s", code, body)
	}
	// Production without a secret: the real verifier refuses with 503.
	prod := &honboarding.TurnstileVerifier{Secret: "", Required: true}
	if code, body := contactPost(t, route(prod), cf.slug, contactBody("a@b.example", cf.token), "198.51.100.20"); code != http.StatusServiceUnavailable || !strings.Contains(body, "contact.captcha_not_configured") {
		t.Errorf("no secret in production: %d %s", code, body)
	}
	if code, _ := contactPost(t, route(fakeVerifier{err: errors.New("cloudflare down")}), cf.slug, contactBody("a@b.example", cf.token), "198.51.100.20"); code != http.StatusServiceUnavailable {
		t.Errorf("verifier outage: %d", code)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM organizer_contact_messages WHERE org_id = $1`, cf.f.orgID).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused captcha stored %d messages", n)
	}
	if code, _ := contactPost(t, route(fakeVerifier{ok: true}), cf.slug, contactBody("a@b.example", cf.token), "198.51.100.20"); code != http.StatusAccepted {
		t.Errorf("passed captcha: %d", code)
	}
}

// captureMail records each letter; with fail set, the provider refuses it.
type captureMail struct {
	sent []emailadapter.Message
	fail bool
}

func (c *captureMail) Send(_ context.Context, m emailadapter.Message) error {
	if c.fail {
		return errors.New("provider down")
	}
	c.sent = append(c.sent, m)
	return nil
}

// captureOps records operator alerts.
type captureOps struct{ texts []string }

func (c *captureOps) Send(_ context.Context, text string) error {
	c.texts = append(c.texts, text)
	return nil
}

// captureAnnounce records each post; with fail set, the organization's one
// chat refuses it (chats 1, delivered 0).
type captureAnnounce struct {
	orgs, texts []string
	fail        bool
}

func (c *captureAnnounce) AnnounceToOrg(_ context.Context, orgID, text string) (int, int, error) {
	if c.fail {
		return 1, 0, nil
	}
	c.orgs = append(c.orgs, orgID)
	c.texts = append(c.texts, text)
	return 1, 1, nil
}

func TestOrganizerContact_WorkerMailsAndPostsOnce(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	cf := newContactFixture(t, srv, true)
	ctx := context.Background()

	send := func(ip string) uuid.UUID {
		t.Helper()
		if code, body := contactPost(t, srv.router, cf.slug, contactBody("anna@example.com", cf.token), ip); code != http.StatusAccepted {
			t.Fatalf("send: %d %s", code, body)
		}
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM organizer_contact_messages WHERE org_id = $1 ORDER BY created_at DESC LIMIT 1`, cf.f.orgID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mail, chat, ops := &captureMail{}, &captureAnnounce{}, &captureOps{}
	h := contactform.New(contactform.Options{Pool: pool, Sender: mail, Announcer: chat, Ops: ops})
	payload := func(id uuid.UUID) []byte {
		b, _ := json.Marshal(contactform.Payload{MessageID: id.String()})
		return b
	}

	// ── switch on (the default): an e-mail and a chat post, each once ─────────
	id := send("198.51.100.30")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(ctx, payload(id)); err != nil { // a retried job sends nothing again
		t.Fatal(err)
	}
	if len(mail.sent) != 1 || mail.sent[0].To != contactOrgEmail || mail.sent[0].ReplyTo != "anna@example.com" || !strings.Contains(mail.sent[0].TextBody, "wheelchair") {
		t.Fatalf("mails = %+v", mail.sent)
	}
	if len(chat.orgs) != 1 || chat.orgs[0] != cf.f.orgID.String() || !strings.Contains(chat.texts[0], "Message from a buyer") {
		t.Fatalf("chat posts = %+v", chat)
	}

	// ── switch off through the owner's API: the e-mail only ───────────────────
	call := sellerCaller(t, srv)
	if code, raw := call(cf.f.ownerID, http.MethodPatch, "/v1/organizations/"+cf.f.orgID.String()+"/seller-profile", `{"contact_form_to_telegram": false}`); code != http.StatusOK || !strings.Contains(string(raw), `"contact_form_to_telegram":false`) {
		t.Fatalf("switch off: %d %s", code, raw)
	}
	id = send("198.51.100.31")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 2 || len(chat.orgs) != 1 {
		t.Fatalf("with the switch off: mails=%d posts=%d", len(mail.sent), len(chat.orgs))
	}
	var tgDone bool
	if err := pool.QueryRow(ctx, `SELECT telegram_sent_at IS NOT NULL FROM organizer_contact_messages WHERE id = $1`, id).Scan(&tgDone); err != nil || !tgDone {
		t.Errorf("the Telegram leg must be marked done even when skipped: %v %v", tgDone, err)
	}
	// The switch is the owner's: a manager cannot change it.
	if code, _ := call(uuid.New(), http.MethodPatch, "/v1/organizations/"+cf.f.orgID.String()+"/seller-profile", `{"contact_form_to_telegram": true}`); code != http.StatusForbidden {
		t.Errorf("a stranger switched it: %d", code)
	}

	// ── a chat that refuses the post: not marked, the retry posts it once ─────
	if code, raw := call(cf.f.ownerID, http.MethodPatch, "/v1/organizations/"+cf.f.orgID.String()+"/seller-profile", `{"contact_form_to_telegram": true}`); code != http.StatusOK {
		t.Fatalf("switch on: %d %s", code, raw)
	}
	chat.fail = true
	id = send("198.51.100.32")
	if err := h.Handle(ctx, payload(id)); err == nil {
		t.Fatal("a post no chat took must fail the job so the worker retries")
	}
	if err := pool.QueryRow(ctx, `SELECT telegram_sent_at IS NOT NULL FROM organizer_contact_messages WHERE id = $1`, id).Scan(&tgDone); err != nil || tgDone {
		t.Errorf("an undelivered post must not be marked sent: %v %v", tgDone, err)
	}
	chat.fail = false
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 3 || len(chat.texts) != 2 || !strings.Contains(chat.texts[1], "went to your e-mail") {
		t.Fatalf("after the retry: mails=%d posts=%v", len(mail.sent), chat.texts)
	}

	// ── no usable public e-mail: the organization's own address takes it ─────
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = jsonb_set(settings, '{hosted_page,public_contact}', '{"email":"not an address"}') WHERE id = $1`, cf.channel); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = 'box-private@organizer.example' WHERE id = $1`, cf.f.orgID); err != nil {
		t.Fatal(err)
	}
	id = send("198.51.100.34")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 4 || mail.sent[3].To != "box-private@organizer.example" || mail.sent[3].ReplyTo != "anna@example.com" {
		t.Fatalf("fallback to the organization's address: %+v", mail.sent[len(mail.sent)-1])
	}

	// ── no organization address: the owner's account e-mail takes it ─────────
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = NULL WHERE id = $1`, cf.f.orgID); err != nil {
		t.Fatal(err)
	}
	id = send("198.51.100.35")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 5 || !strings.EqualFold(mail.sent[4].To, cf.f.emails[0]) || mail.sent[4].ReplyTo != "anna@example.com" {
		t.Fatalf("fallback to the owner's account e-mail: %+v", mail.sent[len(mail.sent)-1])
	}

	// ── a platform superadmin is never the owner who receives it ─────────────
	// The operator is an org_admin of organizations it set up; the message
	// must reach the organizer, the next owner, not the platform.
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
	        SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, cf.f.ownerID); err != nil {
		t.Fatal(err)
	}
	second := "second-owner-" + uuid.NewString()[:8] + "@organizer.example"
	cf.f.emails = append(cf.f.emails, second)
	var secondID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, second).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'org_admin')`, secondID, cf.f.orgID); err != nil {
		t.Fatal(err)
	}
	id = send("198.51.100.36")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 6 || mail.sent[5].To != second {
		t.Fatalf("the superadmin was picked over the organizer: %+v", mail.sent[len(mail.sent)-1])
	}

	// ── no address and no owner but the superadmin: nothing mailed, said so ──
	if _, err := pool.Exec(ctx, `UPDATE memberships SET status = 'suspended' WHERE user_id = $1 AND org_id = $2`, secondID, cf.f.orgID); err != nil {
		t.Fatal(err)
	}
	id = send("198.51.100.33")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	var mailed bool
	if err := pool.QueryRow(ctx, `SELECT email_mailed FROM organizer_contact_messages WHERE id = $1`, id).Scan(&mailed); err != nil || mailed {
		t.Errorf("email_mailed = %v %v, want false", mailed, err)
	}
	if len(mail.sent) != 6 {
		t.Errorf("a message was mailed with no recipient: %+v", mail.sent[len(mail.sent)-1])
	}
	if last := chat.texts[len(chat.texts)-1]; strings.Contains(last, "went to your e-mail") || !strings.Contains(last, "was not e-mailed") {
		t.Errorf("the post claims an e-mail that did not go out: %s", last)
	}
	if len(ops.texts) != 0 {
		t.Errorf("the chat took the message, yet the operator was alerted: %v", ops.texts)
	}

	// ── neither an address nor a chat: the operator hears, without the words ─
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_form_to_telegram = false WHERE id = $1`, cf.f.orgID); err != nil {
		t.Fatal(err)
	}
	id = send("198.51.100.37")
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(ops.texts) != 1 || !strings.Contains(ops.texts[0], id.String()) ||
		strings.Contains(ops.texts[0], "anna@example.com") || strings.Contains(ops.texts[0], "wheelchair") {
		t.Fatalf("message to nobody: ops alerts = %v", ops.texts)
	}

	// ── the e-mail provider fails: the chat copy still goes, once ────────────
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_form_to_telegram = true, contact_email = 'box-private@organizer.example' WHERE id = $1`, cf.f.orgID); err != nil {
		t.Fatal(err)
	}
	mail.fail = true
	posts := len(chat.texts)
	id = send("198.51.100.38")
	if err := h.Handle(ctx, payload(id)); err == nil {
		t.Fatal("a failed e-mail must fail the job so the worker retries it")
	}
	if len(chat.texts) != posts+1 || !strings.Contains(chat.texts[posts], "keeps trying") {
		t.Fatalf("the chat copy must go while the e-mail is retried: %v", chat.texts[posts:])
	}
	var emailDone, tgSent bool
	if err := pool.QueryRow(ctx, `SELECT email_sent_at IS NOT NULL, telegram_sent_at IS NOT NULL FROM organizer_contact_messages WHERE id = $1`, id).Scan(&emailDone, &tgSent); err != nil || emailDone || !tgSent {
		t.Errorf("after a failed e-mail: email done=%v telegram done=%v %v", emailDone, tgSent, err)
	}
	mail.fail = false
	if err := h.Handle(ctx, payload(id)); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 7 || mail.sent[6].To != "box-private@organizer.example" || len(chat.texts) != posts+1 {
		t.Errorf("the retry mails once and does not post again: mails=%d posts=%d", len(mail.sent), len(chat.texts)-posts)
	}
}

// The 90-day purge runs on its own schedule, not only when a new message
// arrives, and always books its next run.
func TestOrganizerContact_PurgeRunsOnASchedule(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	cf := newContactFixture(t, srv, true)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = $1`, contactform.PurgeJobType)
	})
	var oldID, newID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizer_contact_messages (org_id, channel_id, page_slug, sender_name, sender_email, message, created_at)
VALUES ($1, $2, 'p', 'Old', 'old@b.example', 'an old message here', now() - interval '91 days') RETURNING id`, cf.f.orgID, cf.channel).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO organizer_contact_messages (org_id, channel_id, page_slug, sender_name, sender_email, message, created_at)
VALUES ($1, $2, 'p', 'New', 'new@b.example', 'a recent message', now() - interval '89 days') RETURNING id`, cf.f.orgID, cf.channel).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1`, contactform.PurgeJobType); err != nil {
		t.Fatal(err)
	}
	if err := contactform.ScheduleInitialPurge(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := contactform.ScheduleInitialPurge(ctx, pool); err != nil { // never a second chain
		t.Fatal(err)
	}
	if err := contactform.NewPurgeHandler(pool, nil)(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizer_contact_messages WHERE id = ANY($1)`, []uuid.UUID{oldID, newID}).Scan(&left); err != nil || left != 1 {
		t.Errorf("after the purge %d of the two messages are left (%v), want only the recent one", left, err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status = 'pending' AND scheduled_at > now()`, contactform.PurgeJobType).Scan(&pending); err != nil || pending != 1 {
		t.Errorf("next purge runs booked: %d (%v), want 1", pending, err)
	}
}

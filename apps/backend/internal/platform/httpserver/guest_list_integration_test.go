//go:build integration

// guest_list_integration_test.go — the guest list of a session (owner
// decision 2026-11-02, package guestlist, migration 0187) through the REAL
// router, on the EC-07 export fixture (two active tickets of one order).
//
//   - the file: active tickets only (a cancelled one is left out), sorted by
//     the guest's name, the name on the ticket before the buyer's, VIP and
//     invitation columns; an agent is refused, another organization gets 404;
//   - the subscription: a member turns it on and off, the answer says how the
//     list would reach them (e-mail without the bot, telegram with it);
//   - the schedule: the moment "3 hours before" writes one delivery per
//     subscriber, a second pass writes nothing, and a member without a bot
//     link gets the list by e-mail with the CSV attached;
//   - the bot's routes: claim hands a linked member's delivery out once per
//     lease, finish closes it, a second finish is a 404.
//
// Run against a fresh migrated database (AGENTS.md CI-Integration recipe).
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
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
)

const guestListServiceToken = "guest-list-service-token-0123456789"

// glMailSender records the letters it is asked to send.
type glMailSender struct {
	mu   sync.Mutex
	sent []email.Message
}

func (c *glMailSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func TestGuestList_FileSubscriptionScheduleAndBotRoutes(t *testing.T) {
	f := newEC07Fixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM guest_list_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM guest_list_subscriptions WHERE org_id = $1`, f.orgID)
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.q.DB().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	// Ticket 2 carries its own guest's name and the VIP mark; the buyer is
	// renamed so the expected order is the same under any collation.
	exec(`UPDATE orders SET buyer_name = 'Zed Buyer' WHERE id = (SELECT order_id FROM tickets WHERE id = $1)`, f.ticket1)
	exec(`UPDATE tickets SET holder_name = 'Anna Guest', vip = true WHERE id = $1`, f.ticket2)

	manager := f.member(t, "organizer")
	agent := f.member(t, "agent")
	orgPath := "/v1/organizations/" + f.orgID.String()
	path := orgPath + "/sessions/" + f.sessID.String() + "/guest-list.csv?locale=en"

	st, hdr, body := f.get(t, path, manager, nil)
	if st != http.StatusOK {
		t.Fatalf("guest list: %d %s", st, body)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, `filename="guests_ec07-event-`) || !strings.Contains(cd, "_2026-12-24.csv") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	lines := ec07Lines(t, body)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want header + 2:\n%s", len(lines), body)
	}
	if lines[0] != `"Guest";"Category";"Seat";"Ticket";"Barcode";"VIP";"Entered";"Entry time";"Order";"E-mail";"Phone";"Invitation"` {
		t.Errorf("header = %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], `"Anna Guest";"Parterre";"";`) || !strings.Contains(lines[1], `;"yes";"no";"";`) {
		t.Errorf("first row (Anna, VIP, not entered) = %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], `"Zed Buyer";"Parterre";"";`) || !strings.Contains(lines[2], `="4600051000001"`) ||
		!strings.Contains(lines[2], `;"no";"yes";"2026-`) || !strings.Contains(lines[2], `="+34600111222"`) {
		t.Errorf("second row (Zed, entered, stored barcode, phone) = %s", lines[2])
	}

	// A cancelled ticket is not a guest.
	exec(`UPDATE tickets SET status = 'cancelled' WHERE id = $1`, f.ticket2)
	_, _, body = f.get(t, path, manager, nil)
	if l := ec07Lines(t, body); len(l) != 2 || strings.Contains(string(body), "Anna Guest") {
		t.Errorf("a cancelled ticket stayed on the list:\n%s", body)
	}
	exec(`UPDATE tickets SET status = 'active' WHERE id = $1`, f.ticket2)

	// An agent holds no order.read; another organization's path is a 404.
	if st, _, _ := f.get(t, path, agent, nil); st != http.StatusForbidden {
		t.Errorf("agent: %d, want 403", st)
	}
	other := f.prom0113Fixture.org(t, "GlOther")
	t.Cleanup(func() {
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM memberships WHERE org_id = $1`, other)
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, other)
	})
	otherUser, err := f.q.InsertUser(ctx, "gl-other-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.InsertMembership(ctx, otherUser.ID, other, "organizer"); err != nil {
		t.Fatal(err)
	}
	otherTok := f.tokenFor(t, otherUser.ID)
	st, _, body = f.get(t, "/v1/organizations/"+other.String()+"/sessions/"+f.sessID.String()+"/guest-list.csv", otherTok, nil)
	if st != http.StatusNotFound || ec07ErrorCode(t, body) != "session.not_found" {
		t.Errorf("foreign path: %d %s, want 404 session.not_found", st, body)
	}

	// The subscription of the calling member.
	sub := orgPath + "/guest-list-subscription"
	if got := f.subscription(t, http.MethodGet, sub, manager, ""); got.Enabled || got.Channel != "email" || !got.Eligible {
		t.Errorf("initial subscription = %+v, want off by e-mail", got)
	}
	if got := f.subscription(t, http.MethodPut, sub, manager, `{"enabled":true}`); !got.Enabled {
		t.Errorf("after PUT true = %+v", got)
	}
	if got := f.subscription(t, http.MethodPut, sub, manager, `{"enabled":true}`); !got.Enabled {
		t.Errorf("a repeat changed the setting: %+v", got)
	}
	if code := f.rawStatus(t, http.MethodPut, sub, manager, `{"enabled":"yes"}`); code != http.StatusBadRequest {
		t.Errorf("bad body: %d, want 400", code)
	}
	if code := f.rawStatus(t, http.MethodPut, sub, agent, `{"enabled":true}`); code != http.StatusForbidden {
		t.Errorf("agent subscription: %d, want 403", code)
	}

	// The schedule: one minute after "3 hours before" (start 2026-12-24 17:30Z).
	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC)
	mail := &glMailSender{}
	sched := guestlist.NewScheduler(guestlist.Options{Pool: f.srv.pgxPool, Sender: mail})
	written, mailed, err := sched.RunOnce(ctx, start.Add(-3*time.Hour+time.Minute))
	if err != nil || written != 1 || mailed != 1 {
		t.Fatalf("RunOnce at h3: written=%d mailed=%d err=%v", written, mailed, err)
	}
	written, mailed, err = sched.RunOnce(ctx, start.Add(-3*time.Hour+2*time.Minute))
	if err != nil || written != 0 || mailed != 0 {
		t.Fatalf("second RunOnce: written=%d mailed=%d err=%v", written, mailed, err)
	}
	if len(mail.sent) != 1 || len(mail.sent[0].Attachments) != 1 ||
		!strings.HasPrefix(mail.sent[0].Attachments[0].Filename, "guests_ec07-event-") ||
		!bytes.Contains(mail.sent[0].Attachments[0].Data, []byte("Anna Guest")) ||
		!strings.Contains(mail.sent[0].TextBody, "3 hours") {
		t.Fatalf("letter = %+v", mail.sent)
	}
	var slot string
	var sent bool
	if err := f.q.DB().QueryRow(ctx, `SELECT slot, sent_at IS NOT NULL FROM guest_list_deliveries WHERE org_id = $1`, f.orgID).Scan(&slot, &sent); err != nil || slot != "h3" || !sent {
		t.Fatalf("delivery slot=%q sent=%v err=%v", slot, sent, err)
	}
	// Too late: a moment more than 30 minutes ago is never written.
	if written, _, _ := sched.RunOnce(ctx, start.Add(-24*time.Hour+31*time.Minute)); written != 0 {
		t.Errorf("a missed moment was written: %d", written)
	}
}

// TestGuestList_BotClaimAndFinish drives the bot's two service routes.
func TestGuestList_BotClaimAndFinish(t *testing.T) {
	srv, secret := productionIntegrationServerCfg(t, func(c *config.Config) { c.BotServiceToken = guestListServiceToken })
	f := newEC07FixtureOn(t, srv, secret)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM guest_list_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM guest_list_subscriptions WHERE org_id = $1`, f.orgID)
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = 990077`)
	})
	user, err := f.q.InsertUser(ctx, "gl-bot-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.InsertMembership(ctx, user.ID, f.orgID, "org_admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.DB().Exec(ctx, `INSERT INTO bot_telegram_links (telegram_user_id, user_id, locale) VALUES (990077, $1, 'ru')`, user.ID); err != nil {
		t.Fatal(err)
	}
	tok := f.tokenFor(t, user.ID)
	if got := f.subscription(t, http.MethodPut, "/v1/organizations/"+f.orgID.String()+"/guest-list-subscription", tok, `{"enabled":true}`); got.Channel != "telegram" {
		t.Fatalf("a linked member's channel = %+v, want telegram", got)
	}
	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC)
	sched := guestlist.NewScheduler(guestlist.Options{Pool: srv.pgxPool, Sender: &glMailSender{}})
	if written, mailed, err := sched.RunOnce(ctx, start.Add(time.Minute)); err != nil || written != 1 || mailed != 0 {
		t.Fatalf("RunOnce at doors: written=%d mailed=%d err=%v", written, mailed, err)
	}

	claim := func(token string) (int, []map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/v1/bot/guest-list-deliveries/claim", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		var out struct {
			Deliveries []map[string]any `json:"deliveries"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Deliveries
	}
	if code, _ := claim("wrong-token"); code != http.StatusUnauthorized {
		t.Errorf("wrong service token: %d, want 401", code)
	}
	code, ds := claim(guestListServiceToken)
	if code != http.StatusOK || len(ds) != 1 || ds[0]["slot"] != "doors" || ds[0]["telegram_user_id"] != float64(990077) ||
		ds[0]["session_id"] != f.sessID.String() {
		t.Fatalf("claim: %d %v", code, ds)
	}
	// Taken for 10 minutes: a second claim right away hands nothing out.
	if _, again := claim(guestListServiceToken); len(again) != 0 {
		t.Errorf("a taken delivery was handed out again: %v", again)
	}
	finish := func(id, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/bot/guest-list-deliveries/"+id+"/finish", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+guestListServiceToken)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w.Code
	}
	id := ds[0]["id"].(string)
	if ds[0]["attempt"] != float64(1) {
		t.Fatalf("first claim's attempt = %v, want 1", ds[0]["attempt"])
	}
	if c := finish(id, `{"attempt":1,"outcome":"done"}`); c != http.StatusBadRequest {
		t.Errorf("bad outcome: %d, want 400", c)
	}
	if c := finish(id, `{"outcome":"sent"}`); c != http.StatusBadRequest {
		t.Errorf("finish without the attempt: %d, want 400", c)
	}
	// A report of another claim than the row's current one changes nothing.
	if c := finish(id, `{"attempt":2,"outcome":"sent"}`); c != http.StatusNotFound {
		t.Errorf("finish of a claim that never was: %d, want 404", c)
	}
	// A failure keeps the lease: the row is not handed out again at once.
	if c := finish(id, `{"attempt":1,"outcome":"failed","reason":"send_failed"}`); c != http.StatusNoContent {
		t.Errorf("finish failed: %d, want 204", c)
	}
	if _, again := claim(guestListServiceToken); len(again) != 0 {
		t.Errorf("a failed delivery was handed out again inside its lease: %v", again)
	}
	// Lease over: the second claim is attempt 2, and the late report of the
	// first claim is refused.
	if _, err := f.q.DB().Exec(ctx, `UPDATE guest_list_deliveries SET claimed_at = now() - interval '11 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	_, ds = claim(guestListServiceToken)
	if len(ds) != 1 || ds[0]["attempt"] != float64(2) {
		t.Fatalf("claim after the lease: %v, want attempt 2", ds)
	}
	if c := finish(id, `{"attempt":1,"outcome":"sent"}`); c != http.StatusNotFound {
		t.Errorf("late report of the first claim: %d, want 404", c)
	}
	if c := finish(id, `{"attempt":2,"outcome":"sent"}`); c != http.StatusNoContent {
		t.Errorf("finish: %d, want 204", c)
	}
	if c := finish(id, `{"attempt":2,"outcome":"sent"}`); c != http.StatusNotFound {
		t.Errorf("second finish: %d, want 404", c)
	}
}

// tokenFor mints a JWT for a user of the fixture's server.
func (f *ec07Fixture) tokenFor(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	tok, _, err := auth.IssueJWT(f.secret, userID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *ec07Fixture) rawStatus(t *testing.T, method, path, bearer, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type glSubscription struct {
	Enabled  bool   `json:"enabled"`
	Channel  string `json:"channel"`
	Eligible bool   `json:"eligible"`
}

func (f *ec07Fixture) subscription(t *testing.T, method, path, bearer, body string) glSubscription {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out glSubscription
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d", method, path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

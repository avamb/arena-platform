//go:build integration

// guest_list_team_integration_test.go — the guest list for the whole team
// (owner request 2026-11-08, package guestlistteam, migration 0211) and the
// per-event workbook, on the EC-07 export fixture.
//
//   - the job sends the CURRENT list 24 h and 3 h before the start, each at
//     most once (a second pass, and a pass after a restart of the state in
//     memory, write nothing): outside the windows nothing goes;
//   - recipients: the owner and the manager by e-mail, never an agent, a
//     suspended member or someone who already has their own subscription;
//   - Telegram: only the allowed groups of THIS organization, never an
//     operator row (org_id NULL) nor a group that is switched off;
//   - the organization switch and a cancelled session stop the send;
//   - the file holds every ticket, a cancelled one with status 3;
//   - GET .../events/{event_id}/guest-list.xlsx and the settings route.
//
// Run against a fresh migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlistteam"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport/xlsxtest"
)

type glDocSent struct {
	chat, name, caption string
	body                []byte
}

// glDocStub records the documents the "Telegram" stub is asked to send.
type glDocStub struct {
	mu   sync.Mutex
	sent []glDocSent
}

func (s *glDocStub) SendDocument(_ context.Context, chatID, filename, _ string, data []byte, caption string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, glDocSent{chat: chatID, name: filename, caption: caption, body: data})
	return nil
}

func (s *glDocStub) chats() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, d := range s.sent {
		out[d.chat]++
	}
	return out
}

func TestGuestListTeam_JobWindowsDedupeRecipientsAndChats(t *testing.T) {
	f := newEC07Fixture(t)
	ctx := context.Background()
	db := f.q.DB()
	nonce := uuid.NewString()[:8]
	chatA, chatOff, chatOp := "-100"+nonce+"1", "-100"+nonce+"2", "-100"+nonce+"3"
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Exec(bg, `DELETE FROM guest_list_org_sends WHERE org_id = $1`, f.orgID)
		_, _ = db.Exec(bg, `DELETE FROM guest_list_subscriptions WHERE org_id = $1`, f.orgID)
		_, _ = db.Exec(bg, `DELETE FROM sales_notification_subscriptions WHERE org_id = $1 OR chat_id = $2`, f.orgID, chatOp)
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	person := func(role string) (uuid.UUID, string) {
		t.Helper()
		u, err := f.q.InsertUser(ctx, "gl-team-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.q.InsertMembership(ctx, u.ID, f.orgID, role); err != nil {
			t.Fatal(err)
		}
		var email string
		if err := db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, u.ID).Scan(&email); err != nil {
			t.Fatal(err)
		}
		return u.ID, email
	}
	_, ownerMail := person("org_admin")
	_, managerMail := person("organizer")
	_, agentMail := person("agent")
	suspended, suspendedMail := person("organizer")
	exec(`UPDATE memberships SET status = 'suspended' WHERE org_id = $1 AND user_id = $2`, f.orgID, suspended)
	subscribed, subscribedMail := person("organizer")
	exec(`INSERT INTO guest_list_subscriptions (org_id, user_id) VALUES ($1, $2)`, f.orgID, subscribed)

	exec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, allowed) VALUES ($1, 'team', $2, true)`, f.orgID, chatA)
	exec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, allowed) VALUES ($1, 'muted', $2, false)`, f.orgID, chatOff)
	exec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, allowed) VALUES (NULL, 'operator', $1, true)`, chatOp)

	// One of the two tickets is cancelled: it is in the file with status 3.
	exec(`UPDATE tickets SET status = 'cancelled', cancelled_at = now(), cancellation_reason = 'test' WHERE id = $1`, f.ticket2)

	mail := &glMailSender{}
	docs := &glDocStub{}
	job := guestlistteam.New(guestlistteam.Options{Pool: f.srv.pgxPool, Sender: mail, Docs: docs})
	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC)
	mailedTo := func() map[string]int {
		mail.mu.Lock()
		defer mail.mu.Unlock()
		out := map[string]int{}
		for _, m := range mail.sent {
			out[m.To]++
		}
		return out
	}
	sends := func() int {
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM guest_list_org_sends WHERE session_id = $1`, f.sessID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Too early (more than 24 h before): nothing, nothing recorded.
	if n, err := job.RunOnce(ctx, start.Add(-25*time.Hour)); err != nil || n != 0 || sends() != 0 {
		t.Fatalf("25 h before: n=%d err=%v rows=%d", n, err, sends())
	}

	// 24 h before: one list to the owner and the manager, one document to the
	// organization's allowed group.
	t24 := start.Add(-24*time.Hour + time.Minute)
	if n, err := job.RunOnce(ctx, t24); err != nil || n != 1 {
		t.Fatalf("24 h: n=%d err=%v", n, err)
	}
	got := mailedTo()
	if len(got) != 2 || got[ownerMail] != 1 || got[managerMail] != 1 {
		t.Fatalf("recipients = %v, want exactly the owner and the manager (not %s %s %s)", got, agentMail, suspendedMail, subscribedMail)
	}
	if c := docs.chats(); len(c) != 1 || c[chatA] != 1 {
		t.Fatalf("Telegram chats = %v, want only the organization's allowed group %s", c, chatA)
	}
	mail.mu.Lock()
	msg := mail.sent[0]
	mail.mu.Unlock()
	if len(msg.Attachments) != 1 || !strings.HasSuffix(msg.Attachments[0].Filename, ".xlsx") ||
		!strings.Contains(msg.Subject, "Ec07 Event") || !strings.Contains(msg.Subject, "24.12.2026 18:30") ||
		!strings.Contains(msg.TextBody, "Checked in: 1.") || !strings.Contains(msg.TextBody, "Refunded / cancelled: 1.") {
		t.Fatalf("letter = subject %q body %q attachments %d", msg.Subject, msg.TextBody, len(msg.Attachments))
	}
	p := xlsxtest.Open(t, msg.Attachments[0].Data)
	if v := p.Values(); len(v) != 3 {
		t.Fatalf("workbook rows = %d, want header + 2 tickets (cancelled one included)", len(v))
	}

	// Dedupe: the same moment, and a later pass still before 3 h, send nothing.
	if n, err := job.RunOnce(ctx, t24.Add(time.Minute)); err != nil || n != 0 || len(mailedTo()) != 2 || sends() != 1 {
		t.Fatalf("second pass: n=%d err=%v letters=%v rows=%d", n, err, mailedTo(), sends())
	}
	// A fresh job object (a worker restart) honours the stored row.
	job2 := guestlistteam.New(guestlistteam.Options{Pool: f.srv.pgxPool, Sender: mail, Docs: docs})
	if n, err := job2.RunOnce(ctx, t24.Add(2*time.Minute)); err != nil || n != 0 {
		t.Fatalf("after a restart: n=%d err=%v", n, err)
	}

	// 3 h before: the 3 h list (the 24 h one is not repeated).
	t3 := start.Add(-3*time.Hour + time.Minute)
	if n, err := job2.RunOnce(ctx, t3); err != nil || n != 1 || sends() != 2 {
		t.Fatalf("3 h: n=%d err=%v rows=%d", n, err, sends())
	}
	if m := mailedTo(); m[ownerMail] != 2 || m[managerMail] != 2 || docs.chats()[chatA] != 2 {
		t.Fatalf("after the 3 h list: letters=%v chats=%v", m, docs.chats())
	}
	if n, err := job2.RunOnce(ctx, t3.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("second 3 h pass: n=%d err=%v", n, err)
	}
	// After the start nothing is due.
	if n, err := job2.RunOnce(ctx, start.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("after the start: n=%d err=%v", n, err)
	}
	var chatsOp int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM sales_notification_subscriptions WHERE chat_id = $1 AND last_error IS NOT NULL`, chatOp).Scan(&chatsOp); err != nil || chatsOp != 0 {
		t.Fatalf("operator row touched: %d %v", chatsOp, err)
	}

	// The organization switch and a cancelled session stop the send.
	exec(`DELETE FROM guest_list_org_sends WHERE session_id = $1`, f.sessID)
	exec(`UPDATE organizations SET guest_list_auto_send = false WHERE id = $1`, f.orgID)
	before := len(mailedTo())
	if n, err := job2.RunOnce(ctx, t3); err != nil || n != 0 || sends() != 0 {
		t.Fatalf("switch off: n=%d err=%v rows=%d", n, err, sends())
	}
	exec(`UPDATE organizations SET guest_list_auto_send = true WHERE id = $1`, f.orgID)
	exec(`UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.sessID)
	if n, err := job2.RunOnce(ctx, t3); err != nil || n != 0 || sends() != 0 {
		t.Fatalf("cancelled session: n=%d err=%v rows=%d", n, err, sends())
	}
	exec(`UPDATE sessions SET status = 'scheduled' WHERE id = $1`, f.sessID)
	exec(`UPDATE sessions SET deleted_at = now() WHERE id = $1`, f.sessID)
	if n, err := job2.RunOnce(ctx, t3); err != nil || n != 0 || sends() != 0 {
		t.Fatalf("deleted session: n=%d err=%v rows=%d", n, err, sends())
	}
	exec(`UPDATE sessions SET deleted_at = NULL WHERE id = $1`, f.sessID)
	// A session without tickets is skipped: a second session of the event,
	// inside the 24 h window, with no ticket at all.
	empty := uuid.New()
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `DELETE FROM sessions WHERE id = $1`, empty) })
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source, status)
	      SELECT $1, event_id, venue_id, '2026-12-24T20:30:00Z', '2026-12-24T22:30:00Z', 10, 'EUR', 'override', 'scheduled'
	      FROM sessions WHERE id = $2`, empty, f.sessID)
	exec(`UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.sessID) // keep the first one out of the way
	if n, err := job2.RunOnce(ctx, t3); err != nil || n != 0 {
		t.Fatalf("session without tickets: n=%d err=%v", n, err)
	}
	var emptyRows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM guest_list_org_sends WHERE session_id = $1`, empty).Scan(&emptyRows); err != nil || emptyRows != 0 {
		t.Fatalf("a row was recorded for a session without tickets: %d %v", emptyRows, err)
	}
	exec(`DELETE FROM sessions WHERE id = $1`, empty)
	exec(`UPDATE sessions SET status = 'scheduled' WHERE id = $1`, f.sessID)
	if len(mailedTo()) != before {
		t.Fatalf("letters sent while stopped: %v", mailedTo())
	}
}

func TestGuestListTeam_EventWorkbookAndSettingsRoute(t *testing.T) {
	f := newEC07Fixture(t)
	t.Cleanup(func() {
		_, _ = f.q.DB().Exec(context.Background(), `UPDATE organizations SET guest_list_auto_send = true WHERE id = $1`, f.orgID)
	})
	owner := f.member(t, "org_admin")
	manager := f.member(t, "organizer")
	agent := f.member(t, "agent")
	org := "/v1/organizations/" + f.orgID.String()

	// The event workbook: a Session column first, the venue's clock (Prague).
	path := org + "/events/" + f.eventID.String() + "/guest-list.xlsx?locale=en"
	status, hdr, body := f.get(t, path, manager, nil)
	if status != http.StatusOK || !strings.Contains(hdr.Get("Content-Type"), "spreadsheetml") ||
		!strings.HasPrefix(hdr.Get("Content-Disposition"), `attachment; filename="guests_ec07-event`) {
		t.Fatalf("event workbook: %d %v", status, hdr)
	}
	p := xlsxtest.Open(t, body)
	v := p.Values()
	if len(v) != 3 || v[0][0] != "Session" || v[0][1] != "Last name" || v[1][0] != "24.12.2026 18:30" {
		t.Fatalf("event workbook values = %v", v)
	}
	if status, _, _ := f.get(t, path, agent, nil); status != http.StatusForbidden {
		t.Fatalf("agent: %d, want 403", status)
	}
	if status, _, _ := f.get(t, org+"/events/"+uuid.NewString()+"/guest-list.xlsx", manager, nil); status != http.StatusNotFound {
		t.Fatalf("unknown event: %d, want 404", status)
	}
	if !bytes.Contains(body, []byte("PK")) {
		t.Fatal("not a zip")
	}

	// The settings: any member reads, only the owner writes.
	status, _, raw := f.get(t, org+"/guest-list-settings", manager, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"auto_send":true`) {
		t.Fatalf("GET settings: %d %s", status, raw)
	}
	put := func(tok, body string) (int, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, f.ts.URL+org+"/guest-list-settings", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	if status, _ := put(manager, `{"auto_send":false}`); status != http.StatusForbidden {
		t.Fatalf("manager PUT: %d, want 403 (owner only)", status)
	}
	if status, out := put(owner, `{"auto_send":false}`); status != http.StatusOK || !strings.Contains(out, `"auto_send":false`) {
		t.Fatalf("owner PUT: %d %s", status, out)
	}
	if status, _ := put(owner, `{"auto_send":"no"}`); status != http.StatusBadRequest {
		t.Fatalf("bad body: %d, want 400", status)
	}
	if status, _, raw := f.get(t, org+"/guest-list-settings", manager, nil); status != http.StatusOK || !strings.Contains(string(raw), `"auto_send":false`) {
		t.Fatalf("GET after PUT: %d %s", status, raw)
	}
}

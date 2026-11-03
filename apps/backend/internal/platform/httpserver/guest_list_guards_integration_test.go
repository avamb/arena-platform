//go:build integration

// guest_list_guards_integration_test.go — what keeps the automatic guest
// list from going where it must not (review of PR #362, 2026-11-02):
//
//   - a row written for a person who then lost the role, whose organization
//     was archived or who turned the list off is not sent;
//   - a person whose bot link was revoked gets the list by e-mail;
//   - doors at the very moment of the 3-hour list is one file, not two;
//   - a session moved later waits for its new moment and is sent once then;
//   - an organization API key never reads the file;
//   - rows 90 days after their moment are deleted.
//
// Run against a fresh migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
)

func TestGuestList_SendTimeGuards(t *testing.T) {
	f := newEC07Fixture(t)
	ctx := context.Background()
	db := f.q.DB()
	const tg = int64(990079)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Exec(bg, `UPDATE organizations SET deleted_at = NULL WHERE id = $1`, f.orgID)
		_, _ = db.Exec(bg, `DELETE FROM guest_list_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = db.Exec(bg, `DELETE FROM guest_list_subscriptions WHERE org_id = $1`, f.orgID)
		_, _ = db.Exec(bg, `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tg)
		_, _ = db.Exec(bg, `DELETE FROM api_keys WHERE org_id = $1`, f.orgID)
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	person := func(role string) (uuid.UUID, string) {
		t.Helper()
		u, err := f.q.InsertUser(ctx, "gl-guard-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.q.InsertMembership(ctx, u.ID, f.orgID, role); err != nil {
			t.Fatal(err)
		}
		return u.ID, f.tokenFor(t, u.ID)
	}
	manager, managerTok := person("organizer")
	owner, ownerTok := person("org_admin")
	exec(`INSERT INTO bot_telegram_links (telegram_user_id, user_id, locale) VALUES ($1, $2, 'en')`, tg, owner)
	sub := "/v1/organizations/" + f.orgID.String() + "/guest-list-subscription"
	f.subscription(t, http.MethodPut, sub, managerTok, `{"enabled":true}`)
	if got := f.subscription(t, http.MethodPut, sub, ownerTok, `{"enabled":true}`); got.Channel != "telegram" {
		t.Fatalf("owner's channel = %+v, want telegram", got)
	}

	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC)
	mail := &glMailSender{}
	sched := guestlist.NewScheduler(guestlist.Options{Pool: f.srv.pgxPool, Sender: mail})
	mailedTo := func() map[string]int {
		mail.mu.Lock()
		defer mail.mu.Unlock()
		out := map[string]int{}
		for _, m := range mail.sent {
			out[m.To]++
		}
		return out
	}
	emailOf := func(id uuid.UUID) string {
		var e string
		if err := db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, id).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	managerMail, ownerMail := emailOf(manager), emailOf(owner)

	// ── 24 hours before: rows are written, then the manager loses the role
	// and the owner's bot link is revoked. The manager gets nothing, the
	// owner gets the list by e-mail; a restored role is sent after all.
	t24 := start.Add(-24*time.Hour + time.Minute)
	if n, err := gen.New(f.srv.pgxPool).ScheduleGuestListDeliveries(ctx, t24, gen.GuestListGrace); err != nil || n != 2 {
		t.Fatalf("schedule h24: %d %v, want 2", n, err)
	}
	exec(`UPDATE memberships SET status = 'suspended' WHERE org_id = $1 AND user_id = $2`, f.orgID, manager)
	exec(`UPDATE bot_telegram_links SET revoked_at = now() WHERE telegram_user_id = $1`, tg)
	if _, mailed, err := sched.RunOnce(ctx, t24.Add(time.Minute)); err != nil || mailed != 1 {
		t.Fatalf("h24 after the role was lost: mailed=%d err=%v, want 1 (the owner by e-mail)", mailed, err)
	}
	if got := mailedTo(); got[ownerMail] != 1 || got[managerMail] != 0 {
		t.Fatalf("letters after the role was lost = %v", got)
	}
	exec(`UPDATE memberships SET status = 'active' WHERE org_id = $1 AND user_id = $2`, f.orgID, manager)
	if _, mailed, err := sched.RunOnce(ctx, t24.Add(2*time.Minute)); err != nil || mailed != 1 || mailedTo()[managerMail] != 1 {
		t.Fatalf("h24 after the role came back: mailed=%d err=%v letters=%v", mailed, err, mailedTo())
	}

	// ── 3 hours before, doors at the same moment: one row per person, no
	// doors row. The organization is archived before the send: nothing goes
	// out until it is back.
	exec(`UPDATE sessions SET doors_open_at = start_at - interval '3 hours' WHERE id = $1`, f.sessID)
	t3 := start.Add(-3*time.Hour + time.Minute)
	if n, err := gen.New(f.srv.pgxPool).ScheduleGuestListDeliveries(ctx, t3, gen.GuestListGrace); err != nil || n != 2 {
		t.Fatalf("schedule h3 with doors at the same moment: %d %v, want 2", n, err)
	}
	var doors int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM guest_list_deliveries WHERE org_id = $1 AND slot = 'doors'`, f.orgID).Scan(&doors); err != nil || doors != 0 {
		t.Fatalf("doors rows = %d %v, want 0", doors, err)
	}
	exec(`UPDATE organizations SET deleted_at = now() WHERE id = $1`, f.orgID)
	if _, mailed, err := sched.RunOnce(ctx, t3.Add(time.Minute)); err != nil || mailed != 0 {
		t.Fatalf("archived organization: mailed=%d err=%v, want 0", mailed, err)
	}
	exec(`UPDATE organizations SET deleted_at = NULL WHERE id = $1`, f.orgID)
	if _, mailed, err := sched.RunOnce(ctx, t3.Add(2*time.Minute)); err != nil || mailed != 2 {
		t.Fatalf("organization back: mailed=%d err=%v, want 2", mailed, err)
	}
	exec(`UPDATE sessions SET doors_open_at = NULL WHERE id = $1`, f.sessID)

	// ── Doors (the start): the manager turns the list off after the rows were
	// written, so only the owner gets it.
	td := start.Add(time.Minute)
	if n, err := gen.New(f.srv.pgxPool).ScheduleGuestListDeliveries(ctx, td, gen.GuestListGrace); err != nil || n != 2 {
		t.Fatalf("schedule doors: %d %v, want 2", n, err)
	}
	f.subscription(t, http.MethodPut, sub, managerTok, `{"enabled":false}`)
	before := mailedTo()
	if _, mailed, err := sched.RunOnce(ctx, td.Add(time.Minute)); err != nil || mailed != 1 ||
		mailedTo()[ownerMail] != before[ownerMail]+1 || mailedTo()[managerMail] != before[managerMail] {
		t.Fatalf("after turning it off: mailed=%d err=%v letters=%v", mailed, err, mailedTo())
	}

	// ── An organization API key never reads the file.
	_, rawKey, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID: f.orgID, Name: "gl-" + uuid.NewString()[:8], Scopes: []string{"order.read", "session.read"}, CreatedBy: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"csv", "xlsx"} {
		file := "/v1/organizations/" + f.orgID.String() + "/sessions/" + f.sessID.String() + "/guest-list." + ext
		if st, _, body := f.get(t, file, rawKey, nil); st != http.StatusForbidden || ec07ErrorCode(t, body) != "guest_list.api_key_forbidden" {
			t.Errorf("API key %s: %d %s, want 403 guest_list.api_key_forbidden", ext, st, body)
		}
	}

	// ── Rows 90 days after their moment are deleted, newer ones stay.
	exec(`UPDATE guest_list_deliveries SET due_at = now() - interval '91 days' WHERE org_id = $1 AND slot = 'h24'`, f.orgID)
	if n, err := f.q.PurgeGuestListDeliveries(ctx, time.Now()); err != nil || n < 2 {
		t.Fatalf("purge: %d %v, want at least the 2 old rows", n, err)
	}
	var left int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM guest_list_deliveries WHERE org_id = $1`, f.orgID).Scan(&left); err != nil || left != 4 {
		t.Fatalf("rows left after the purge = %d %v, want 4 (h3 and doors)", left, err)
	}
}

// TestGuestList_MovedSessionWaitsForItsNewMoment: a row written before the
// session moved later is not sent at the old moment; the new moment sends it
// once.
func TestGuestList_MovedSessionWaitsForItsNewMoment(t *testing.T) {
	f := newEC07Fixture(t)
	ctx := context.Background()
	db := f.q.DB()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Exec(bg, `DELETE FROM guest_list_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = db.Exec(bg, `DELETE FROM guest_list_subscriptions WHERE org_id = $1`, f.orgID)
	})
	managerTok := f.member(t, "organizer")
	f.subscription(t, http.MethodPut, "/v1/organizations/"+f.orgID.String()+"/guest-list-subscription", managerTok, `{"enabled":true}`)

	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC)
	t3 := start.Add(-3*time.Hour + time.Minute)
	mail := &glMailSender{}
	sched := guestlist.NewScheduler(guestlist.Options{Pool: f.srv.pgxPool, Sender: mail})
	if n, err := gen.New(f.srv.pgxPool).ScheduleGuestListDeliveries(ctx, t3, gen.GuestListGrace); err != nil || n != 1 {
		t.Fatalf("schedule h3: %d %v, want 1", n, err)
	}
	if _, err := db.Exec(ctx, `UPDATE sessions SET start_at = start_at + interval '1 day', end_at = end_at + interval '1 day' WHERE id = $1`, f.sessID); err != nil {
		t.Fatal(err)
	}
	if _, mailed, err := sched.RunOnce(ctx, t3.Add(time.Minute)); err != nil || mailed != 0 {
		t.Fatalf("old moment after the move: mailed=%d err=%v, want 0", mailed, err)
	}
	newT3 := t3.Add(24 * time.Hour)
	written, mailed, err := sched.RunOnce(ctx, newT3)
	if err != nil || written != 1 || mailed != 1 {
		t.Fatalf("new moment: written=%d mailed=%d err=%v, want 1 and 1", written, mailed, err)
	}
	if written, mailed, err := sched.RunOnce(ctx, newT3.Add(time.Minute)); err != nil || written != 0 || mailed != 0 {
		t.Fatalf("a second pass at the new moment: written=%d mailed=%d err=%v, want nothing", written, mailed, err)
	}
}

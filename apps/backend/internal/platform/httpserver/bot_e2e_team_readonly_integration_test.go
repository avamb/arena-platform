//go:build integration

package httpserver

// Task U-8 (owner decision V-11, 2026-10-16): the bot's Team screen on THREE
// roles through the real router, the real bot and a stub Telegram. The owner
// has every control, the manager reads the list with no action button and every
// team:* press from them is refused and changes nothing (an old button, a
// forged payload), an agent has no Team row and the screen refuses. The stub
// records message TEXT and the callback data of the buttons, so the buttons are
// read from the keyboard and the effects from the database.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestBotE2E_TeamScreen_OwnerManagesManagerReadsAgentRefused(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	ctx := context.Background()
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	const ownerTG, managerTG, agentTG = int64(811), int64(812), int64(813)
	linkDialogTestOwner(t, f, srv, ownerTG)
	ownerEmail := f.emails[0]

	m := &invManage{t: t, pool: pool, f: f, srv: srv}
	waitingEmail, managerEmail := f.newEmail("waiting"), f.newEmail("mgr")
	waitingInv, waitingUser := m.invite(f, waitingEmail, "manager")
	_, managerUser := m.invite(f, managerEmail, "manager")
	mgrCode, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, mgrCode, managerEmail, managerTG)); rec.Code != http.StatusOK {
		t.Fatalf("accept manager: %d %s", rec.Code, rec.Body.String())
	}
	// An agent, linked to Telegram directly (the bot invitation never makes one).
	agentEmail := f.newEmail("agent")
	q := gen.New(pool)
	agent, err := q.InsertUser(ctx, agentEmail, "x", "ru")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(ctx, agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, agentTG, agent.ID, nil, "ru", &f.orgID); err != nil {
		t.Fatalf("link agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE telegram_user_id = ANY($1)`, []int64{managerTG, agentTG})
	})

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)

	revoked := func() bool {
		var at *string
		if err := pool.QueryRow(ctx, `SELECT revoked_at::text FROM bot_invitations WHERE id = $1`, waitingInv).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at != nil
	}
	members := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE org_id = $1`, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	jobs := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'bot.invitation_email' AND payload->>'email' = $1`, waitingEmail).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	teamDialogs := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_dialogs WHERE kind = 'team' AND telegram_user_id = ANY($1)`, []int64{managerTG, agentTG}).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	startMembers, startJobs := members(), jobs()
	// teamButtons is the callback data of the Team screen's buttons.
	screen := func(tgID int64) []string {
		t.Helper()
		id, _, kb := tg.lastOf(tgID)
		if id == 0 {
			t.Fatal("the bot sent nothing")
		}
		return kb
	}

	// ── the owner: every control ────────────────────────────────────────────
	mk := tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team"))
	text := tg.waitSince(t, mk, "Сотрудники: ")
	if !strings.Contains(text, waitingEmail) || !strings.Contains(text, managerEmail) {
		t.Errorf("the owner's screen must list the team:\n%s", text)
	}
	kb := screen(ownerTG)
	for _, want := range []string{"team:invite", "team:rm:" + waitingUser.String(), "team:rs:" + waitingUser.String(), "team:ri:" + waitingUser.String(), "team:rm:" + managerUser.String()} {
		if !hasData(kb, want) {
			t.Errorf("the owner's Team screen lacks the button %q: %v", want, kb)
		}
	}

	// ── the manager: the list, no controls ──────────────────────────────────
	mk = tg.mark()
	tg.push(e2eCallbackAs(managerTG, "home"))
	tg.waitSince(t, mk, "")
	if !tg.waitButton(managerTG, "team", 5*time.Second) {
		t.Error("the manager's main menu must carry the Team row")
	}
	mk = tg.mark()
	tg.push(e2eCallbackAs(managerTG, "team"))
	text = tg.waitSince(t, mk, "Сотрудники: ")
	for _, want := range []string{ownerEmail, waitingEmail, managerEmail, "приглашение отправлено", "Здесь вы видите команду"} {
		if !strings.Contains(text, want) {
			t.Errorf("the manager's Team screen lacks %q:\n%s", want, text)
		}
	}
	kb = screen(managerTG)
	for _, d := range kb {
		if strings.HasPrefix(d, "team:") {
			t.Errorf("the manager's Team screen must carry no team action, found %q in %v", d, kb)
		}
	}
	if !hasData(kb, "home") {
		t.Errorf("the manager's Team screen must keep the way back: %v", kb)
	}
	// Forged presses: refused with the note, nothing changes.
	for _, data := range []string{
		"team:invite",
		"team:role:owner",
		"team:rs:" + waitingUser.String(),
		"team:ri:" + waitingUser.String(),
		"team:ri:" + waitingUser.String() + ":yes",
		"team:rm:" + waitingUser.String(),
		"team:rm:" + waitingUser.String() + ":yes",
		"team:rm:" + managerUser.String() + ":yes",
	} {
		mk = tg.mark()
		tg.push(e2eCallbackAs(managerTG, data))
		tg.waitSince(t, mk, "Команду меняет только владелец")
	}
	// A typed address after a forged "invite" is nobody's answer.
	mk = tg.mark()
	tg.push(e2eMessageAs(managerTG, "forged@arena-integration.test"))
	time.Sleep(500 * time.Millisecond)
	if n := teamDialogs(); n != 0 {
		t.Errorf("a manager's forged press opened %d team dialog(s)", n)
	}
	if revoked() || members() != startMembers || jobs() != startJobs {
		t.Fatalf("a manager's press changed something: revoked=%v members=%d/%d jobs=%d/%d", revoked(), members(), startMembers, jobs(), startJobs)
	}

	// ── the agent: no Team row, the screen refuses ──────────────────────────
	mk = tg.mark()
	tg.push(e2eCallbackAs(agentTG, "home"))
	tg.waitSince(t, mk, "")
	if !tg.waitButton(agentTG, "el:new", 5*time.Second) {
		t.Fatal("the agent's main menu never appeared")
	}
	if tg.waitButton(agentTG, "team", time.Second) || tg.waitButton(agentTG, "team:invite", 0) {
		t.Error("an agent must not see the Team row")
	}
	for _, data := range []string{"team", "team:invite", "team:rm:" + waitingUser.String() + ":yes", "team:ri:" + waitingUser.String() + ":yes"} {
		mk = tg.mark()
		tg.push(e2eCallbackAs(agentTG, data))
		tg.waitSince(t, mk, "управляет только владелец")
	}
	if revoked() || members() != startMembers || jobs() != startJobs || teamDialogs() != 0 {
		t.Fatalf("an agent's press changed something: revoked=%v members=%d/%d jobs=%d/%d", revoked(), members(), startMembers, jobs(), startJobs)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE resource_id = $1`, waitingInv.String())
}

func hasData(data []string, want string) bool {
	for _, d := range data {
		if d == want {
			return true
		}
	}
	return false
}

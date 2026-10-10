//go:build integration

package httpserver

// End-to-end proof of EC-16 (spec 35 §6.7) through the real router, the real
// bot and a stub Telegram: the Team screen shows where each invitation stands,
// the owner resends a letter (the ten-minute limit answers in plain words) and
// revokes an invitation after one confirmation, a stale button ends in a note,
// and a manager gets the owner-only answer and changes nothing. The stub
// records message TEXT only, so buttons are proven by pressing their
// callback_data and checking the database.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBotE2E_TeamInvitations_OwnerResendsAndRevokes_ManagerCannot(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	ctx := context.Background()
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	const ownerTG, managerTG = int64(793), int64(794)
	linkDialogTestOwner(t, f, srv, ownerTG)

	inviteUser := func(email, role string) (inv, user uuid.UUID) {
		t.Helper()
		m := &invManage{t: t, pool: pool, f: f, srv: srv}
		return m.invite(f, email, role)
	}
	waitingEmail, expiredEmail, managerEmail := f.newEmail("waiting"), f.newEmail("expired"), f.newEmail("mgr")
	waitingInv, waitingUser := inviteUser(waitingEmail, "manager")
	expiredInv, expiredUser := inviteUser(expiredEmail, "manager")
	if _, err := pool.Exec(ctx, `UPDATE bot_invitations SET expires_at = now() - interval '1 day', last_sent_at = now() - interval '8 days' WHERE id = $1`, expiredInv); err != nil {
		t.Fatal(err)
	}
	_, managerUser := inviteUser(managerEmail, "manager")
	mgrCode, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, mgrCode, managerEmail, managerTG)); rec.Code != http.StatusOK {
		t.Fatalf("accept manager: %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, managerTG) })

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)

	jobs := func(email string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'bot.invitation_email' AND payload->>'email' = $1`, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	revoked := func(inv uuid.UUID) bool {
		var at *string
		if err := pool.QueryRow(ctx, `SELECT revoked_at::text FROM bot_invitations WHERE id = $1`, inv).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at != nil
	}
	member := func(user uuid.UUID) bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2`, user, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}

	// ── the state of every invitation on the Team screen ────────────────────
	m := tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team"))
	screen := tg.waitSince(t, m, "Сотрудники: ")
	for _, want := range []string{
		waitingEmail + " — менеджер, приглашение отправлено, ещё не открыто",
		expiredEmail + " — менеджер, приглашение истекло",
		managerEmail + " — менеджер, Telegram ✓",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the Team screen lacks %q:\n%s", want, screen)
		}
	}

	// ── resend: too soon, then after ten minutes ────────────────────────────
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:rs:"+waitingUser.String()))
	soon := tg.waitSince(t, m, "меньше десяти минут")
	if !strings.Contains(soon, waitingEmail) || jobs(waitingEmail) != 1 {
		t.Errorf("a resend inside ten minutes must say so and queue nothing: jobs=%d\n%s", jobs(waitingEmail), soon)
	}
	if _, err := pool.Exec(ctx, `UPDATE bot_invitations SET last_sent_at = now() - interval '11 minutes' WHERE id = $1`, waitingInv); err != nil {
		t.Fatal(err)
	}
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:rs:"+waitingUser.String()))
	tg.waitSince(t, m, "Новое письмо со свежей ссылкой")
	if jobs(waitingEmail) != 2 {
		t.Errorf("the resend must queue a second letter: %d jobs", jobs(waitingEmail))
	}
	if code, payload := f.queuedCode(waitingEmail); code == "" || payload.Locale != "ru" {
		t.Errorf("the new letter: code %q locale %q, want a code and the owner's language", code, payload.Locale)
	}

	// ── the manager: owner-only answer, nothing changes ─────────────────────
	for _, data := range []string{"team", "team:rs:" + expiredUser.String(), "team:ri:" + expiredUser.String() + ":yes"} {
		m = tg.mark()
		tg.push(e2eCallbackAs(managerTG, data))
		tg.waitSince(t, m, "управляет только владелец")
	}
	if revoked(expiredInv) || !member(expiredUser) || jobs(expiredEmail) != 1 {
		t.Fatal("a manager's press changed something")
	}

	// ── revoke: confirmation first, then the one press ──────────────────────
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:ri:"+expiredUser.String()))
	confirm := tg.waitSince(t, m, "Отозвать приглашение для")
	if !strings.Contains(confirm, expiredEmail) || revoked(expiredInv) || !member(expiredUser) {
		t.Fatalf("the confirmation must change nothing: revoked=%v\n%s", revoked(expiredInv), confirm)
	}
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:ri:"+expiredUser.String()+":yes"))
	done := tg.waitSince(t, m, "Ссылка не работает, человека нет в команде")
	if !strings.Contains(done, expiredEmail) || !revoked(expiredInv) || member(expiredUser) {
		t.Fatalf("revoke: revoked=%v member=%v\n%s", revoked(expiredInv), member(expiredUser), done)
	}
	// The same button pressed again: a note, not an error.
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:ri:"+expiredUser.String()+":yes"))
	tg.waitSince(t, m, "Такого приглашения уже нет")

	// ── the accepted manager has no invitation controls ─────────────────────
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:ri:"+managerUser.String()+":yes"))
	tg.waitSince(t, m, "Такого приглашения уже нет")
	if !member(managerUser) {
		t.Fatal("an accepted member must stay")
	}
	// The pending one that is still waiting was never touched by any of it.
	if revoked(waitingInv) || !member(waitingUser) {
		t.Fatal("the waiting invitation must be untouched")
	}
	_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE resource_id = ANY($1)`, []string{waitingInv.String(), expiredInv.String()})
}

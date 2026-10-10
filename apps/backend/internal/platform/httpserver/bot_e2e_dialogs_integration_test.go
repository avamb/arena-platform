//go:build integration

package httpserver

// End-to-end proof of EC-01 (spec 35 §4.1): the bot's short dialogs live in
// bot_dialogs, so the team invite survives a bot restart between the e-mail
// and the role, and a dialog that ran out is reported once — not silently
// swallowed, and not repeated forever. Run against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run BotE2E_

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// startDialogTestBot runs a bot against the stub Telegram until stop is
// called; stop waits for Run to return.
func startDialogTestBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram) (stop func()) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bot.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bot did not stop after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

// linkDialogTestOwner binds a Telegram account to the fixture's owner through
// the real invitation route, as the bot's own deep link would.
func linkDialogTestOwner(t *testing.T, f *botInviteFixture, srv *Server, tgID int64) {
	t.Helper()
	ownerEmail := f.emails[0]
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"owner","locale":"ru"}`, ownerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite owner: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(ownerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, ownerEmail, tgID)); rec.Code != http.StatusOK {
		t.Fatalf("accept owner: %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() {
		// bot_dialogs rows go with the link (ON DELETE CASCADE).
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tgID)
	})
}

// The owner types a colleague's e-mail, the bot restarts (a deploy), and the
// role pressed afterwards still sends the invitation — the dialog was on disk.
func TestBotE2E_TeamInviteSurvivesBotRestart(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	const ownerTG = int64(781)
	linkDialogTestOwner(t, f, srv, ownerTG)

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)

	m := tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team"))
	tg.waitSince(t, m, "Сотрудники: ")
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:invite"))
	tg.waitSince(t, m, "E-mail коллеги")
	colleague := f.newEmail("restart")
	m = tg.mark()
	tg.push(e2eMessageAs(ownerTG, colleague))
	tg.waitSince(t, m, "Что может")

	var step string
	if err := pool.QueryRow(context.Background(),
		`SELECT step FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'team'`, ownerTG).Scan(&step); err != nil || step != "role" {
		t.Fatalf("the dialog must be on disk at the role step: step=%q err=%v", step, err)
	}

	// The restart. Once Run has returned, wait out the stub's 200 ms long
	// poll so an update pushed next cannot be handed to the dead poller.
	stop()
	time.Sleep(400 * time.Millisecond)
	startDialogTestBot(t, pool, api, tg)

	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:role:manager"))
	invited := tg.waitSince(t, m, "Приглашение отправлено")
	if !strings.Contains(invited, colleague) || !strings.Contains(invited, "менеджер") {
		t.Fatalf("after the restart the invitation must name the colleague and the role:\n%s", invited)
	}

	ctx := context.Background()
	var role, invID string
	if err := pool.QueryRow(ctx,
		`SELECT role, id::text FROM bot_invitations WHERE org_id = $1 AND email = $2 ORDER BY created_at DESC LIMIT 1`,
		f.orgID, colleague).Scan(&role, &invID); err != nil || role != "manager" {
		t.Fatalf("no manager invitation for %s after the restart: role=%q err=%v", colleague, role, err)
	}
	if code, _ := f.queuedCode(colleague); code == "" {
		t.Fatal("no invitation e-mail queued for the colleague")
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1`, ownerTG).Scan(&left); err != nil || left != 0 {
		t.Fatalf("a finished dialog must be gone: %d rows (%v)", left, err)
	}

	// The bot declares itself in X-Client-Channel, so the audit row of the
	// invitation names the Telegram bot as the client it came through.
	var via *string
	if err := pool.QueryRow(ctx,
		`SELECT metadata->>'via' FROM audit_events WHERE action = 'v1.bot.invitation.create' AND resource_id = $1 ORDER BY occurred_at DESC LIMIT 1`,
		invID).Scan(&via); err != nil {
		t.Fatalf("no audit row for the invitation: %v", err)
	}
	if via == nil || *via != "telegram_bot" {
		t.Fatalf("audit metadata.via = %v, want telegram_bot", via)
	}
}

// An expired dialog answers "time is up" once; the same text sent again is
// an ordinary stray message.
func TestBotE2E_DialogExpiresOnce(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	const ownerTG = int64(782)
	linkDialogTestOwner(t, f, srv, ownerTG)

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)

	m := tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:invite"))
	tg.waitSince(t, m, "E-mail коллеги")

	if _, err := pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 hour' WHERE telegram_user_id = $1 AND kind = 'team'`, ownerTG); err != nil {
		t.Fatalf("age the dialog: %v", err)
	}
	late := f.newEmail("late")

	m = tg.mark()
	tg.push(e2eMessageAs(ownerTG, late))
	tg.waitSince(t, m, "Время вышло")

	m = tg.mark()
	tg.push(e2eMessageAs(ownerTG, late))
	again := tg.waitSince(t, m, "Я не понял")
	if strings.Contains(again, "Время вышло") {
		t.Fatalf("the lapse must be reported once:\n%s", again)
	}
	var invitations int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM bot_invitations WHERE org_id = $1 AND email = $2`, f.orgID, late).Scan(&invitations); err != nil || invitations != 0 {
		t.Fatalf("an expired dialog must not invite anyone: %d (%v)", invitations, err)
	}
}

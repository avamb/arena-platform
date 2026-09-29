//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// The operator links their own (platform_superadmin) account to Telegram
// through an ordinary owner invitation and then works in EVERY organization
// of the platform: the chooser offers an organization they are not a member
// of, and "My events" / "Team" there go through arena-api's superadmin
// bypass — which only lets them in because the bot states X-Admin-Reason on
// every call (the live defect of 2026-09-29: 400 superadmin.missing_reason
// on every screen for the operator).
func TestBotE2E_SuperadminWorksInEveryOrganization(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := gen.New(pool)

	// An organization the operator is NOT a member of.
	suffix := uuid.NewString()[:8]
	foreign, err := q.InsertOrganization(ctx, "Foreign org "+suffix, "bot-foreign-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization foreign: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, foreign.ID)
	})

	// The operator is invited as owner of the fixture organization (the
	// only way to link a Telegram account) and holds the platform role.
	const saTG = int64(783)
	saEmail := f.newEmail("operator")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"owner","locale":"ru"}`, saEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite operator: %d %s", rec.Code, rec.Body.String())
	}
	code, payload := f.queuedCode(saEmail)
	saUserID := uuid.MustParse(payload.UserID)
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id, org_id)
		 SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, saUserID); err != nil {
		t.Fatalf("grant platform_superadmin: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, saTG)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, saUserID)
	})
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, saEmail, saTG)); rec.Code != http.StatusOK {
		t.Fatalf("accept operator: %d %s", rec.Code, rec.Body.String())
	}

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           q,
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	// 1. The menu opens in the invited organization; "My events" there works
	//    as for any owner.
	m := tg.mark()
	tg.push(e2eMessageAs(saTG, "/start"))
	tg.waitSince(t, m, "Что будем делать?")
	m = tg.mark()
	tg.push(e2eCallbackAs(saTG, "events:1"))
	tg.waitSince(t, m, "Ивентов пока нет")

	// 2. The foreign organization is offered and can be switched to, although
	//    the operator has no membership there.
	m = tg.mark()
	tg.push(e2eCallbackAs(saTG, "org:"+foreign.ID.String()))
	switched := tg.waitSince(t, m, "Переключились на")
	if !strings.Contains(switched, foreign.Name) {
		t.Fatalf("switch should land in the foreign organization:\n%s", switched)
	}
	link, err := q.GetBotTelegramLink(ctx, saTG)
	if err != nil || link.CurrentOrgID == nil || *link.CurrentOrgID != foreign.ID {
		t.Fatalf("current org after switch: %+v %v", link, err)
	}

	// 3. "My events" and "Team" in that organization go through the
	//    superadmin bypass (X-Admin-Reason is sent by the client).
	m = tg.mark()
	tg.push(e2eCallbackAs(saTG, "events:1"))
	tg.waitSince(t, m, "Ивентов пока нет")
	m = tg.mark()
	tg.push(e2eCallbackAs(saTG, "team"))
	team := tg.waitSince(t, m, "Сотрудники: ")
	if !strings.Contains(team, foreign.Name) {
		t.Fatalf("team screen should be the foreign organization's:\n%s", team)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bot.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bot did not stop after cancel")
	}
}

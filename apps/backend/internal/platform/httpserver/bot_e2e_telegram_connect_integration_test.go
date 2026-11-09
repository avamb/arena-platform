//go:build integration

package httpserver

// F-007 (acceptance run 2026-11-09): an organizer who works in the web event
// center connects their Telegram to the EXISTING organization with a one-time
// link, and a stranger's /start offers "I already have an organization"
// instead of only the application of a new one.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

type connectCall struct {
	t   *testing.T
	api *httptest.Server
}

func (c connectCall) do(method, path string, user uuid.UUID, body string, service bool) (int, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.api.URL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case service:
		req.Header.Set("Authorization", "Bearer "+botTestServiceToken)
	case user != uuid.Nil:
		token, _, err := auth.IssueJWT(botTestJWTSecret, user, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
		if err != nil {
			c.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.api.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (c connectCall) issue(orgID, user uuid.UUID) string {
	c.t.Helper()
	code, out := c.do(http.MethodPost, "/v1/organizations/"+orgID.String()+"/telegram-connect-codes", user, "", false)
	if code != http.StatusCreated {
		c.t.Fatalf("issue connect code: %d %v", code, out)
	}
	url, _ := out["url"].(string)
	start, _ := out["start_param"].(string)
	if !strings.HasPrefix(url, "https://t.me/ArenaEventsCentrBot?start=con_") || !strings.HasSuffix(url, start) || len(start) > 64 {
		c.t.Fatalf("connect link %q (start %q) is not a t.me deep link of the bot", url, start)
	}
	return strings.TrimPrefix(start, "con_")
}

func TestTelegramConnect_Routes(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx := context.Background()
	c := connectCall{t: t, api: api}
	q := gen.New(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM telegram_connect_codes WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE action LIKE 'v1.telegram_connect.%' AND resource_id = $1`, f.orgID.String())
	})
	const tgA, tgB = int64(790001), int64(790002)

	// A manager of the organization; a stranger of another organization.
	managerEmail := f.newEmail("connect-mgr")
	manager, err := q.InsertUser(ctx, managerEmail, "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertMembership(ctx, manager.ID, f.orgID, "organizer"); err != nil {
		t.Fatal(err)
	}
	stranger, err := q.InsertUser(ctx, f.newEmail("connect-stranger"), "x", "en")
	if err != nil {
		t.Fatal(err)
	}

	// Status before: not linked, the server knows the bot.
	code, out := c.do(http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/telegram-connect", f.ownerID, "", false)
	if code != http.StatusOK || out["linked"] != false || out["available"] != true || out["bot_username"] != "ArenaEventsCentrBot" {
		t.Fatalf("status before: %d %v", code, out)
	}
	// A person outside the organization gets nothing, the anonymous caller neither.
	if code, _ := c.do(http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/telegram-connect-codes", stranger.ID, "", false); code != http.StatusForbidden {
		t.Fatalf("stranger issue = %d, want 403", code)
	}
	if code, _ := c.do(http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/telegram-connect-codes", uuid.Nil, "", false); code != http.StatusUnauthorized {
		t.Fatalf("anonymous issue = %d, want 401", code)
	}

	// The owner issues two codes: the first is taken back by the second.
	first := c.issue(f.orgID, f.ownerID)
	second := c.issue(f.orgID, f.ownerID)
	body := func(code string, tg int64) string {
		return fmt.Sprintf(`{"code":%q,"telegram_user_id":%d,"telegram_username":"owner_tg","locale":"ru"}`, code, tg)
	}
	if code, out := c.do(http.MethodPost, "/v1/bot/telegram-connect/preview", uuid.Nil, body(first, tgA), true); code != http.StatusNotFound || connectErrCode(out) != "bot.connect_code_invalid" {
		t.Fatalf("replaced code preview: %d %v", code, out)
	}
	// The service routes need the bot's token.
	if code, _ := c.do(http.MethodPost, "/v1/bot/telegram-connect/accept", f.ownerID, body(second, tgA), false); code != http.StatusUnauthorized {
		t.Fatalf("accept without the service token = %d, want 401", code)
	}
	code, out = c.do(http.MethodPost, "/v1/bot/telegram-connect/preview", uuid.Nil, body("con_"+second, tgA), true)
	if code != http.StatusOK || out["state"] != "new" || out["org_id"] != f.orgID.String() {
		t.Fatalf("preview: %d %v", code, out)
	}
	if em, _ := out["email_masked"].(string); !strings.HasPrefix(em, "b•••@") || strings.Contains(em, "bot-owner") {
		t.Fatalf("email is not masked: %q", em)
	}
	// Preview used nothing: accept works, and only once.
	code, out = c.do(http.MethodPost, "/v1/bot/telegram-connect/accept", uuid.Nil, body(second, tgA), true)
	if code != http.StatusOK || out["role"] != "owner" || out["already_linked"] != false || out["locale"] != "ru" {
		t.Fatalf("accept: %d %v", code, out)
	}
	if code, out := c.do(http.MethodPost, "/v1/bot/telegram-connect/accept", uuid.Nil, body(second, tgB), true); code != http.StatusNotFound {
		t.Fatalf("second use of a code = %d %v, want 404", code, out)
	}
	link, err := q.GetBotTelegramLink(ctx, tgA)
	if err != nil || link.UserID != f.ownerID || link.CurrentOrgID == nil || *link.CurrentOrgID != f.orgID {
		t.Fatalf("link after accept: %+v %v", link, err)
	}
	code, out = c.do(http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/telegram-connect", f.ownerID, "", false)
	if accounts, _ := out["accounts"].([]any); code != http.StatusOK || out["linked"] != true || len(accounts) != 1 {
		t.Fatalf("status after: %d %v", code, out)
	}

	// The manager's code opened in the OWNER's Telegram: refused, nothing changes, the code still works.
	mgrCode := c.issue(f.orgID, manager.ID)
	if code, out := c.do(http.MethodPost, "/v1/bot/telegram-connect/preview", uuid.Nil, body(mgrCode, tgA), true); code != http.StatusOK || out["state"] != "other_user" {
		t.Fatalf("preview in a foreign Telegram: %d %v", code, out)
	}
	if code, out := c.do(http.MethodPost, "/v1/bot/telegram-connect/accept", uuid.Nil, body(mgrCode, tgA), true); code != http.StatusConflict || connectErrCode(out) != "bot.telegram_already_linked" {
		t.Fatalf("accept in a foreign Telegram: %d %v", code, out)
	}
	if link, _ := q.GetBotTelegramLink(ctx, tgA); link.UserID != f.ownerID {
		t.Fatalf("the owner's link moved to the manager: %+v", link)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = ANY($1)`, []int64{tgA, tgB})
	})
	if code, out := c.do(http.MethodPost, "/v1/bot/telegram-connect/accept", uuid.Nil, body(mgrCode, tgB), true); code != http.StatusOK || out["role"] != "manager" {
		t.Fatalf("manager accept in their own Telegram: %d %v", code, out)
	}

	// A code of a person who left the organization no longer works.
	leaver := c.issue(f.orgID, manager.ID)
	if _, err := pool.Exec(ctx, `UPDATE memberships SET status = 'revoked' WHERE user_id = $1 AND org_id = $2`, manager.ID, f.orgID); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.do(http.MethodPost, "/v1/bot/telegram-connect/preview", uuid.Nil, body(leaver, tgB), true); code != http.StatusNotFound {
		t.Fatalf("preview of a former member's code = %d, want 404", code)
	}

	// Both actions are audited.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action IN ('v1.telegram_connect.code_issue','v1.telegram_connect.accept')`, f.orgID.String()).Scan(&n); err != nil || n < 6 {
		t.Fatalf("audit rows = %d (%v), want the issues and the accepts", n, err)
	}
}

func connectErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// TestBotE2E_TelegramConnect drives the bot: a stranger's /start offers
// "I already have an organization", the connect link asks before it links,
// Cancel links nothing, and "Connect" opens the organization's menu.
func TestBotE2E_TelegramConnect(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := connectCall{t: t, api: api}
	const userTG = int64(777) // e2eMessage and e2eCallback speak as 777
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, userTG)
		_, _ = pool.Exec(context.Background(), `DELETE FROM telegram_connect_codes WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE action LIKE 'v1.telegram_connect.%' AND resource_id = $1`, f.orgID.String())
	})
	_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, userTG)

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	tg := newStubTelegram(t)
	tg.restartGap()
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		EventCenterURL:    "https://events.bot.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	// A stranger: the welcome names both ways in, and "I already have an
	// organization" explains where the button is.
	tg.push(e2eMessage("/start"))
	if !tg.waitButton(userTG, "con:how", 5*time.Second) {
		t.Fatal("the welcome screen has no con:how button")
	}
	m := tg.mark()
	tg.push(e2eCallback("con:how"))
	tg.waitSince(t, m, "Подключить Telegram")

	// The link from the web: a question first, Cancel links nothing.
	code := c.issue(f.orgID, f.ownerID)
	m = tg.mark()
	tg.push(e2eMessage("/start con_" + code))
	tg.waitSince(t, m, "Подключить этот Telegram к")
	tg.waitButton(userTG, "con:no", 5*time.Second)
	m = tg.mark()
	tg.push(e2eCallback("con:no"))
	tg.waitSince(t, m, "Ничего не подключено")
	if _, err := gen.New(pool).GetBotTelegramLink(ctx, userTG); err == nil {
		t.Fatal("Cancel linked the account")
	}

	// Opened again and confirmed: linked, and the organization's menu opens.
	m = tg.mark()
	tg.push(e2eMessage("/start con_" + code))
	tg.waitSince(t, m, "Подключить этот Telegram к")
	tg.waitButton(userTG, "con:ok:"+code, 5*time.Second)
	m = tg.mark()
	tg.push(e2eCallback("con:ok:" + code))
	tg.waitSince(t, m, "Готово! Этот Telegram подключён")
	tg.waitSince(t, m, "Что будем делать?")
	link, err := gen.New(pool).GetBotTelegramLink(ctx, userTG)
	if err != nil || link.UserID != f.ownerID || link.CurrentOrgID == nil || *link.CurrentOrgID != f.orgID {
		t.Fatalf("link after connect: %+v %v", link, err)
	}

	// The used link says so, and a fresh one only reopens the organization.
	m = tg.mark()
	tg.push(e2eMessage("/start con_" + code))
	tg.waitSince(t, m, "больше не работает")
	again := c.issue(f.orgID, f.ownerID)
	m = tg.mark()
	tg.push(e2eMessage("/start con_" + again))
	tg.waitSince(t, m, "уже подключён к вашему аккаунту")

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("bot did not stop after cancel")
	}
}

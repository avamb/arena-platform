//go:build integration

// bot_e2e_web_login_integration_test.go — "Open in browser" through the
// real bot, the real router and a stub Telegram (spec 08_architecture/37
// section 4.3): the button sends a one-time link, the web signs in with it,
// the bot reports the sign-in and "Sign out everywhere" ends the session.

package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const webLoginManagerTG = int64(7891)

func TestBotE2E_OpenInBrowserSignsInAndSignsOutEverywhere(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.EventCenterPublicURL = "https://events.bot.test"
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, webLoginManagerTG, "manager")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM web_login_codes WHERE telegram_user_id = $1`, webLoginManagerTG)
	})

	tg := newStubTelegram(t)
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		NoticeEvery:       200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = bot.Run(runCtx) }()
	d := ecDriver{t, tg, webLoginManagerTG}

	// The manager has the button in the main menu.
	d.say("/start", "Что будем делать?")
	if _, _, data := tg.lastOf(webLoginManagerTG); !hasPrefixData(data, "web:open") {
		t.Fatalf("the main menu has no Open in browser: %v", data)
	}

	// The button sends a one-time link with the code after "#".
	d.press("web:open", "работает один раз")
	id, _, data := tg.lastOf(webLoginManagerTG)
	if !hasPrefixData(data, "web:out") {
		t.Errorf("the link screen has no Sign out everywhere: %v", data)
	}
	urls := tg.urlsOf(id)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://events.bot.test/login/link#code=") {
		t.Fatalf("link buttons: %v", urls)
	}
	code := strings.TrimPrefix(urls[0], "https://events.bot.test/login/link#code=")

	// The web exchanges it for a session.
	m := tg.mark()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/v1/auth/web/link", strings.NewReader(fmt.Sprintf(`{"code":%q}`, code)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var signedIn map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&signedIn)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exchange: %d %v", resp.StatusCode, signedIn)
	}

	// The bot reports the sign-in, and its button ends the web session.
	tg.waitSince(t, m, "Вы вошли в веб-ивент-центр")
	d.press("web:out", "Все входы в веб-ивент-центр закрыты")
	var expired bool
	if err := pool.QueryRow(ctx, `SELECT expires_at <= now() FROM refresh_tokens WHERE token = $1`,
		users.TokenHash(signedIn["refresh_token"].(string))).Scan(&expired); err != nil || !expired {
		t.Fatalf("the web session is still open after Sign out everywhere: %v %v", expired, err)
	}
}

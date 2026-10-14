//go:build integration

// web_link_integration_test.go — the bot's "Open in browser" (spec
// 08_architecture/37 section 4.3, migration 0151): a one-time code under the
// bot service token, its exchange for a web session, the sign-in notice and
// "Sign out everywhere".

package httpserver

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const webLinkServiceToken = "web-link-service-token-0123456789"

func TestWebLink_BotCodeSignsInOnceAndSignOutEverywhere(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.BotServiceToken = webLinkServiceToken
		c.EventCenterPublicURL = "https://events.example.test"
		c.WebSessionIdleHours = 72
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()
	service := map[string]string{"Authorization": "Bearer " + webLinkServiceToken}

	nonce := uuid.NewString()[:8]
	hash, err := users.HashPassword("a long phrase 1")
	if err != nil {
		t.Fatal(err)
	}
	newUser := func(label string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
			label+"-"+nonce+"@example.test", hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	userID, adminID := newUser("web-link"), newUser("web-link-sa")
	tg, tgAdmin, tgNobody := 7_000_000_000+rand.Int64N(1_000_000_000), 8_000_000_000+rand.Int64N(1_000_000_000), 9_000_000_000+rand.Int64N(1_000_000_000)
	q := gen.New(pool)
	if _, err := q.UpsertBotTelegramLink(ctx, tg, userID, nil, "ru", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, tgAdmin, adminID, nil, "en", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{userID, adminID} {
			_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM web_login_codes WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		}
	})

	issue := func(telegramID int64) (int, map[string]any) {
		return c.do(http.MethodPost, "/v1/bot/web-login-codes", "", fmt.Sprintf(`{"telegram_user_id":%d}`, telegramID), service)
	}
	codeOf := func(out map[string]any) string {
		u, _ := out["url"].(string)
		const prefix = "https://events.example.test/login/link#code="
		if !strings.HasPrefix(u, prefix) {
			t.Fatalf("link %q does not start with %q", u, prefix)
		}
		return strings.TrimPrefix(u, prefix)
	}
	exchange := func(code string) (int, map[string]any) {
		return c.do(http.MethodPost, "/v1/auth/web/link", "", fmt.Sprintf(`{"code":%q}`, code), nil)
	}

	// The service token is required.
	if code, _ := c.do(http.MethodPost, "/v1/bot/web-login-codes", "", fmt.Sprintf(`{"telegram_user_id":%d}`, tg), nil); code != http.StatusUnauthorized {
		t.Fatalf("code without the service token: %d", code)
	}
	// An account linked to nobody, and a platform superadmin, get no link.
	if code, out := issue(tgNobody); code != http.StatusNotFound || errCode(out) != "bot.not_linked" {
		t.Fatalf("unlinked account: %d %v", code, out)
	}
	if code, out := issue(tgAdmin); code != http.StatusForbidden || errCode(out) != "bot.web_login_superadmin" {
		t.Fatalf("superadmin: %d %v", code, out)
	}

	// A new code takes the older one back; only its SHA-256 is stored.
	code1, out1 := issue(tg)
	if code1 != http.StatusCreated {
		t.Fatalf("first code: %d %v", code1, out1)
	}
	first := codeOf(out1)
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE code_hash = $1`, first).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the raw code is stored: %d %v", stored, err)
	}
	_, out2 := issue(tg)
	second := codeOf(out2)
	if code, out := exchange(first); code != http.StatusBadRequest || errCode(out) != "auth.web_link_invalid" {
		t.Fatalf("taken-back code: %d %v", code, out)
	}

	// The new code opens a WEB session, whatever header the page sends.
	code, signedIn := exchange(second)
	if code != http.StatusOK {
		t.Fatalf("exchange: %d %v", code, signedIn)
	}
	s := readTokenSession(t, pool, signedIn["refresh_token"].(string))
	if s.channel == nil || *s.channel != "event_center_web" || s.idle == nil || *s.idle != 72*3600 || !near(s.expiresAt, time.Now().Add(72*time.Hour)) {
		t.Fatalf("session of a bot link: %+v", s)
	}
	// It works once.
	if code, out := exchange(second); code != http.StatusBadRequest || errCode(out) != "auth.web_link_invalid" {
		t.Fatalf("second exchange: %d %v", code, out)
	}

	// The bot learns about the sign-in exactly once.
	claim := func() []any {
		code, out := c.do(http.MethodPost, "/v1/bot/web-login-notices/claim", "", `{}`, service)
		if code != http.StatusOK {
			t.Fatalf("claim: %d %v", code, out)
		}
		list, _ := out["notices"].([]any)
		var mine []any
		for _, n := range list {
			if m, _ := n.(map[string]any); m != nil && int64(m["telegram_user_id"].(float64)) == tg {
				mine = append(mine, n)
			}
		}
		return mine
	}
	if got := claim(); len(got) != 1 {
		t.Fatalf("notices after one sign-in: %v", got)
	}
	if got := claim(); len(got) != 0 {
		t.Fatalf("a notice came twice: %v", got)
	}

	// A code is void once the Telegram account is unlinked.
	_, out3 := issue(tg)
	third := codeOf(out3)
	if _, err := pool.Exec(ctx, `UPDATE bot_telegram_links SET revoked_at = now() WHERE telegram_user_id = $1`, tg); err != nil {
		t.Fatal(err)
	}
	if code, out := exchange(third); code != http.StatusBadRequest || errCode(out) != "auth.web_link_invalid" {
		t.Fatalf("code of an unlinked account: %d %v", code, out)
	}

	// "Sign out everywhere" ends the web sessions and keeps the others.
	code, plain := c.do(http.MethodPost, "/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":"a long phrase 1"}`, "web-link-"+nonce+"@example.test"), nil)
	if code != http.StatusOK {
		t.Fatalf("plain login: %d %v", code, plain)
	}
	code, revoked := c.do(http.MethodPost, "/v1/auth/web-sessions/revoke", signedIn["access_token"].(string), "", nil)
	if code != http.StatusOK || revoked["revoked_sessions"].(float64) < 1 {
		t.Fatalf("revoke: %d %v", code, revoked)
	}
	// The open web tab sends its token once more: it is refused as expired,
	// never taken for theft (which would end every session of the person).
	if code, out := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, signedIn["refresh_token"]), nil); code != http.StatusUnauthorized || errCode(out) != "auth.refresh_token_expired" {
		t.Fatalf("an ended web session: %d %v", code, out)
	}
	if code, out := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, plain["refresh_token"]), nil); code != http.StatusOK {
		t.Fatalf("a non-web session was ended too: %d %v", code, out)
	}
}

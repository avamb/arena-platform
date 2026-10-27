//go:build integration

// telegram_miniapp_next_integration_test.go — where the Telegram Mini App goes
// after it signed in (task S3b-next): the signed start_param or the page's own
// `next`, accepted only as an event-center path of the shape /o/<uuid>/...

package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hauth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

func TestTelegramMiniappLogin_NextPath(t *testing.T) {
	key := hauth.DeriveMiniappSecretKey(miniappTestBotToken)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.TelegramMiniappSecretKey = hex.EncodeToString(key)
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()
	const path = "/v1/auth/telegram-miniapp"

	hash, err := users.HashPassword("a long phrase 1")
	if err != nil {
		t.Fatal(err)
	}
	var person uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		"tgnext-"+uuid.NewString()[:8]+"@example.test", hash).Scan(&person); err != nil {
		t.Fatal(err)
	}
	tg := 9_000_000_000 + rand.Int64N(900_000_000)
	if _, err := gen.New(pool).UpsertBotTelegramLink(ctx, tg, person, nil, "ru", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, person)
		_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, person)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id = $1`, person)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, person)
	})

	org := uuid.NewString()
	seats := fmt.Sprintf("/o/%s/events/%s/sessions/%s/seats?embed=1", org, uuid.NewString(), uuid.NewString())
	other := fmt.Sprintf("/o/%s/events", org)
	now := time.Now()
	signed := func(startParam string) string {
		extra := map[string]string{}
		if startParam != "" {
			extra["start_param"] = startParam
		}
		return miniappInitDataWith(t, key, tg, now, extra)
	}
	login := func(initData, next string) string {
		t.Helper()
		body := fmt.Sprintf(`{"init_data":%q`, initData)
		if next != "" {
			body += fmt.Sprintf(`,"next":%q`, next)
		}
		body += "}"
		code, out := c.do(http.MethodPost, path, "", body, nil)
		if code != http.StatusOK {
			t.Fatalf("login: %d %v", code, out)
		}
		got, ok := out["next"].(string)
		if !ok {
			t.Fatalf("answer has no string next: %v", out)
		}
		return got
	}

	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	if got := login(signed(b64(seats)), ""); got != seats {
		t.Fatalf("signed start_param: next = %q, want %q", got, seats)
	}
	if got := login(signed(""), seats); got != seats {
		t.Fatalf("body next: next = %q, want %q", got, seats)
	}
	// The signed value wins over the body's.
	if got := login(signed(b64(other)), seats); got != other {
		t.Fatalf("start_param must win: next = %q, want %q", got, other)
	}
	// Nothing asked: an empty next, and the sign-in still works.
	if got := login(signed(""), ""); got != "" {
		t.Fatalf("no destination: next = %q, want empty", got)
	}
	// Open redirect attempts through either door are dropped, never followed.
	for _, bad := range []string{
		"https://evil.example/o/" + org,
		"//evil.example/o/" + org,
		"/login",
		"/o/" + org + "/../../admin",
		"/o/not-a-uuid/events",
	} {
		if got := login(signed(""), bad); got != "" {
			t.Fatalf("body next %q leaked as %q", bad, got)
		}
		if got := login(signed(b64(bad)), ""); got != "" {
			t.Fatalf("start_param %q leaked as %q", bad, got)
		}
	}
	// A bad start_param falls back to a good body path.
	if got := login(signed("!!not-base64!!"), other); got != other {
		t.Fatalf("bad start_param, good body: next = %q, want %q", got, other)
	}
	// A start_param changed after Telegram signed it breaks the signature.
	forged := miniappInitDataWith(t, hauth.DeriveMiniappSecretKey("1:other"), tg, now, map[string]string{"start_param": b64(seats)})
	code, out := c.do(http.MethodPost, path, "", miniappBody(forged), nil)
	if code != http.StatusUnauthorized || errCode(out) != "auth.telegram_init_data_invalid" {
		t.Fatalf("forged start_param: %d %v", code, out)
	}
}

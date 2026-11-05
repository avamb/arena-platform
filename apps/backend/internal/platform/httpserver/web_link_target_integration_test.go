//go:build integration

// web_link_target_integration_test.go — the optional `target` of the bot's
// "Open in browser" (spec 61 section 4.5): the shop path of an organization
// the person works in rides in the fragment as `&next=`, anything else is
// ignored without an error.

package httpserver

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

func TestWebLink_TargetAcceptedOnlyForOwnShopPath(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.BotServiceToken = webLinkServiceToken
		c.EventCenterPublicURL = "https://events.example.test"
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
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		"web-target-"+nonce+"@example.test", hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	mine, other, archived := uuid.New(), uuid.New(), uuid.New()
	for i, id := range []uuid.UUID{mine, other, archived} {
		if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			id, fmt.Sprintf("Web Target %d %s", i, nonce), fmt.Sprintf("web-target-%d-%s", i, nonce)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{mine, archived} {
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`, userID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, archived); err != nil {
		t.Fatal(err)
	}
	tg := 7_000_000_000 + rand.Int64N(1_000_000_000)
	if _, err := gen.New(pool).UpsertBotTelegramLink(ctx, tg, userID, nil, "en", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM web_login_codes WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{mine, other, archived})
	})

	const prefix = "https://events.example.test/login/link#code="
	linkFor := func(body string) string {
		t.Helper()
		code, out := c.do(http.MethodPost, "/v1/bot/web-login-codes", "", body, service)
		if code != http.StatusCreated {
			t.Fatalf("issue %s: %d %v", body, code, out)
		}
		u, _ := out["url"].(string)
		if !strings.HasPrefix(u, prefix) {
			t.Fatalf("link %q does not start with %q", u, prefix)
		}
		return strings.TrimPrefix(u, prefix)
	}
	withTarget := func(target string) string {
		return fmt.Sprintf(`{"telegram_user_id":%d,"target":%q}`, tg, target)
	}

	// An own organization's shop, and a deeper shop path, travel as next=.
	for _, target := range []string{"/o/" + mine.String() + "/shop", "/o/" + mine.String() + "/shop/orders/" + uuid.NewString()} {
		rest := linkFor(withTarget(target))
		code, next, ok := strings.Cut(rest, "&next=")
		if !ok || code == "" || strings.ContainsAny(code, "&=") {
			t.Fatalf("accepted target %q: fragment %q", target, rest)
		}
		got, err := url.QueryUnescape(next)
		if err != nil || got != target {
			t.Fatalf("next of %q = %q (%v)", target, got, err)
		}
		if strings.Contains(next, "/") {
			t.Fatalf("next is not url-encoded: %q", next)
		}
	}

	// Everything else is ignored: the link stays the plain one.
	for name, target := range map[string]string{
		"another organization": "/o/" + other.String() + "/shop",
		"archived":             "/o/" + archived.String() + "/shop",
		"unknown organization": "/o/" + uuid.NewString() + "/shop",
		"not the shop":         "/o/" + mine.String() + "/events",
		"absolute url":         "https://evil.example.test/o/" + mine.String() + "/shop",
		"traversal":            "/o/" + mine.String() + "/shop/../../x",
		"query":                "/o/" + mine.String() + "/shop?x=1",
		"empty":                "",
	} {
		rest := linkFor(withTarget(target))
		if strings.ContainsAny(rest, "&=") || rest == "" {
			t.Errorf("%s: target %q was not ignored: %q", name, target, rest)
		}
	}
	// No target at all.
	if rest := linkFor(fmt.Sprintf(`{"telegram_user_id":%d}`, tg)); strings.Contains(rest, "next") {
		t.Errorf("no target: %q", rest)
	}
}

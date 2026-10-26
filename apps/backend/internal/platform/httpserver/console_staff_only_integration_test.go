//go:build integration

// console_staff_only_integration_test.go - owner rule 2026-10-26 (UXH-08): the
// admin console (X-Client-Channel: admin_web) is for platform staff only. An
// organizer, including one with an old global row of a membership role, gets
// 403 auth.console_staff_only from sign-in and refresh and no token; staff and
// every other client (no header, the event center) are unaffected, and
// GET /v1/me reports platform_staff.

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

var consoleChannel = map[string]string{"X-Client-Channel": "admin_web"}

func TestConsoleStaffOnly_SignInAndRefresh(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()

	nonce := uuid.NewString()[:8]
	const password = "a long phrase 1"
	hash, err := users.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	orgID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Console Gate "+nonce, "console-gate-"+nonce); err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	person := func(name, memberRole, globalRole string) string {
		t.Helper()
		email := fmt.Sprintf("%s-%s@console-gate.example.test", name, nonce)
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`, email, hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if memberRole != "" {
			if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)`, id, orgID, memberRole); err != nil {
				t.Fatal(err)
			}
		}
		if globalRole != "" {
			if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
			        SELECT $1, id, NULL FROM roles WHERE name = $2 AND org_id IS NULL`, id, globalRole); err != nil {
				t.Fatal(err)
			}
		}
		return email
	}
	login := func(email string) string { return fmt.Sprintf(`{"email":%q,"password":%q}`, email, password) }

	organizer := person("owner", "org_admin", "")
	manager := person("manager", "organizer", "")
	oldStyle := person("old", "org_admin", "org_admin") // global row of a membership role
	staff := person("staff", "", "platform_superadmin")
	operator := person("ops", "org_admin", "support") // staff who also owns an organization

	// Sign-in from the console: only staff get a token.
	for _, tc := range []struct {
		name  string
		email string
		want  int
	}{
		{"organizer", organizer, http.StatusForbidden},
		{"manager", manager, http.StatusForbidden},
		{"old-style global org_admin", oldStyle, http.StatusForbidden},
		{"superadmin", staff, http.StatusOK},
		{"staff who is also an owner", operator, http.StatusOK},
	} {
		code, body := c.do(http.MethodPost, "/v1/auth/login", "", login(tc.email), consoleChannel)
		if code != tc.want {
			t.Fatalf("%s: console login %d %v, want %d", tc.name, code, body, tc.want)
		}
		if tc.want == http.StatusForbidden {
			errBody, _ := body["error"].(map[string]any)
			if errBody["code"] != "auth.console_staff_only" {
				t.Fatalf("%s: error code %v", tc.name, body)
			}
			if _, has := body["access_token"]; has {
				t.Fatalf("%s: a refused sign-in must issue nothing: %v", tc.name, body)
			}
		}
	}

	// The same organizer signs in anywhere else, and the header is optional.
	for name, headers := range map[string]map[string]string{
		"no header":    nil,
		"event center": webChannel,
		"telegram bot": {"X-Client-Channel": "telegram_bot"},
	} {
		if code, body := c.do(http.MethodPost, "/v1/auth/login", "", login(organizer), headers); code != http.StatusOK {
			t.Fatalf("organizer login (%s): %d %v", name, code, body)
		}
	}

	// A refresh token the organizer obtained elsewhere cannot mint a console
	// session; staff refresh through the console as before.
	_, org := c.do(http.MethodPost, "/v1/auth/login", "", login(organizer), nil)
	orgRefresh := fmt.Sprintf(`{"refresh_token":%q}`, org["refresh_token"])
	code, body := c.do(http.MethodPost, "/v1/auth/refresh", "", orgRefresh, consoleChannel)
	if code != http.StatusForbidden {
		t.Fatalf("organizer console refresh: %d %v", code, body)
	}
	if errBody, _ := body["error"].(map[string]any); errBody["code"] != "auth.console_staff_only" {
		t.Fatalf("organizer console refresh error: %v", body)
	}
	// The refusal did not consume the token: it still works for its own client.
	if code, body := c.do(http.MethodPost, "/v1/auth/refresh", "", orgRefresh, nil); code != http.StatusOK {
		t.Fatalf("organizer refresh without the header: %d %v", code, body)
	}

	_, st := c.do(http.MethodPost, "/v1/auth/login", "", login(staff), consoleChannel)
	stRefresh := fmt.Sprintf(`{"refresh_token":%q}`, st["refresh_token"])
	code, stBody := c.do(http.MethodPost, "/v1/auth/refresh", "", stRefresh, consoleChannel)
	if code != http.StatusOK {
		t.Fatalf("staff console refresh: %d %v", code, stBody)
	}

	// GET /v1/me tells the console who it is talking to.
	_, orgLogin := c.do(http.MethodPost, "/v1/auth/login", "", login(organizer), nil)
	code, me := c.do(http.MethodGet, "/v1/me", orgLogin["access_token"].(string), "", nil)
	if code != http.StatusOK || me["platform_staff"] != false {
		t.Fatalf("organizer /v1/me platform_staff: %d %v", code, me)
	}
	code, me = c.do(http.MethodGet, "/v1/me", stBody["access_token"].(string), "", nil)
	if code != http.StatusOK || me["platform_staff"] != true {
		t.Fatalf("staff /v1/me platform_staff: %d %v", code, me)
	}
}

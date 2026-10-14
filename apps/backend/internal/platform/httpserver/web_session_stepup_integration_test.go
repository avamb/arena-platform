//go:build integration

// web_session_stepup_integration_test.go — owner decisions 2026-10-14 for
// the web event center (spec 08_architecture/37 sections 4.4 and 4.5):
//
//   - a sign-in from the web (X-Client-Channel: event_center_web) lives 72
//     hours WITHOUT activity, each refresh slides the end, never past 30 days
//     from the sign-in; any other sign-in keeps the fixed 30-day token;
//   - money routes ask the web for the password again (POST /v1/auth/step-up
//     returns a short token for X-Step-Up-Token); other clients are not asked.

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

type webAuthClient struct {
	t  *testing.T
	ts *httptest.Server
}

func (c webAuthClient) do(method, path, bearer, body string, headers map[string]string) (int, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.ts.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

var webChannel = map[string]string{"X-Client-Channel": "event_center_web"}

type tokenSession struct {
	channel   *string
	idle      *int32
	started   *time.Time
	expiresAt time.Time
}

func readTokenSession(t *testing.T, pool *pgxpool.Pool, rawRefresh string) tokenSession {
	t.Helper()
	var s tokenSession
	if err := pool.QueryRow(context.Background(), `SELECT client_channel, idle_seconds, session_started_at, expires_at
	        FROM refresh_tokens WHERE token = $1`, users.TokenHash(rawRefresh)).Scan(&s.channel, &s.idle, &s.started, &s.expiresAt); err != nil {
		t.Fatalf("read refresh token: %v", err)
	}
	return s
}

func near(a, b time.Time) bool { d := a.Sub(b); return d < 2*time.Minute && d > -2*time.Minute }

func TestWebSession_IdleWindowAndStepUp(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.WebSessionIdleHours = 72 })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()

	nonce := uuid.NewString()[:8]
	email := "web-session-" + nonce + "@example.test"
	const password = "a long phrase 1"
	hash, err := users.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`, email, hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	orgID := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Web Session "+nonce, "web-session-"+nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'org_admin')`, userID, orgID); err != nil {
		t.Fatal(err)
	}
	login := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)

	// A sign-in from the web: 72 hours idle, started now.
	code, web := c.do(http.MethodPost, "/v1/auth/login", "", login, webChannel)
	if code != http.StatusOK {
		t.Fatalf("web login: %d %v", code, web)
	}
	webRefresh := web["refresh_token"].(string)
	s := readTokenSession(t, pool, webRefresh)
	if s.channel == nil || *s.channel != "event_center_web" || s.idle == nil || *s.idle != 72*3600 || s.started == nil {
		t.Fatalf("web session facts: %+v", s)
	}
	if !near(s.expiresAt, time.Now().Add(72*time.Hour)) {
		t.Fatalf("web token ends %v, want now + 72h", s.expiresAt)
	}

	// Any other sign-in keeps the fixed 30 days.
	code, admin := c.do(http.MethodPost, "/v1/auth/login", "", login, nil)
	if code != http.StatusOK {
		t.Fatalf("plain login: %d %v", code, admin)
	}
	a := readTokenSession(t, pool, admin["refresh_token"].(string))
	if a.idle != nil || !near(a.expiresAt, time.Now().Add(30*24*time.Hour)) {
		t.Fatalf("plain session: %+v", a)
	}

	// A refresh slides the window and carries the session facts...
	code, rotated := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, webRefresh), webChannel)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %v", code, rotated)
	}
	r := readTokenSession(t, pool, rotated["refresh_token"].(string))
	if r.idle == nil || *r.idle != 72*3600 || r.started == nil || !r.started.Equal(*s.started) || !near(r.expiresAt, time.Now().Add(72*time.Hour)) {
		t.Fatalf("rotated session: %+v (started %v)", r, s.started)
	}
	// ...but never past 30 days from the sign-in, even without the header.
	if _, err := pool.Exec(ctx, `UPDATE refresh_tokens SET session_started_at = now() - interval '30 days' + interval '1 hour'
	        WHERE token = $1`, users.TokenHash(rotated["refresh_token"].(string))); err != nil {
		t.Fatal(err)
	}
	code, capped := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, rotated["refresh_token"]), nil)
	if code != http.StatusOK {
		t.Fatalf("refresh near the cap: %d %v", code, capped)
	}
	if cp := readTokenSession(t, pool, capped["refresh_token"].(string)); !near(cp.expiresAt, time.Now().Add(time.Hour)) || cp.idle == nil {
		t.Fatalf("capped session: %+v, want the end one hour from now", cp)
	}
	access := capped["access_token"].(string)

	// Step-up: a wrong password is 403 (never 401: the access token is fine).
	code, wrong := c.do(http.MethodPost, "/v1/auth/step-up", access, `{"password":"not it"}`, webChannel)
	if code != http.StatusForbidden || errCode(wrong) != "auth.step_up_wrong_password" {
		t.Fatalf("wrong step-up: %d %v", code, wrong)
	}
	code, ok := c.do(http.MethodPost, "/v1/auth/step-up", access, fmt.Sprintf(`{"password":%q}`, password), webChannel)
	stepUp, _ := ok["step_up_token"].(string)
	if code != http.StatusOK || stepUp == "" {
		t.Fatalf("step-up: %d %v", code, ok)
	}
	// The step-up token is no access token.
	if code, _ := c.do(http.MethodGet, "/v1/me", stepUp, "", nil); code != http.StatusUnauthorized {
		t.Fatalf("step-up token used as access token: %d", code)
	}

	// A money route: the web without the confirmation is refused, with it the
	// request reaches the handler; other clients are not asked.
	path := "/v1/organizations/" + orgID.String() + "/orders/" + uuid.NewString() + "/refunds"
	code, refused := c.do(http.MethodPost, path, access, `{}`, webChannel)
	if code != http.StatusForbidden || errCode(refused) != "auth.step_up_required" {
		t.Fatalf("web refund without step-up: %d %v", code, refused)
	}
	withToken := map[string]string{"X-Client-Channel": "event_center_web", "X-Step-Up-Token": stepUp}
	if code, out := c.do(http.MethodPost, path, access, `{}`, withToken); errCode(out) == "auth.step_up_required" || code == http.StatusForbidden {
		t.Fatalf("web refund with step-up stopped at the guard: %d %v", code, out)
	}
	if code, out := c.do(http.MethodPost, path, access, `{}`, nil); errCode(out) == "auth.step_up_required" {
		t.Fatalf("a non-web client was asked for step-up: %d %v", code, out)
	}
}

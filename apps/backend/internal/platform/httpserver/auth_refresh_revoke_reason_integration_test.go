//go:build integration

// auth_refresh_revoke_reason_integration_test.go checks that POST
// /v1/auth/refresh tells a token replaced by rotation from a token revoked by
// a sign-out or a revoke-all sweep (migration 0214, refresh_tokens.revoke_reason):
// only reuse of a rotated token revokes every session of the person.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/redissession"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

type revokeReasonHarness struct {
	t        *testing.T
	ts       *httptest.Server
	pool     *pgxpool.Pool
	email    string
	password string
}

// revokeReasonServer builds a server like productionIntegrationServer with a
// pool large enough for the concurrent requests below and, when store is
// not nil, a session store with a concurrent-session limit.
func revokeReasonServer(t *testing.T, store redissession.Store, maxSessions int) *Server {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	const secret = "integration-test-secret-32-bytes!!"
	const issuer = "arena-api"
	const audience = "arena-api"

	poolCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	poolCfg.MaxConns = 24
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:     secret,
		Issuer:     issuer,
		Audience:   audience,
		DefaultTTL: time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	verifier, err := auth.NewJWTVerifier(secret, issuer, audience)
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}
	opts := Options{
		Config: &config.Config{
			HTTPListenAddr: ":0",
			RequestTimeout: 30 * time.Second,
			BodyLimitBytes: 1 << 20,
			JWTSecretStub:  secret,
			JWTIssuer:      issuer,
			JWTAudience:    audience,
			JWTDefaultTTL:  time.Hour,
			EnableStubAuth: true,
			AppEnv:         config.EnvDevelopment,
			DefaultLocale:  "en",
		},
		Pool:     pool,
		PgxPool:  pool,
		Auth:     stub,
		Verifier: verifier,
	}
	if store != nil {
		opts.SessionStore = store
		opts.MaxConcurrentSessionsPerUser = maxSessions
	}
	srv := New(opts)
	t.Cleanup(func() { _ = srv.closeAnalytics(context.Background()) })
	return srv
}

func newRevokeReasonHarness(t *testing.T) *revokeReasonHarness {
	t.Helper()
	return newRevokeReasonHarnessOn(t, revokeReasonServer(t, nil, 0))
}

// newRevokeReasonHarnessOn registers a new person on srv and removes the
// person with the test.
func newRevokeReasonHarnessOn(t *testing.T, srv *Server) *revokeReasonHarness {
	t.Helper()
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	h := &revokeReasonHarness{
		t:        t,
		ts:       ts,
		pool:     pool,
		email:    fmt.Sprintf("revoke-reason-%d@example.com", time.Now().UnixNano()),
		password: "Test1234!",
	}
	regBody := fmt.Sprintf(`{"email":%q,"password":%q,"first_name":"Test","last_name":"User"}`, h.email, h.password)
	resp := integDoRequest(t, ts.Client(), http.MethodPost, ts.URL+"/v1/auth/register", "", regBody)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("register: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	}
	resp.Body.Close()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`,
			`DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`,
			`DELETE FROM users WHERE email = $1`,
		} {
			if _, err := pool.Exec(ctx, stmt, h.email); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	return h
}

// userID returns the id of the harness person.
func (h *revokeReasonHarness) userID() uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	if err := h.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, h.email).Scan(&id); err != nil {
		h.t.Fatalf("read user id: %v", err)
	}
	return id
}

// liveTokens counts the person's refresh tokens that are not revoked.
func (h *revokeReasonHarness) liveTokens() int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens
		 WHERE user_id = (SELECT id FROM users WHERE email = $1) AND revoked_at IS NULL`,
		h.email).Scan(&n); err != nil {
		h.t.Fatalf("count live tokens: %v", err)
	}
	return n
}

// rawResult is the outcome of a request sent from a goroutine.
type rawResult struct {
	status int
	body   string
	next   string
	err    error
}

// rawPost sends a request without the testing.T, so it is safe in a
// goroutine. On a 200 refresh, next is the new refresh token.
func rawPost(client *http.Client, url, bearer, body string) rawResult {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return rawResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return rawResult{err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return rawResult{status: resp.StatusCode, err: err}
	}
	res := rawResult{status: resp.StatusCode, body: string(raw)}
	if resp.StatusCode == http.StatusOK && len(raw) > 0 {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			res.next, _ = m["refresh_token"].(string)
		}
	}
	return res
}

// holdTokenRow locks the token's row in a transaction of its own, so the
// UPDATEs of the requests under test queue up behind it. The returned
// function releases the lock without changing the row.
func (h *revokeReasonHarness) holdTokenRow(token string) func() {
	h.t.Helper()
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.t.Fatalf("begin lock tx: %v", err)
	}
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM refresh_tokens WHERE token = $1 FOR UPDATE`, users.TokenHash(token)).Scan(&one); err != nil {
		_ = tx.Rollback(ctx)
		h.t.Fatalf("lock token row: %v", err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = tx.Rollback(ctx) }) }
	h.t.Cleanup(release)
	return release
}

// waitLockWaiters waits until want statements named query wait for a row
// lock and reports whether that happened in time.
func (h *revokeReasonHarness) waitLockWaiters(query string, want int) bool {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := h.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			 WHERE datname = current_database() AND pid <> pg_backend_pid()
			   AND wait_event_type = 'Lock' AND query LIKE $1`,
			"%name: "+query+" :%").Scan(&n); err != nil {
			h.t.Logf("read pg_stat_activity: %v", err)
			return false
		}
		if n >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// login returns the access and refresh token of a new sign-in.
func (h *revokeReasonHarness) login() (string, string) {
	h.t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, h.email, h.password)
	resp := integDoRequest(h.t, h.ts.Client(), http.MethodPost, h.ts.URL+"/v1/auth/login", "", body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("login: got %d, body: %s", resp.StatusCode, integReadBody(h.t, resp))
	}
	data := integDecodeMap(h.t, resp)
	access, _ := data["access_token"].(string)
	refresh, _ := data["refresh_token"].(string)
	if access == "" || refresh == "" {
		h.t.Fatalf("login: empty tokens in %v", data)
	}
	return access, refresh
}

// refresh posts the token and returns the status, the body and, on 200, the
// new refresh token.
func (h *revokeReasonHarness) refresh(token string) (int, string, string) {
	h.t.Helper()
	resp := integDoRequest(h.t, h.ts.Client(), http.MethodPost, h.ts.URL+"/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, token))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, integReadBody(h.t, resp), ""
	}
	data := integDecodeMap(h.t, resp)
	next, _ := data["refresh_token"].(string)
	if next == "" {
		h.t.Fatalf("refresh: empty refresh_token in %v", data)
	}
	return http.StatusOK, "", next
}

func (h *revokeReasonHarness) logout(access, token string) {
	h.t.Helper()
	resp := integDoRequest(h.t, h.ts.Client(), http.MethodPost, h.ts.URL+"/v1/auth/logout", access, fmt.Sprintf(`{"refresh_token":%q}`, token))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		h.t.Fatalf("logout: got %d, body: %s", resp.StatusCode, integReadBody(h.t, resp))
	}
	resp.Body.Close()
}

// revoked reports whether the token row is revoked and its revoke reason.
func (h *revokeReasonHarness) revoked(token string) (bool, string) {
	h.t.Helper()
	var revoked bool
	var reason *string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL, revoke_reason FROM refresh_tokens WHERE token = $1`,
		users.TokenHash(token)).Scan(&revoked, &reason); err != nil {
		h.t.Fatalf("read refresh token: %v", err)
	}
	if reason == nil {
		return revoked, ""
	}
	return revoked, *reason
}

func expectRefreshError(t *testing.T, step string, code int, body, wantCode string) {
	t.Helper()
	if code != http.StatusUnauthorized {
		t.Fatalf("%s: got %d, want 401, body: %s", step, code, body)
	}
	if !strings.Contains(body, `"`+wantCode+`"`) {
		t.Fatalf("%s: want %s, body: %s", step, wantCode, body)
	}
}

// A token revoked by a sign-out answers auth.invalid_refresh_token and the
// other sign-in of the person keeps working.
func TestRefreshRevokeReason_SignedOutTokenIsPlain401(t *testing.T) {
	h := newRevokeReasonHarness(t)
	access1, refresh1 := h.login()
	_, refresh2 := h.login()

	h.logout(access1, refresh1)
	if revoked, reason := h.revoked(refresh1); !revoked || reason != "signed_out" {
		t.Fatalf("after logout: revoked=%v reason=%q, want true signed_out", revoked, reason)
	}

	code, body, _ := h.refresh(refresh1)
	expectRefreshError(t, "refresh with signed-out token", code, body, "auth.invalid_refresh_token")

	if revoked, _ := h.revoked(refresh2); revoked {
		t.Fatal("the second sign-in was revoked by a signed-out token")
	}
	if code, body, _ := h.refresh(refresh2); code != http.StatusOK {
		t.Fatalf("refresh of the second sign-in: got %d, body: %s", code, body)
	}
}

// Reuse of a rotated token revokes every session of the person; a token
// revoked by that sweep then answers auth.invalid_refresh_token and a later
// sign-in still refreshes.
func TestRefreshRevokeReason_RotatedTokenReuseIsCompromise(t *testing.T) {
	h := newRevokeReasonHarness(t)
	_, t1 := h.login()
	_, other := h.login()

	code, body, t2 := h.refresh(t1)
	if code != http.StatusOK {
		t.Fatalf("rotate T1: got %d, body: %s", code, body)
	}
	if revoked, reason := h.revoked(t1); !revoked || reason != "rotated" {
		t.Fatalf("after rotation: revoked=%v reason=%q, want true rotated", revoked, reason)
	}

	code, body, _ = h.refresh(t1)
	expectRefreshError(t, "reuse of rotated T1", code, body, "auth.session_compromised")

	if revoked, reason := h.revoked(other); !revoked || reason != "revoked_all" {
		t.Fatalf("second sign-in after compromise: revoked=%v reason=%q, want true revoked_all", revoked, reason)
	}
	if revoked, reason := h.revoked(t2); !revoked || reason != "revoked_all" {
		t.Fatalf("T2 after compromise: revoked=%v reason=%q, want true revoked_all", revoked, reason)
	}

	// The new sign-in comes before the reuse of T2, so a sweep there would
	// revoke it.
	_, fresh := h.login()

	code, body, _ = h.refresh(t2)
	expectRefreshError(t, "reuse of swept T2", code, body, "auth.invalid_refresh_token")

	if code, body, _ := h.refresh(fresh); code != http.StatusOK {
		t.Fatalf("refresh of a sign-in made after the sweep: got %d, body: %s", code, body)
	}
}

// Several refreshes of ONE token that all read it live: exactly one rotates
// it, the others get a plain 401 and the other sign-ins stay untouched.
func TestRefreshRevokeReason_ConcurrentRefreshRotatesOnce(t *testing.T) {
	const n = 8
	h := newRevokeReasonHarness(t)
	_, t1 := h.login()
	_, other := h.login()

	// The row lock makes every request read the token live and then wait at
	// the rotation, so the requests really overlap.
	release := h.holdTokenRow(t1)
	results := make([]rawResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = rawPost(h.ts.Client(), h.ts.URL+"/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, t1))
		}(i)
	}
	waited := h.waitLockWaiters("RotateRefreshToken", n)
	release()
	wg.Wait()
	if !waited {
		t.Fatalf("the %d refreshes never queued at the rotation", n)
	}

	var successor string
	ok := 0
	for i, res := range results {
		if res.err != nil {
			t.Fatalf("request %d: %v", i, res.err)
		}
		switch res.status {
		case http.StatusOK:
			ok++
			successor = res.next
		case http.StatusUnauthorized:
			if !strings.Contains(res.body, `"auth.invalid_refresh_token"`) {
				t.Errorf("request %d: want auth.invalid_refresh_token, body: %s", i, res.body)
			}
		default:
			t.Errorf("request %d: got %d, body: %s", i, res.status, res.body)
		}
	}
	if ok != 1 {
		t.Fatalf("successful refreshes = %d, want exactly 1", ok)
	}
	if revoked, reason := h.revoked(t1); !revoked || reason != "rotated" {
		t.Fatalf("T1: revoked=%v reason=%q, want true rotated", revoked, reason)
	}
	if revoked, _ := h.revoked(successor); revoked {
		t.Fatal("the successor is revoked")
	}
	if revoked, _ := h.revoked(other); revoked {
		t.Fatal("the other sign-in was revoked by the race")
	}
	if live := h.liveTokens(); live != 2 {
		t.Fatalf("live tokens = %d, want 2 (one successor and the other sign-in)", live)
	}
}

// A refresh racing a sign-out of the same token never leaves both a new
// token and a sign-out: either the token was rotated and has a successor, or
// it was signed out and the refresh got a plain 401. Some rounds fix the
// order at the token row (sign-out first, refresh first), the rest run free.
func TestRefreshRevokeReason_RefreshRacingLogout(t *testing.T) {
	const (
		logoutFirst = iota
		refreshFirst
		free
	)
	h := newRevokeReasonHarness(t)
	for i := 0; i < 9; i++ {
		mode := i % 3
		access, tok := h.login()
		liveBefore := h.liveTokens()

		var release func()
		if mode != free {
			release = h.holdTokenRow(tok)
		}
		var refreshRes, logoutRes rawResult
		var wg sync.WaitGroup
		startRefresh := func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				refreshRes = rawPost(h.ts.Client(), h.ts.URL+"/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, tok))
			}()
		}
		startLogout := func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				logoutRes = rawPost(h.ts.Client(), h.ts.URL+"/v1/auth/logout", access, fmt.Sprintf(`{"refresh_token":%q}`, tok))
			}()
		}
		waited := true
		switch mode {
		case logoutFirst:
			startLogout()
			waited = h.waitLockWaiters("RevokeRefreshToken", 1)
			startRefresh()
			waited = waited && h.waitLockWaiters("RotateRefreshToken", 1)
			release()
		case refreshFirst:
			startRefresh()
			waited = h.waitLockWaiters("RotateRefreshToken", 1)
			startLogout()
			waited = waited && h.waitLockWaiters("RevokeRefreshToken", 1)
			release()
		default:
			startRefresh()
			startLogout()
		}
		wg.Wait()
		if !waited {
			t.Fatalf("round %d: the refresh and the sign-out never queued at the token row", i)
		}
		if mode == logoutFirst && refreshRes.status != http.StatusUnauthorized {
			t.Fatalf("round %d: the sign-out took the row first but the refresh got %d, body: %s", i, refreshRes.status, refreshRes.body)
		}
		if mode == refreshFirst && refreshRes.status != http.StatusOK {
			t.Fatalf("round %d: the refresh took the row first but got %d, body: %s", i, refreshRes.status, refreshRes.body)
		}
		if refreshRes.err != nil || logoutRes.err != nil {
			t.Fatalf("round %d: refresh err %v, logout err %v", i, refreshRes.err, logoutRes.err)
		}
		if logoutRes.status != http.StatusNoContent && logoutRes.status != http.StatusOK {
			t.Fatalf("round %d: logout got %d, body: %s", i, logoutRes.status, logoutRes.body)
		}

		revoked, reason := h.revoked(tok)
		live := h.liveTokens()
		switch refreshRes.status {
		case http.StatusOK:
			if !revoked || reason != "rotated" {
				t.Fatalf("round %d: refresh won but token revoked=%v reason=%q, want rotated", i, revoked, reason)
			}
			if r, _ := h.revoked(refreshRes.next); r {
				t.Fatalf("round %d: the successor is revoked", i)
			}
			if live != liveBefore {
				t.Fatalf("round %d: live tokens %d, want %d", i, live, liveBefore)
			}
		case http.StatusUnauthorized:
			if !strings.Contains(refreshRes.body, `"auth.invalid_refresh_token"`) {
				t.Fatalf("round %d: want auth.invalid_refresh_token, body: %s", i, refreshRes.body)
			}
			if !revoked || reason != "signed_out" {
				t.Fatalf("round %d: sign-out won but token revoked=%v reason=%q, want signed_out", i, revoked, reason)
			}
			if live != liveBefore-1 {
				t.Fatalf("round %d: live tokens %d, want %d (no successor)", i, live, liveBefore-1)
			}
		default:
			t.Fatalf("round %d: refresh got %d, body: %s", i, refreshRes.status, refreshRes.body)
		}
	}
}

// A sign-in above the concurrent-session limit revokes the oldest token in
// the database, and that token then answers a plain 401.
func TestRefreshRevokeReason_EvictedSessionIsSignedOut(t *testing.T) {
	const maxSessions = 2
	h := newRevokeReasonHarnessOn(t, revokeReasonServer(t, redissession.NewMemStore(), maxSessions))
	_, t1 := h.login()
	time.Sleep(10 * time.Millisecond)
	_, t2 := h.login()
	time.Sleep(10 * time.Millisecond)
	_, t3 := h.login()

	if revoked, reason := h.revoked(t1); !revoked || reason != "signed_out" {
		t.Fatalf("oldest token after the limit: revoked=%v reason=%q, want true signed_out", revoked, reason)
	}
	for name, tok := range map[string]string{"T2": t2, "T3": t3} {
		if revoked, _ := h.revoked(tok); revoked {
			t.Fatalf("%s was revoked by the eviction", name)
		}
	}

	code, body, _ := h.refresh(t1)
	expectRefreshError(t, "refresh of the evicted token", code, body, "auth.invalid_refresh_token")

	if revoked, _ := h.revoked(t2); revoked {
		t.Fatal("T2 was revoked by the refresh of the evicted token")
	}
}

// A token revoked before migration 0214 (no reason) is handled like a
// rotated one: the person's other sessions are ended.
func TestRefreshRevokeReason_UnknownReasonIsCompromise(t *testing.T) {
	h := newRevokeReasonHarness(t)
	_, t1 := h.login()
	_, other := h.login()

	if _, err := h.pool.Exec(context.Background(),
		`UPDATE refresh_tokens SET revoked_at = now(), revoke_reason = NULL WHERE token = $1`,
		users.TokenHash(t1)); err != nil {
		t.Fatalf("revoke T1 without a reason: %v", err)
	}

	code, body, _ := h.refresh(t1)
	expectRefreshError(t, "refresh of a token revoked without a reason", code, body, "auth.session_compromised")

	if revoked, reason := h.revoked(other); !revoked || reason != "revoked_all" {
		t.Fatalf("other sign-in: revoked=%v reason=%q, want true revoked_all", revoked, reason)
	}
}

// A password reset revokes the old tokens as revoked_all; such a token
// answers a plain 401 and ends no later sign-in.
func TestRefreshRevokeReason_PasswordResetTokensArePlain401(t *testing.T) {
	h := newRevokeReasonHarness(t)
	_, t1 := h.login()
	_, t2 := h.login()

	raw, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	if err := gen.New(h.pool).InsertPasswordResetToken(context.Background(), users.TokenHash(raw), h.userID(), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("InsertPasswordResetToken: %v", err)
	}
	const newPassword = "NewPassword1!"
	resp := integDoRequest(t, h.ts.Client(), http.MethodPost, h.ts.URL+"/v1/auth/password-reset/confirm", "",
		fmt.Sprintf(`{"token":%q,"new_password":%q}`, raw, newPassword))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password reset confirm: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	}
	resp.Body.Close()
	h.password = newPassword

	for name, tok := range map[string]string{"T1": t1, "T2": t2} {
		if revoked, reason := h.revoked(tok); !revoked || reason != "revoked_all" {
			t.Fatalf("%s after the reset: revoked=%v reason=%q, want true revoked_all", name, revoked, reason)
		}
	}

	_, fresh := h.login()

	code, body, _ := h.refresh(t1)
	expectRefreshError(t, "refresh with a token from before the reset", code, body, "auth.invalid_refresh_token")

	if revoked, reason := h.revoked(t2); !revoked || reason != "revoked_all" {
		t.Fatalf("T2 after the refresh: revoked=%v reason=%q, want unchanged revoked_all", revoked, reason)
	}
	if revoked, _ := h.revoked(fresh); revoked {
		t.Fatal("the sign-in made after the reset was revoked")
	}
	if code, body, _ := h.refresh(fresh); code != http.StatusOK {
		t.Fatalf("refresh of the sign-in made after the reset: got %d, body: %s", code, body)
	}
}

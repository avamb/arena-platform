//go:build integration

// auth_refresh_revoke_reason_integration_test.go checks that POST
// /v1/auth/refresh tells a token replaced by rotation from a token revoked by
// a sign-out or a revoke-all sweep (migration 0214, refresh_tokens.revoke_reason):
// only reuse of a rotated token revokes every session of the person.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

type revokeReasonHarness struct {
	t        *testing.T
	ts       *httptest.Server
	pool     *pgxpool.Pool
	email    string
	password string
}

func newRevokeReasonHarness(t *testing.T) *revokeReasonHarness {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
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
		_, _ = pool.Exec(context.Background(), `DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, h.email)
	})
	return h
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

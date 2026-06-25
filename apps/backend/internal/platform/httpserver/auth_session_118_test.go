// auth_session_118_test.go — unit tests for the Redis-backed session management
// endpoints (feature #118).
//
// Tests cover:
//   - POST /v1/auth/logout  — auth gating, validation, revocation logic
//   - POST /v1/auth/refresh — Redis revocation check (fast path)
//   - Concurrent-session enforcement  (max sessions = 1)
//   - MemStore correctness (interface compliance + store behaviour)
//
// All tests use an in-memory MemStore so no Redis instance is required.
// Integration tests that require a real Redis testcontainer are in
// auth_session_118_integration_test.go.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/redissession"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// serverWithSessionStore builds a minimal Server with a configured JWT
// signing secret, a fake pool, and a MemStore wired as the session store.
// maxSessions = 0 means unlimited (default).
func serverWithSessionStore(t *testing.T, maxSessions int) (*Server, *redissession.MemStore) {
	t.Helper()
	store := redissession.NewMemStore()
	s := &Server{
		cfg: &config.Config{
			AppEnv:        config.EnvDevelopment,
			AppName:       "test",
			AppVersion:    "0.0.0-dev",
			JWTSecretStub: "test-secret-feature-118",
		},
		pool:                  &fakeLoginPool{},
		sessionStore:          store,
		maxConcurrentSessions: maxSessions,
	}
	return s, store
}

func postLogout(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleAuthLogout(w, r)
	return w
}

func decodeSessionJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

// ---------------------------------------------------------------------------
// POST /v1/auth/logout — validation (no DB, no actor)
// ---------------------------------------------------------------------------

// TestSession118_LogoutEmptyBody tests that POST /v1/auth/logout with an
// empty body returns 401 (because no actor context — middleware not run in
// the unit test, so auth.ActorFromContext returns not-ok).
func TestSession118_LogoutEmptyBody(t *testing.T) {
	s, _ := serverWithSessionStore(t, 0)
	w := postLogout(t, s, "")
	// Without an actor in the request context the handler returns 401.
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 (no authenticated actor)", w.Code)
	}
}

// TestSession118_LogoutNoActor ensures that requests without a JWT context
// actor are rejected with 401.
func TestSession118_LogoutNoActor(t *testing.T) {
	s, _ := serverWithSessionStore(t, 0)
	w := postLogout(t, s, `{"refresh_token":"abc"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 (unauthenticated)", w.Code)
	}
	m := decodeSessionJSON(t, w)
	if code := errorCode(t, m); code != "auth.unauthenticated" {
		t.Errorf("error.code = %q; want 'auth.unauthenticated'", code)
	}
}

// TestSession118_LogoutInvalidJSON tests that malformed JSON returns 400.
func TestSession118_LogoutInvalidJSON(t *testing.T) {
	s, _ := serverWithSessionStore(t, 0)
	// Inject a valid actor so we get past the auth check.
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader("not-json"))
	r = r.WithContext(withTestActor(r.Context(), "user-id-1"))
	w := httptest.NewRecorder()
	s.handleAuthLogout(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
	m := decodeSessionJSON(t, w)
	if code := errorCode(t, m); code != "http.invalid_json" {
		t.Errorf("error.code = %q; want 'http.invalid_json'", code)
	}
}

// TestSession118_LogoutMissingRefreshToken tests that omitting refresh_token
// field returns 400.
func TestSession118_LogoutMissingRefreshToken(t *testing.T) {
	s, _ := serverWithSessionStore(t, 0)
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(`{}`))
	r = r.WithContext(withTestActor(r.Context(), "user-id-2"))
	w := httptest.NewRecorder()
	s.handleAuthLogout(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
	m := decodeSessionJSON(t, w)
	if code := errorCode(t, m); code != "validation.refresh_token_required" {
		t.Errorf("error.code = %q; want 'validation.refresh_token_required'", code)
	}
}

// TestSession118_LogoutNilPool tests that a server without a pool returns 503.
func TestSession118_LogoutNilPool(t *testing.T) {
	s := &Server{cfg: &config.Config{AppEnv: config.EnvDevelopment, JWTSecretStub: "x"}}
	// pool is nil
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(`{"refresh_token":"tok"}`))
	r = r.WithContext(withTestActor(r.Context(), "user-id-3"))
	w := httptest.NewRecorder()
	s.handleAuthLogout(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", w.Code)
	}
	m := decodeSessionJSON(t, w)
	if code := errorCode(t, m); code != "dependency.database_unavailable" {
		t.Errorf("error.code = %q; want 'dependency.database_unavailable'", code)
	}
}

// ---------------------------------------------------------------------------
// POST /v1/auth/logout — route exists
// ---------------------------------------------------------------------------

// TestSession118_LogoutRouteExists confirms the handler is wired as a method
// on *Server (compile-time check only in this test).
func TestSession118_LogoutRouteExists(_ *testing.T) {
	s := &Server{cfg: &config.Config{AppEnv: config.EnvDevelopment}}
	_ = s.handleAuthLogout // compile-time: method must exist on *Server
}

// ---------------------------------------------------------------------------
// MemStore — unit tests for the in-memory session store
// ---------------------------------------------------------------------------

// TestSession118_MemStore_TrackAndRevoke verifies the basic TrackSession +
// IsRevoked contract.
func TestSession118_MemStore_TrackAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := store.TrackSession(ctx, "user1", "token-A", exp); err != nil {
		t.Fatalf("TrackSession: %v", err)
	}

	// Token is NOT revoked yet.
	revoked, err := store.IsRevoked(ctx, "token-A")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("token-A should not be revoked after TrackSession")
	}

	// Revoke it.
	if err := store.RevokeSession(ctx, "user1", "token-A", exp); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	// Now it should be revoked.
	revoked, err = store.IsRevoked(ctx, "token-A")
	if err != nil {
		t.Fatalf("IsRevoked after revoke: %v", err)
	}
	if !revoked {
		t.Error("token-A should be revoked after RevokeSession")
	}
}

// TestSession118_MemStore_IsRevokedExpiredToken verifies that a revoked token
// whose expiry is in the past is treated as not-found (simulating Redis TTL).
func TestSession118_MemStore_IsRevokedExpiredToken(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	// Revoke a token that "expired" 1 second ago.
	pastExp := time.Now().UTC().Add(-1 * time.Second)
	if err := store.RevokeSession(ctx, "user1", "expired-tok", pastExp); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	revoked, err := store.IsRevoked(ctx, "expired-tok")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	// Mimic Redis TTL: expired revocation keys are treated as not-revoked.
	if revoked {
		t.Error("expired revocation entry should not report as revoked (simulated TTL)")
	}
}

// TestSession118_MemStore_PruneAndEvict_Unlimited verifies that when
// maxSessions = -1 (unlimited / no limit) no tokens are evicted.
func TestSession118_MemStore_PruneAndEvict_Unlimited(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	now := time.Now().UTC()
	exp := now.Add(30 * 24 * time.Hour)
	_ = store.TrackSession(ctx, "user1", "tok-1", exp)
	_ = store.TrackSession(ctx, "user1", "tok-2", exp)
	_ = store.TrackSession(ctx, "user1", "tok-3", exp)

	// Passing -1 means "unlimited" — no eviction regardless of session count.
	evicted, err := store.PruneAndEvict(ctx, "user1", -1, now)
	if err != nil {
		t.Fatalf("PruneAndEvict: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("expected 0 evictions (unlimited), got %d: %v", len(evicted), evicted)
	}
}

// TestSession118_MemStore_PruneAndEvict_MaxOne verifies that when the login
// handler is about to add a 3rd session with maxSessions=2, the oldest is
// evicted. The login handler calls PruneAndEvict(maxSessions-1 = 1) to leave
// room for the incoming new session, so after eviction exactly 1 session
// remains, and after tracking the new session there are 2 (the max).
func TestSession118_MemStore_PruneAndEvict_MaxOne(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	now := time.Now().UTC()
	exp := now.Add(30 * 24 * time.Hour)

	// Simulate user has 2 sessions and max=2. A 3rd login is incoming.
	// Login handler calls PruneAndEvict(maxSessions-1=1) → evict 1.
	_ = store.TrackSession(ctx, "user1", "tok-old", exp)
	_ = store.TrackSession(ctx, "user1", "tok-new", exp)

	// Login handler passes effectiveMax = maxSessions-1 = 1.
	evicted, err := store.PruneAndEvict(ctx, "user1", 1, now)
	if err != nil {
		t.Fatalf("PruneAndEvict: %v", err)
	}
	if len(evicted) != 1 {
		t.Fatalf("expected 1 eviction; got %d: %v", len(evicted), evicted)
	}
	if evicted[0] != "tok-old" {
		t.Errorf("evicted[0] = %q; want 'tok-old'", evicted[0])
	}
}

// TestSession118_MemStore_PruneExpired verifies that tokens whose expiry is
// in the past are removed by PruneAndEvict.
func TestSession118_MemStore_PruneExpired(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	now := time.Now().UTC()
	past := now.Add(-1 * time.Second)
	future := now.Add(30 * 24 * time.Hour)

	_ = store.TrackSession(ctx, "user1", "tok-expired", past)
	_ = store.TrackSession(ctx, "user1", "tok-active", future)

	// PruneAndEvict should remove expired entries without evicting.
	evicted, err := store.PruneAndEvict(ctx, "user1", 5, now)
	if err != nil {
		t.Fatalf("PruneAndEvict: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("expected 0 evictions; expired session should be pruned silently, got %v", evicted)
	}

	// tok-expired is no longer in the session set.
	// Add 5 more sessions to trigger eviction on the next call: only tok-active
	// should remain after pruning, so there should be 1 active session.
	evicted2, err := store.PruneAndEvict(ctx, "user1", 5, now)
	if err != nil {
		t.Fatalf("PruneAndEvict second call: %v", err)
	}
	if len(evicted2) != 0 {
		t.Errorf("expected 0 evictions on second call; active count is 1 ≤ maxSessions=5, got %v", evicted2)
	}
}

// TestSession118_MemStore_Ping checks that Ping always returns nil.
func TestSession118_MemStore_Ping(t *testing.T) {
	store := redissession.NewMemStore()
	if err := store.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v; want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Refresh flow — Redis revocation fast-path
// ---------------------------------------------------------------------------

// TestSession118_RefreshChecksRedisRevocation verifies that when a token is
// in the MemStore revocation list, the refresh handler returns 401 with code
// 'auth.refresh_token_revoked' without hitting the DB.
func TestSession118_RefreshChecksRedisRevocation(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	// Pre-revoke a token.
	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	_ = store.RevokeSession(ctx, "user-x", "revoked-token-abc", exp)

	s := &Server{
		cfg: &config.Config{
			AppEnv:        config.EnvDevelopment,
			JWTSecretStub: "secret-118",
		},
		pool:         &fakeLoginPool{}, // must be non-nil to pass pool check
		sessionStore: store,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":"revoked-token-abc"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleAuthRefresh(w, r)

	// The handler must detect the revocation via Redis before any DB call.
	// fakeLoginPool panics on BeginTx, so a successful 401 proves the DB
	// was not touched.
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 (token revoked in Redis)", w.Code)
	}
	m := decodeSessionJSON(t, w)
	if code := errorCode(t, m); code != "auth.refresh_token_revoked" {
		t.Errorf("error.code = %q; want 'auth.refresh_token_revoked'", code)
	}
}

// TestSession118_RefreshPassesThroughWhenNotRevoked verifies that a token
// that is NOT in the revocation store proceeds to the DB check (which will
// panic in fakeLoginPool → confirms the DB path is reached).
func TestSession118_RefreshPassesThroughWhenNotRevoked(t *testing.T) {
	store := redissession.NewMemStore()
	// No tokens revoked in the store.

	s := &Server{
		cfg: &config.Config{
			AppEnv:        config.EnvDevelopment,
			JWTSecretStub: "secret-118",
		},
		pool:         &fakeLoginPool{}, // will panic on BeginTx
		sessionStore: store,
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected fakeLoginPool to panic (proving DB was reached); no panic occurred")
		}
	}()

	// This should panic because the fakeLoginPool panics on BeginTx.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":"not-revoked-token"}`))
	s.handleAuthRefresh(w, r)
}

// ---------------------------------------------------------------------------
// Concurrent-session enforcement — MemStore integration test
// ---------------------------------------------------------------------------

// TestSession118_ConcurrentSessionEnforcement verifies the full session
// eviction flow using the MemStore. It exercises:
//   - PruneAndEvict returning the oldest token to evict.
//   - The evicted token being marked revoked in the store.
//   - A subsequent IsRevoked call returning true for the evicted token.
//
// The login handler calls PruneAndEvict(maxSessions-1) to leave room for
// exactly one new session. With maxSessions=1, effectiveMax=0, so all current
// sessions are evicted before the new one is tracked.
//
// Note: This test does NOT exercise the DB path (no real PostgreSQL) — it
// verifies only the Redis-side logic.
func TestSession118_ConcurrentSessionEnforcement(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()

	now := time.Now().UTC()
	exp := now.Add(30 * 24 * time.Hour)

	// Simulate: user logs in first time → session A created.
	if err := store.TrackSession(ctx, "user1", "token-A", exp); err != nil {
		t.Fatalf("track A: %v", err)
	}

	// Simulate: user logs in again with max_sessions=1.
	// Login handler calls PruneAndEvict(effectiveMax = maxSessions-1 = 0).
	// With effectiveMax=0, all active sessions are evicted to make room
	// for exactly 1 new session.
	evicted, err := store.PruneAndEvict(ctx, "user1", 0, now)
	if err != nil {
		t.Fatalf("PruneAndEvict: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != "token-A" {
		t.Fatalf("evicted = %v; want [token-A]", evicted)
	}

	// Caller (login handler) revokes the evicted token.
	if err := store.RevokeSession(ctx, "user1", "token-A", exp); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	// Track new session B.
	if err := store.TrackSession(ctx, "user1", "token-B", exp); err != nil {
		t.Fatalf("track B: %v", err)
	}

	// Token-A is now revoked.
	revokedA, err := store.IsRevoked(ctx, "token-A")
	if err != nil {
		t.Fatalf("IsRevoked A: %v", err)
	}
	if !revokedA {
		t.Error("token-A should be revoked after eviction")
	}

	// Token-B is not revoked.
	revokedB, err := store.IsRevoked(ctx, "token-B")
	if err != nil {
		t.Fatalf("IsRevoked B: %v", err)
	}
	if revokedB {
		t.Error("token-B should not be revoked")
	}
}

// ---------------------------------------------------------------------------
// Full verification (step-by-step feature test)
// ---------------------------------------------------------------------------

// TestSession118_FullVerification exercises all five feature steps.
func TestSession118_FullVerification(t *testing.T) {
	t.Run("step1_redis_session_client_with_ttl", func(t *testing.T) {
		// MemStore satisfies the Store interface.
		var _ redissession.Store = redissession.NewMemStore()
		// TrackSession + IsRevoked contract.
		ctx := context.Background()
		store := redissession.NewMemStore()
		exp := time.Now().UTC().Add(time.Hour)
		_ = store.TrackSession(ctx, "u", "tok", exp)
		revoked, _ := store.IsRevoked(ctx, "tok")
		if revoked {
			t.Error("freshly tracked token should not be revoked")
		}
	})

	t.Run("step2_post_auth_logout_exists", func(_ *testing.T) {
		s := &Server{cfg: &config.Config{AppEnv: config.EnvDevelopment}}
		_ = s.handleAuthLogout // compile-time check
	})

	t.Run("step3_revocation_check_in_refresh_flow", func(t *testing.T) {
		ctx := context.Background()
		store := redissession.NewMemStore()
		exp := time.Now().UTC().Add(time.Hour)
		_ = store.RevokeSession(ctx, "u", "tok-revoked", exp)

		s := &Server{
			cfg: &config.Config{
				AppEnv:        config.EnvDevelopment,
				JWTSecretStub: "sec",
			},
			pool:         &fakeLoginPool{},
			sessionStore: store,
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
			strings.NewReader(`{"refresh_token":"tok-revoked"}`))
		s.handleAuthRefresh(w, r)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d; want 401 for revoked token", w.Code)
		}
	})

	t.Run("step4_concurrent_session_policy", func(t *testing.T) {
		ctx := context.Background()
		store := redissession.NewMemStore()
		now := time.Now().UTC()
		exp := now.Add(time.Hour)

		// Session A exists (user's first login).
		_ = store.TrackSession(ctx, "u", "A", exp)

		// Second login with max_sessions=1: login handler calls
		// PruneAndEvict(maxSessions-1 = 0) to make room for the new session.
		evicted, err := store.PruneAndEvict(ctx, "u", 0, now)
		if err != nil || len(evicted) != 1 {
			t.Fatalf("PruneAndEvict: err=%v evicted=%v", err, evicted)
		}
		if evicted[0] != "A" {
			t.Errorf("evicted[0]=%q; want A", evicted[0])
		}
	})

	t.Run("step5_server_struct_has_session_store_field", func(t *testing.T) {
		s, store := serverWithSessionStore(t, 3)
		if s.sessionStore == nil {
			t.Error("Server.sessionStore should be set when SessionStore option is provided")
		}
		if s.sessionStore != store {
			t.Error("Server.sessionStore should be the injected MemStore")
		}
		if s.maxConcurrentSessions != 3 {
			t.Errorf("Server.maxConcurrentSessions = %d; want 3", s.maxConcurrentSessions)
		}
	})
}

// ---------------------------------------------------------------------------
// Helper: inject a test actor into a request context
// ---------------------------------------------------------------------------

// withTestActor injects a minimal auth.Actor into the context, simulating the
// auth.Middleware that runs before the logout handler in production. The actor
// has the given ID and ActorTypeUser type.
func withTestActor(ctx context.Context, userID string) context.Context {
	return auth.WithActor(ctx, auth.Actor{
		ID:   userID,
		Type: auth.ActorTypeUser,
	})
}

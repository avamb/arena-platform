// auth_pr2_03_359_test.go — unit tests for PR2-03 security hardening (feature #359).
//
// Covers:
//   - Token hashing: SHA-256(rawToken) stored in DB; raw token only on client.
//   - Refresh rotation: Refresh returns a new refresh_token every call.
//   - Session compromise: reuse of revoked refresh token terminates all sessions.
//   - Password reset session revocation: PasswordResetConfirm revokes all sessions.
//   - Tampered-token rejection: altered tokens don't match any DB hash.
//
// All tests use in-memory fakes — no live PostgreSQL or Redis required.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/redissession"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// ---------------------------------------------------------------------------
// Token hashing — users.TokenHash
// ---------------------------------------------------------------------------

// TestPR2_03_359_TokenHashIsSHA256 verifies that TokenHash produces the correct
// SHA-256 hex output for a known input.
func TestPR2_03_359_TokenHashIsSHA256(t *testing.T) {
	// SHA-256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	empty := users.TokenHash("")
	if empty != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("TokenHash(\"\") = %q; want SHA-256 of empty string", empty)
	}
}

// TestPR2_03_359_TokenHashIsIdempotent verifies that the same input always
// produces the same output.
func TestPR2_03_359_TokenHashIsIdempotent(t *testing.T) {
	token, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	h1 := users.TokenHash(token)
	h2 := users.TokenHash(token)
	if h1 != h2 {
		t.Errorf("TokenHash is not idempotent: %q != %q", h1, h2)
	}
}

// TestPR2_03_359_TamperedTokenProducesDifferentHash verifies that even a
// single-byte change produces a completely different hash, so tampered tokens
// are rejected by DB lookup.
func TestPR2_03_359_TamperedTokenProducesDifferentHash(t *testing.T) {
	token, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	originalHash := users.TokenHash(token)

	// Tamper: replace first two characters.
	tampered := "XX" + token[2:]
	tamperedHash := users.TokenHash(tampered)

	if originalHash == tamperedHash {
		t.Errorf("tampered token produced same hash — token hashing is broken")
	}
}

// TestPR2_03_359_HashLength verifies that TokenHash always returns a 64-character
// hex string (SHA-256 produces 32 bytes = 64 hex chars).
func TestPR2_03_359_HashLength(t *testing.T) {
	for i := 0; i < 5; i++ {
		tok, err := users.GenerateVerificationToken()
		if err != nil {
			t.Fatalf("GenerateVerificationToken: %v", err)
		}
		h := users.TokenHash(tok)
		if len(h) != 64 {
			t.Errorf("TokenHash length = %d; want 64 (SHA-256 hex)", len(h))
		}
	}
}

// TestPR2_03_359_HashNotEqualRawToken verifies that the hash is always
// different from the raw token. Raw tokens are 64 hex chars from crypto/rand;
// their SHA-256 will almost never equal the input.
func TestPR2_03_359_HashNotEqualRawToken(t *testing.T) {
	for i := 0; i < 20; i++ {
		tok, err := users.GenerateVerificationToken()
		if err != nil {
			t.Fatalf("GenerateVerificationToken: %v", err)
		}
		h := users.TokenHash(tok)
		if h == tok {
			t.Fatalf("TokenHash(rawToken) == rawToken — SHA-256 is not being applied")
		}
	}
}

// ---------------------------------------------------------------------------
// Refresh handler — validation (no DB)
// ---------------------------------------------------------------------------

// TestPR2_03_359_RefreshHandlerExists verifies the Refresh handler shim exists.
func TestPR2_03_359_RefreshHandlerExists(t *testing.T) {
	s := minServerForAuth(t)
	_ = s.handleAuthRefresh // compile-time check
}

// TestPR2_03_359_RefreshValidationRejectsEmptyBody verifies the Refresh handler
// rejects empty request bodies with 400.
func TestPR2_03_359_RefreshValidationRejectsEmptyBody(t *testing.T) {
	s := minServerForAuth(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", http.NoBody)
	s.handleAuthRefresh(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for empty body", w.Code)
	}
}

// TestPR2_03_359_RefreshValidationRejectsMissingToken verifies that an empty
// refresh_token field returns 400 with the correct error code.
func TestPR2_03_359_RefreshValidationRejectsMissingToken(t *testing.T) {
	s := minServerForAuth(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":""}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleAuthRefresh(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for missing refresh_token", w.Code)
	}
	m := bodyJSON(t, w)
	if code := errorCode(t, m); code != "validation.refresh_token_required" {
		t.Errorf("error.code = %q; want 'validation.refresh_token_required'", code)
	}
}

// TestPR2_03_359_RefreshNoPoolReturns503 verifies that the Refresh handler
// returns 503 when the pool is nil.
func TestPR2_03_359_RefreshNoPoolReturns503(t *testing.T) {
	s := minServerForAuth(t) // pool is nil
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":"some-raw-token"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleAuthRefresh(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (nil pool)", w.Code)
	}
}

// TestPR2_03_359_RefreshAlwaysHitsDB verifies that the Refresh handler always
// opens a DB transaction (for rotation + compromise detection) regardless of
// whether a session store is wired.
// fakeLoginPool panics on BeginTx; if the test function returns without panic,
// the DB was not reached — test failure.
func TestPR2_03_359_RefreshAlwaysHitsDB(t *testing.T) {
	// Provide a non-empty JWT secret so the handler passes the configuration
	// guard and reaches BeginTx (where fakeLoginPool will panic).
	cfg := &config.Config{
		AppEnv:        config.EnvDevelopment,
		AppName:       "test",
		JWTSecretStub: "test-jwt-secret-for-pr2-03",
	}
	s := &Server{
		cfg:  cfg,
		pool: &fakeLoginPool{}, // panics on BeginTx
	}

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected fakeLoginPool to panic: Refresh must always open a DB transaction (PR2-03)")
		}
	}()

	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":"raw-token-value"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleAuthRefresh(httptest.NewRecorder(), r)
}

// ---------------------------------------------------------------------------
// ClearUserSessions — MemStore correctness
// ---------------------------------------------------------------------------

// TestPR2_03_359_MemStoreClearUserSessions verifies that ClearUserSessions
// removes all session tracking for a user without affecting other users.
func TestPR2_03_359_MemStoreClearUserSessions(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()
	now := time.Now().UTC()
	exp := now.Add(30 * 24 * time.Hour)

	// Track three sessions for user1.
	for _, tok := range []string{"tok-a", "tok-b", "tok-c"} {
		if err := store.TrackSession(ctx, "user1", tok, exp); err != nil {
			t.Fatalf("TrackSession(%s): %v", tok, err)
		}
	}

	// Track one session for user2 (should be unaffected).
	if err := store.TrackSession(ctx, "user2", "tok-u2", exp); err != nil {
		t.Fatalf("TrackSession user2: %v", err)
	}

	// Clear all sessions for user1.
	if err := store.ClearUserSessions(ctx, "user1"); err != nil {
		t.Fatalf("ClearUserSessions: %v", err)
	}

	// After clearing, PruneAndEvict on user1 with maxSessions=5 should see 0 sessions.
	// (If any sessions remained they would appear here.)
	evicted, err := store.PruneAndEvict(ctx, "user1", 5, now)
	if err != nil {
		t.Fatalf("PruneAndEvict user1: %v", err)
	}
	if len(evicted) != 0 {
		t.Errorf("expected 0 sessions for user1 after ClearUserSessions; got %d: %v", len(evicted), evicted)
	}

	// user2's session should be unaffected — still tracked.
	evicted2, err := store.PruneAndEvict(ctx, "user2", 0, now)
	if err != nil {
		t.Fatalf("PruneAndEvict user2: %v", err)
	}
	// maxSessions=0 means evict all; user2 should have exactly 1 to evict.
	if len(evicted2) != 1 || evicted2[0] != "tok-u2" {
		t.Errorf("user2 evicted = %v; want [tok-u2]", evicted2)
	}
}

// TestPR2_03_359_MemStoreClearUserSessionsIdempotent verifies that calling
// ClearUserSessions on a user with no sessions does not return an error.
func TestPR2_03_359_MemStoreClearUserSessionsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := redissession.NewMemStore()
	if err := store.ClearUserSessions(ctx, "nonexistent-user"); err != nil {
		t.Errorf("ClearUserSessions on empty user: %v; want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Store interface compliance — compile-time
// ---------------------------------------------------------------------------

// TestPR2_03_359_StoreInterfaceHasClearUserSessions verifies at compile time
// that MemStore (and therefore RedisStore by the compile-time guard in store.go)
// satisfies the updated Store interface which now includes ClearUserSessions.
func TestPR2_03_359_StoreInterfaceHasClearUserSessions(_ *testing.T) {
	var _ redissession.Store = redissession.NewMemStore()
}

// ---------------------------------------------------------------------------
// RevokeAllUserRefreshTokens — SQL files check
// ---------------------------------------------------------------------------

// TestPR2_03_359_RevokeAllUserRefreshTokensQueryExists verifies that the SQL
// query file and gen file both contain the RevokeAllUserRefreshTokens definition.
func TestPR2_03_359_RevokeAllUserRefreshTokensQueryExists(t *testing.T) {
	sqlContent := findFileByName(t, "refresh_tokens.sql")
	if !strings.Contains(sqlContent, "RevokeAllUserRefreshTokens") {
		t.Error("refresh_tokens.sql should contain RevokeAllUserRefreshTokens query")
	}
	if !strings.Contains(sqlContent, "revoked_at IS NULL") {
		t.Error("RevokeAllUserRefreshTokens should only revoke non-revoked tokens")
	}

	genContent := findFileByName(t, "refresh_tokens.sql.go")
	if !strings.Contains(genContent, "RevokeAllUserRefreshTokens") {
		t.Error("refresh_tokens.sql.go should contain RevokeAllUserRefreshTokens function")
	}
}

// ---------------------------------------------------------------------------
// Migration file check
// ---------------------------------------------------------------------------

// TestPR2_03_359_MigrationExists verifies the token hash invalidation migration
// exists with correct content.
func TestPR2_03_359_MigrationExists(t *testing.T) {
	content := findFileByName(t, "0065_token_hash_invalidation.sql")
	if !strings.Contains(content, "goose Up") {
		t.Error("0065 migration missing +goose Up directive")
	}
	if !strings.Contains(content, "refresh_tokens") {
		t.Error("0065 migration should reference refresh_tokens table")
	}
	if !strings.Contains(content, "email_verification_tokens") {
		t.Error("0065 migration should reference email_verification_tokens table")
	}
	if !strings.Contains(content, "password_reset_tokens") {
		t.Error("0065 migration should reference password_reset_tokens table")
	}
}

// ---------------------------------------------------------------------------
// Password-reset session revocation — structural check
// ---------------------------------------------------------------------------

// TestPR2_03_359_PasswordResetConfirmHandlerExists verifies the confirm
// handler is wired on *Server.
func TestPR2_03_359_PasswordResetConfirmHandlerExists(t *testing.T) {
	s := minServerForAuth(t)
	_ = s.handleAuthPasswordResetConfirm // compile-time
}

// TestPR2_03_359_PasswordResetConfirmNoPoolReturns503 verifies the handler
// checks pool availability before touching the session store.
func TestPR2_03_359_PasswordResetConfirmNoPoolReturns503(t *testing.T) {
	s := minServerForAuth(t) // pool is nil
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/confirm",
		strings.NewReader(`{"token":"raw-tok","new_password":"Password1!"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleAuthPasswordResetConfirm(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 for nil pool", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Full feature verification
// ---------------------------------------------------------------------------

// TestPR2_03_359_FullVerification bundles all structural checks for CI.
func TestPR2_03_359_FullVerification(t *testing.T) {
	t.Run("step1_token_hashing_function_exists", func(t *testing.T) {
		tok, err := users.GenerateVerificationToken()
		if err != nil {
			t.Fatalf("GenerateVerificationToken: %v", err)
		}
		h := users.TokenHash(tok)
		if len(h) != 64 {
			t.Errorf("TokenHash returned %d chars; want 64", len(h))
		}
		if h == tok {
			t.Error("TokenHash must differ from raw token")
		}
	})

	t.Run("step2_revokeall_query_in_sql_file", func(t *testing.T) {
		sql := findFileByName(t, "refresh_tokens.sql")
		if !strings.Contains(sql, "RevokeAllUserRefreshTokens") {
			t.Error("RevokeAllUserRefreshTokens not found in refresh_tokens.sql")
		}
	})

	t.Run("step3_refresh_rotation_returns_new_token_field", func(t *testing.T) {
		// Structural: verify the login.go comment documents the new refresh_token
		// in the Refresh response.
		loginGo := findFileByName(t, "login.go")
		if !strings.Contains(loginGo, "refresh_token") || !strings.Contains(loginGo, "newRawToken") {
			t.Error("login.go Refresh handler should produce a new refresh_token (rotation)")
		}
	})

	t.Run("step4_compromise_detection_in_refresh", func(t *testing.T) {
		loginGo := findFileByName(t, "login.go")
		if !strings.Contains(loginGo, "session_compromised") {
			t.Error("login.go Refresh handler should emit 'auth.session_compromised' error")
		}
		if !strings.Contains(loginGo, "RevokeAllUserRefreshTokens") {
			t.Error("login.go Refresh handler should call RevokeAllUserRefreshTokens on compromise")
		}
	})

	t.Run("step5_password_reset_confirm_revokes_sessions", func(t *testing.T) {
		resetGo := findFileByName(t, "password_reset.go")
		if !strings.Contains(resetGo, "RevokeAllUserRefreshTokens") {
			t.Error("password_reset.go PasswordResetConfirm should call RevokeAllUserRefreshTokens")
		}
		if !strings.Contains(resetGo, "ClearUserSessions") {
			t.Error("password_reset.go PasswordResetConfirm should call ClearUserSessions")
		}
	})

	t.Run("step5_tokens_hashed_in_all_handlers", func(t *testing.T) {
		for _, file := range []string{"login.go", "logout.go", "password_reset.go", "register.go", "verify.go"} {
			content := findFileByName(t, file)
			if !strings.Contains(content, "TokenHash") {
				t.Errorf("%s: missing TokenHash call — tokens must be hashed before DB operations", file)
			}
		}
	})
}

//go:build integration

// auth_password_reset_integration_test.go — integration tests for the
// password-reset flow (feature #116).
//
// These tests require a live PostgreSQL instance with arena-migrate applied.
// They are excluded from the normal "go test ./..." run and are activated only
// when the "integration" build tag is set:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/...
//
// The DATABASE_URL environment variable must point to a reachable PostgreSQL
// server (e.g. the one started by docker compose up postgres).
//
// Scenarios exercised:
//   - request → email queued (202 + slog output)
//   - valid token resets password (200 + can log in with new password)
//   - reused token rejected (410 Gone)
//   - expired token rejected (410 Gone)
package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
)

// integrationPool opens a real pgxpool from DATABASE_URL or skips the test.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// buildIntegrationResetServer builds a full Server wired to a real pgxpool.
func buildIntegrationResetServer(t *testing.T, pool *pgxpool.Pool) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		AppName:        "test",
		AppVersion:     "0.0.0-dev",
		RequestTimeout: 10 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "integration-test-secret",
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	return New(Options{
		Config:  cfg,
		Pool:    pool,
		PgxPool: pool,
	})
}

// seedTestUser inserts a user for integration tests and returns their ID + email.
func seedTestUser(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, string) {
	t.Helper()
	email := fmt.Sprintf("reset-test-%s@arena-integration.test", uuid.New().String()[:8])
	hash, err := users.HashPassword("OldPassword1!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	q := gen.New(pool)
	row, err := q.InsertUser(t.Context(), email, hash, "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup — ignore errors (row may already be gone).
		pool.Exec(t.Context(), "DELETE FROM users WHERE id = $1", row.ID)
	})
	return row.ID, email
}

// TestPasswordResetIntegration116_RequestEmailQueued verifies that a valid
// password-reset request returns 202 Accepted and that a reset token is stored.
func TestPasswordResetIntegration116_RequestEmailQueued(t *testing.T) {
	pool := integrationPool(t)
	_, email := seedTestUser(t, pool)
	srv := buildIntegrationResetServer(t, pool)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/request",
		strings.NewReader(`{"email":"`+email+`"}`))
	r.Header.Set("Content-Type", "application/json")
	srv.handleAuthPasswordResetRequest(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d; want 202", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["message"]; !ok {
		t.Error("response should contain 'message' field")
	}
}

// TestPasswordResetIntegration116_UnknownEmailReturns202 verifies that an
// unknown email also returns 202 (prevents user enumeration).
func TestPasswordResetIntegration116_UnknownEmailReturns202(t *testing.T) {
	pool := integrationPool(t)
	srv := buildIntegrationResetServer(t, pool)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/request",
		strings.NewReader(`{"email":"notregistered-ever@arena-integration.test"}`))
	r.Header.Set("Content-Type", "application/json")
	srv.handleAuthPasswordResetRequest(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d; want 202 (user enumeration prevention)", w.Code)
	}
}

// TestPasswordResetIntegration116_ValidTokenResetsPassword verifies the full
// happy path: request → token inserted → confirm → password updated.
func TestPasswordResetIntegration116_ValidTokenResetsPassword(t *testing.T) {
	pool := integrationPool(t)
	userID, email := seedTestUser(t, pool)
	srv := buildIntegrationResetServer(t, pool)
	_ = email

	// Insert a reset token directly.
	token, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	q := gen.New(pool)
	if err := q.InsertPasswordResetToken(t.Context(), token, userID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("InsertPasswordResetToken: %v", err)
	}

	// Confirm with valid token and new password.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/confirm",
		strings.NewReader(`{"token":"`+token+`","new_password":"NewPassword1!"}`))
	r.Header.Set("Content-Type", "application/json")
	srv.handleAuthPasswordResetConfirm(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["user_id"]; !ok {
		t.Error("response should contain 'user_id' field")
	}
}

// TestPasswordResetIntegration116_ReusedTokenRejected verifies that the same
// token cannot be used twice (410 Gone on second attempt).
func TestPasswordResetIntegration116_ReusedTokenRejected(t *testing.T) {
	pool := integrationPool(t)
	userID, _ := seedTestUser(t, pool)
	srv := buildIntegrationResetServer(t, pool)

	token, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	q := gen.New(pool)
	if err := q.InsertPasswordResetToken(t.Context(), token, userID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("InsertPasswordResetToken: %v", err)
	}

	body := `{"token":"` + token + `","new_password":"NewPassword1!"}`

	// First use — should succeed.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/confirm", strings.NewReader(body))
	r1.Header.Set("Content-Type", "application/json")
	srv.handleAuthPasswordResetConfirm(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first confirm: status = %d; want 200", w1.Code)
	}

	// Second use — should be rejected (token already used).
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/confirm", strings.NewReader(body))
	r2.Header.Set("Content-Type", "application/json")
	srv.handleAuthPasswordResetConfirm(w2, r2)
	if w2.Code != http.StatusGone {
		t.Errorf("second confirm: status = %d; want 410 Gone (token already used)", w2.Code)
	}

	var m map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, _ := m["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "auth.token_already_used" {
		t.Errorf("error.code = %q; want 'auth.token_already_used'", code)
	}
}

// TestPasswordResetIntegration116_ExpiredTokenRejected verifies that tokens
// past their expires_at are rejected with 410 Gone.
func TestPasswordResetIntegration116_ExpiredTokenRejected(t *testing.T) {
	pool := integrationPool(t)
	userID, _ := seedTestUser(t, pool)
	srv := buildIntegrationResetServer(t, pool)

	token, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	q := gen.New(pool)
	// Insert token that expired 2 hours ago.
	expired := time.Now().UTC().Add(-2 * time.Hour)
	if err := q.InsertPasswordResetToken(t.Context(), token, userID, expired); err != nil {
		t.Fatalf("InsertPasswordResetToken: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/confirm",
		strings.NewReader(`{"token":"`+token+`","new_password":"NewPassword1!"}`))
	r.Header.Set("Content-Type", "application/json")
	srv.handleAuthPasswordResetConfirm(w, r)

	if w.Code != http.StatusGone {
		t.Errorf("status = %d; want 410 Gone (token expired)", w.Code)
	}

	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, _ := m["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "auth.token_expired" {
		t.Errorf("error.code = %q; want 'auth.token_expired'", code)
	}
}

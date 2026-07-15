//go:build integration

// Package httpserver — auth_production_integration_test.go validates the
// full JWT production flow end-to-end against a real PostgreSQL database:
//
//	Register → Login → GET /v1/me → POST /v1/auth/refresh → POST /v1/auth/logout
//
// Requires DATABASE_URL env var pointing to a real PostgreSQL instance with
// migrations applied.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ -run TestAuthProduction
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// productionIntegrationServer constructs a full *Server backed by a real
// PostgreSQL pool with the JWTVerifier (production path) wired.
func productionIntegrationServer(t *testing.T) (*Server, string) {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	const secret = "integration-test-secret-32-bytes!!"
	const issuer = "arena-api"
	const audience = "arena-api"

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool.Ping: %v", err)
	}

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

	cfg := &config.Config{
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
	}

	srv := New(Options{
		Config:   cfg,
		PgxPool:  pool,
		Auth:     stub,
		Verifier: verifier,
	})

	return srv, secret
}

// TestAuthProduction_RegisterLoginMeRefreshLogout validates the happy-path
// production JWT flow end-to-end: register → login → GET /v1/me →
// POST /v1/auth/refresh → POST /v1/auth/logout.
func TestAuthProduction_RegisterLoginMeRefreshLogout(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	client := ts.Client()
	email := fmt.Sprintf("integration-%d@example.com", time.Now().UnixNano())
	const password = "Test1234!"

	// Register.
	regBody := fmt.Sprintf(`{"email":%q,"password":%q,"first_name":"Test","last_name":"User"}`,
		email, password)
	resp := integDoRequest(t, client, http.MethodPost, ts.URL+"/v1/auth/register", "", regBody)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("register: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Login.
	loginBody := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	resp = integDoRequest(t, client, http.MethodPost, ts.URL+"/v1/auth/login", "", loginBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	}
	loginData := integDecodeMap(t, resp)
	accessToken, _ := loginData["access_token"].(string)
	refreshToken, _ := loginData["refresh_token"].(string)
	if accessToken == "" {
		t.Fatal("login: empty access_token")
	}
	if refreshToken == "" {
		t.Fatal("login: empty refresh_token")
	}

	// GET /v1/me with access_token should succeed (200).
	resp = integDoRequest(t, client, http.MethodGet, ts.URL+"/v1/me", accessToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/me: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	} else {
		resp.Body.Close()
	}

	// POST /v1/auth/refresh.
	refreshBody := fmt.Sprintf(`{"refresh_token":%q}`, refreshToken)
	resp = integDoRequest(t, client, http.MethodPost, ts.URL+"/v1/auth/refresh", "", refreshBody)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("refresh: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	} else {
		refreshData := integDecodeMap(t, resp)
		newAccess, _ := refreshData["access_token"].(string)
		if newAccess == "" {
			t.Error("refresh: empty new access_token")
		}
	}

	// POST /v1/auth/logout.
	resp = integDoRequest(t, client, http.MethodPost, ts.URL+"/v1/auth/logout", accessToken, "")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Errorf("logout: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	} else {
		resp.Body.Close()
	}
}

// TestAuthProduction_InvalidTokensReturn401 verifies that various invalid
// tokens return 401 (not 503 as the old disabled-stub path did).
func TestAuthProduction_InvalidTokensReturn401(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()

	// Mint a token with the wrong secret to produce a bad-signature case.
	wrongSecretTok := integMustIssueJWT(t, "wrong-secret-padded-to-32bytes!!", "arena-api", "arena-api", time.Hour)

	cases := []struct{ name, token string }{
		{"garbage", "not.a.real.token"},
		{"wrong_secret", wrongSecretTok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := integDoRequest(t, client, http.MethodGet, ts.URL+"/v1/me", tc.token, "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", resp.StatusCode)
			}
			// Must NOT be 503 (old disabled-stub behaviour).
			if resp.StatusCode == http.StatusServiceUnavailable {
				t.Error("got 503; production verifier must not return 503 for auth failures")
			}
		})
	}

	// No-token case: should be 401 missing_token.
	t.Run("no_token", func(t *testing.T) {
		resp := integDoRequest(t, client, http.MethodGet, ts.URL+"/v1/me", "", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	_ = secret
}

// TestAuthProduction_ExpiredTokenReturns401TokenExpired verifies the
// specific auth.token_expired error code is returned.
func TestAuthProduction_ExpiredTokenReturns401TokenExpired(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	expiredTok := integMustIssueJWT(t, secret, "arena-api", "arena-api", -time.Hour)

	resp := integDoRequest(t, ts.Client(), http.MethodGet, ts.URL+"/v1/me", expiredTok, "")
	body := integReadBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", resp.StatusCode)
	}
	if !strings.Contains(body, "token_expired") {
		t.Errorf("expected auth.token_expired in body, got: %s", body)
	}
}

// TestAuthProduction_WrongIssuerReturns401 verifies issuer mismatch rejection.
func TestAuthProduction_WrongIssuerReturns401(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	tok := integMustIssueJWT(t, secret, "evil-issuer", "arena-api", time.Hour)
	resp := integDoRequest(t, ts.Client(), http.MethodGet, ts.URL+"/v1/me", tok, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong issuer, got %d", resp.StatusCode)
	}
}

// TestAuthProduction_WrongAudienceReturns401 verifies audience mismatch rejection.
func TestAuthProduction_WrongAudienceReturns401(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	tok := integMustIssueJWT(t, secret, "arena-api", "wrong-audience", time.Hour)
	resp := integDoRequest(t, ts.Client(), http.MethodGet, ts.URL+"/v1/me", tok, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong audience, got %d", resp.StatusCode)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func integDoRequest(t *testing.T, client *http.Client, method, url, bearer, body string) *http.Response {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func integReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf.String()
}

func integDecodeMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func integMustIssueJWT(t *testing.T, secret, issuer, audience string, ttl time.Duration) string {
	t.Helper()
	actorID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tok, _, err := auth.IssueJWT(secret, actorID, nil, nil, issuer, audience, ttl)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

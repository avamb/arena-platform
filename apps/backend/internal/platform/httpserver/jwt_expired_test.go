// Package httpserver — jwt_expired_test.go covers feature #8:
// "JWT auth middleware rejects expired token"
//
// Steps verified:
//  1. Generate JWT with exp = now() - 60s, valid signature, valid claims
//  2. POST /v1/echo with Authorization: Bearer <expired-jwt>  → HTTP 401
//  3. Response body carries code='auth.token_expired'
//  4. Response carries Retry-After: 0 (hint to request a fresh token immediately)
//  5. Generate JWT with nbf (not-before) in the future          → HTTP 401
//     Response body carries code='auth.token_not_yet_valid'
//  6. Both cases produce structured slog WARN log events
package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Log capture helper
// =============================================================================

// captureSlogHandler is a slog.Handler that stores every log record in memory.
// It is wired as the base logger in the test server so writeAuthError's
// logger.Warn(...) calls are captured for assertion.
type captureSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureSlogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureSlogHandler) WithGroup(_ string) slog.Handler      { return h }

// warnMessages returns the messages from all WARN-level records captured so far.
func (h *captureSlogHandler) warnMessages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			out = append(out, r.Message)
		}
	}
	return out
}

// hasWarnWithAttr returns true when at least one WARN record contains the
// given key=value attribute pair.
func (h *captureSlogHandler) hasWarnWithAttr(key, value string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != slog.LevelWarn {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && a.Value.String() == value {
				return false // stop iteration — found it
			}
			return true
		})
		// Re-iterate to set found flag (slog.Record.Attrs iterates once).
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && a.Value.String() == value {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// countWarnRecords returns the total number of WARN-level records captured.
func (h *captureSlogHandler) countWarnRecords() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			n++
		}
	}
	return n
}

// =============================================================================
// Test server builder
// =============================================================================

// buildV1TestServerWithLog builds a minimal test server like buildV1TestServer
// but also returns a captureSlogHandler so tests can verify WARN events.
func buildV1TestServerWithLog(t *testing.T) (*Server, *captureSlogHandler) {
	t.Helper()

	logHandler := &captureSlogHandler{}
	logger := slog.New(logHandler)

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:     "test-secret-jwt-expired",
		Issuer:     "arena-test",
		Audience:   "arena-api",
		DefaultTTL: time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config: cfg,
		Logger: logger,
		Auth:   stub,
		Audit:  &captureAuditWriter{},
		Idem:   &noopIdemStore{},
		Pool:   &fakePoolDB{tx: &fakeTx{}},
	})
	return s, logHandler
}

// issueExpiredToken creates an HS256 JWT using the test server's StubProvider
// secret but with exp = now() - 60s so the token is already expired on arrival.
func issueExpiredToken(t *testing.T) string {
	t.Helper()
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:     "test-secret-jwt-expired",
		Issuer:     "arena-test",
		Audience:   "arena-api",
		DefaultTTL: time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000001",
		ActorType: auth.ActorTypeStubUser,
		Roles:     []string{"viewer"},
		TTL:       -60 * time.Second, // exp = now - 60s → already expired
	})
	if err != nil {
		t.Fatalf("IssueToken (expired): %v", err)
	}
	return tok
}

// issueNbfFutureToken creates an HS256 JWT with nbf = now() + 1 hour so the
// token is not yet valid at the time it is sent.
func issueNbfFutureToken(t *testing.T) string {
	t.Helper()
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:     "test-secret-jwt-expired",
		Issuer:     "arena-test",
		Audience:   "arena-api",
		DefaultTTL: 2 * time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000002",
		ActorType: auth.ActorTypeStubUser,
		Roles:     []string{"viewer"},
		TTL:       2 * time.Hour,
		// NotBefore is 1 hour in the future — the token exists but is not yet valid.
		NotBefore: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("IssueToken (nbf future): %v", err)
	}
	return tok
}

// decodeErrorCode is a small helper that unmarshals the standard JSON error
// envelope {"error":{"code":"...",...}} and returns the code string.
func decodeErrorCode(t *testing.T, body io.Reader) string {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return env.Error.Code
}

// =============================================================================
// Step 1-3: Expired token → HTTP 401 with code='auth.token_expired'
// =============================================================================

// TestJWTExpired_Returns401 verifies steps 1-3: a token whose exp claim is 60
// seconds in the past is rejected with HTTP 401 and the structured error code
// "auth.token_expired".
func TestJWTExpired_Returns401(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	expiredTok := issueExpiredToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+expiredTok)
	req.Header.Set("Idempotency-Key", "test-key-expired-001")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", resp.StatusCode)
	}

	code := decodeErrorCode(t, resp.Body)
	if code != "auth.token_expired" {
		t.Fatalf("error.code: got %q want auth.token_expired", code)
	}
}

// TestJWTExpired_HasWWWAuthenticate verifies that the 401 for an expired token
// includes the WWW-Authenticate: Bearer realm="arena" challenge header per
// RFC 7235, so clients can detect the Bearer scheme automatically.
func TestJWTExpired_HasWWWAuthenticate(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	expiredTok := issueExpiredToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+expiredTok)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	want := `Bearer realm="arena"`
	if got := resp.Header.Get("WWW-Authenticate"); got != want {
		t.Fatalf("WWW-Authenticate: got %q want %q", got, want)
	}
}

// =============================================================================
// Step 4: Retry-After header present for expired token
// =============================================================================

// TestJWTExpired_RetryAfterHeader verifies step 4: the response to an expired
// token request carries Retry-After: 0, which signals "get a fresh token now".
func TestJWTExpired_RetryAfterHeader(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	expiredTok := issueExpiredToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+expiredTok)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After header must be present on auth.token_expired responses")
	}
	// RFC 7231 §7.1.3: a delta-seconds value of 0 means "retry immediately".
	if retryAfter != "0" {
		t.Fatalf("Retry-After: got %q want \"0\"", retryAfter)
	}
}

// TestJWTExpired_ResponseMessageHintToRefresh verifies step 4 (message hint):
// the localized message for auth.token_expired tells the client to request a
// fresh token, satisfying the "hint to refresh token" requirement.
func TestJWTExpired_ResponseMessageHintToRefresh(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	expiredTok := issueExpiredToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+expiredTok)
	req.Header.Set("Accept-Language", "en")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	msg := env.Error.Message
	// The message must mention both "expired" and a call-to-action ("fresh"
	// or "request") so the client knows what to do.
	if !strings.Contains(strings.ToLower(msg), "expired") {
		t.Fatalf("error.message %q does not mention 'expired'", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "fresh") && !strings.Contains(strings.ToLower(msg), "request") {
		t.Fatalf("error.message %q does not contain a refresh hint (expected 'fresh' or 'request')", msg)
	}
}

// =============================================================================
// Step 5: nbf in future → HTTP 401 with code='auth.token_not_yet_valid'
// =============================================================================

// TestJWTNbfFuture_Returns401 verifies step 5 (nbf case): a token whose nbf
// claim is 1 hour in the future is rejected with HTTP 401 and the structured
// error code "auth.token_not_yet_valid".
func TestJWTNbfFuture_Returns401(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	nbfTok := issueNbfFutureToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nbfTok)
	req.Header.Set("Idempotency-Key", "test-key-nbf-001")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", resp.StatusCode)
	}

	code := decodeErrorCode(t, resp.Body)
	if code != "auth.token_not_yet_valid" {
		t.Fatalf("error.code: got %q want auth.token_not_yet_valid", code)
	}
}

// TestJWTNbfFuture_HasWWWAuthenticate verifies that the 401 for an nbf-in-future
// token also includes the WWW-Authenticate header.
func TestJWTNbfFuture_HasWWWAuthenticate(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	nbfTok := issueNbfFutureToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nbfTok)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	want := `Bearer realm="arena"`
	if got := resp.Header.Get("WWW-Authenticate"); got != want {
		t.Fatalf("WWW-Authenticate: got %q want %q", got, want)
	}
}

// TestJWTNbfFuture_NoRetryAfter verifies that the Retry-After header is NOT
// set for auth.token_not_yet_valid (only expired tokens carry Retry-After).
func TestJWTNbfFuture_NoRetryAfterOnNbfRejection(t *testing.T) {
	t.Parallel()

	s, _ := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	nbfTok := issueNbfFutureToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nbfTok)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	// Retry-After must NOT be present: the NBF rejection does not signal that
	// the client should re-issue with the same token — they need a different
	// one with a valid (current or past) nbf.
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		t.Fatalf("Retry-After must be absent for auth.token_not_yet_valid; got %q", ra)
	}
}

// =============================================================================
// Step 6: Structured slog WARN events for both rejection cases
// =============================================================================

// TestJWTExpired_ProducesWarnLog verifies step 6 (expired case): rejecting an
// expired token emits at least one structured WARN log record from the auth
// middleware. The log record message should mention token rejection.
func TestJWTExpired_ProducesWarnLog(t *testing.T) {
	// Not parallel: we read the shared capture handler after the request.
	s, logHandler := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	expiredTok := issueExpiredToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+expiredTok)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d", resp.StatusCode)
	}

	warns := logHandler.warnMessages()
	if len(warns) == 0 {
		t.Fatal("expected at least one WARN log record when an expired token is rejected; got none")
	}
	// At least one WARN message should reference token rejection.
	foundRejection := false
	for _, msg := range warns {
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "token") || strings.Contains(lower, "auth") || strings.Contains(lower, "rejected") {
			foundRejection = true
			break
		}
	}
	if !foundRejection {
		t.Fatalf("WARN log records do not mention token rejection; records: %v", warns)
	}
}

// TestJWTNbfFuture_ProducesWarnLog verifies step 6 (nbf case): rejecting a
// not-yet-valid token also emits a WARN log record.
func TestJWTNbfFuture_ProducesWarnLog(t *testing.T) {
	// Not parallel: we read the shared capture handler after the request.
	s, logHandler := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	nbfTok := issueNbfFutureToken(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nbfTok)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d", resp.StatusCode)
	}

	warns := logHandler.warnMessages()
	if len(warns) == 0 {
		t.Fatal("expected at least one WARN log record when a not-yet-valid token is rejected; got none")
	}
}

// TestJWTBothCases_WarnLogHasAuthCode verifies step 6 (structural log shape):
// the WARN log records for both rejection types include the auth error code as
// a structured attribute (key="code"), enabling log-based alerting by code.
func TestJWTBothCases_WarnLogHasAuthCode(t *testing.T) {
	s, logHandler := buildV1TestServerWithLog(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	expiredTok := issueExpiredToken(t)
	nbfTok := issueNbfFutureToken(t)

	for _, tc := range []struct {
		name     string
		token    string
		wantCode string
	}{
		{"expired", expiredTok, "auth.token_expired"},
		{"nbf_future", nbfTok, "auth.token_not_yet_valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
				strings.NewReader(`{"message":"hi"}`))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tc.token)

			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("POST /v1/echo: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401; got %d", resp.StatusCode)
			}
		})
	}

	// After both requests we should have at least 2 WARN records total.
	if n := logHandler.countWarnRecords(); n < 2 {
		t.Fatalf("expected at least 2 WARN records (one per rejection); got %d", n)
	}
}

// =============================================================================
// Unit-level StubProvider verification (no HTTP server needed)
// =============================================================================

// TestStubProvider_ExpiredToken_ReturnsErrTokenExpired verifies step 1 at the
// StubProvider.Verify level: a token with exp in the past returns ErrTokenExpired.
func TestStubProvider_ExpiredToken_ReturnsErrTokenExpired(t *testing.T) {
	t.Parallel()

	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-jwt-expired",
		Enabled: true,
	})

	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: "00000000-0000-0000-0000-000000000001",
		TTL:     -60 * time.Second,
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	_, verr := stub.Verify(context.Background(), tok)
	if verr == nil {
		t.Fatal("Verify: expected error for expired token; got nil")
	}
	// Must wrap ErrTokenExpired so mapAuthErrorToStatus maps it to auth.token_expired.
	target := auth.ErrTokenExpired
	found := false
	for e := verr; e != nil; {
		if e == target {
			found = true
			break
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	if !found {
		t.Fatalf("Verify error does not wrap ErrTokenExpired; got: %v", verr)
	}
}

// TestStubProvider_NbfFutureToken_ReturnsErrTokenNotValidYet verifies step 5
// at the StubProvider.Verify level: a token with nbf in the future returns
// ErrTokenNotValidYet.
func TestStubProvider_NbfFutureToken_ReturnsErrTokenNotValidYet(t *testing.T) {
	t.Parallel()

	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-jwt-expired",
		Enabled: true,
	})

	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000001",
		TTL:       2 * time.Hour,
		NotBefore: time.Now().Add(time.Hour), // not valid for another hour
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	_, verr := stub.Verify(context.Background(), tok)
	if verr == nil {
		t.Fatal("Verify: expected error for nbf-in-future token; got nil")
	}
	target := auth.ErrTokenNotValidYet
	found := false
	for e := verr; e != nil; {
		if e == target {
			found = true
			break
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	if !found {
		t.Fatalf("Verify error does not wrap ErrTokenNotValidYet; got: %v", verr)
	}
}

// TestStubProvider_ValidToken_NotRejected verifies regression: a token with
// nbf in the past (or now) and exp in the future is accepted by Verify.
func TestStubProvider_ValidToken_NotRejected(t *testing.T) {
	t.Parallel()

	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-jwt-expired",
		Enabled: true,
	})

	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: "00000000-0000-0000-0000-000000000001",
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	actor, verr := stub.Verify(context.Background(), tok)
	if verr != nil {
		t.Fatalf("Verify: unexpected error for valid token: %v", verr)
	}
	if actor.ID == "" {
		t.Fatal("Verify: expected non-empty actor.ID for valid token")
	}
}

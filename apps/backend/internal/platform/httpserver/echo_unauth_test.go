// Package httpserver — unit tests for feature #43:
// "Direct call to /v1/echo without Bearer header returns 401"
//
// Verifies that:
//  1. POST /v1/echo with no Authorization header returns HTTP 401.
//  2. The response code is 'auth.missing_token'.
//  3. No audit_events row is written (auth middleware fires before handler).
//  4. No idempotency_keys lookup is performed (auth precedes idempotency check).
//
// All tests run without a live PostgreSQL connection using in-memory doubles.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

// =============================================================================
// Test doubles for feature #43
// =============================================================================

// countingIdemStore is an idempotency.Store that counts Lookup and Save calls.
// It always returns MISS on Lookup so the auth test never hits a replay path.
type countingIdemStore struct {
	lookupCalls atomic.Int64
	saveCalls   atomic.Int64
}

func (s *countingIdemStore) Lookup(_ context.Context, _, _ string) (idempotency.StoredResponse, bool, error) {
	s.lookupCalls.Add(1)
	return idempotency.StoredResponse{}, false, nil
}

func (s *countingIdemStore) Save(_ context.Context, _, _, _ string, _ idempotency.StoredResponse) error {
	s.saveCalls.Add(1)
	return nil
}

var _ idempotency.Store = (*countingIdemStore)(nil)

// =============================================================================
// Test server builder for feature #43
// =============================================================================

// buildUnauthEchoServer constructs a fully-wired Server so that /v1/echo is
// mounted (all four dependencies satisfied). The returned doubles let us verify
// that neither the audit writer nor the idempotency store was invoked.
func buildUnauthEchoServer(t *testing.T) (
	srv *Server,
	idemStore *countingIdemStore,
	auditW *captureAuditWriter,
) {
	t.Helper()

	const secret = "test-secret-not-for-production"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	idemStore = &countingIdemStore{}
	auditW = &captureAuditWriter{}
	pool := &fakePoolDB{tx: &fakeTx{}}

	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		AppName:        "arena-api-test",
		AppVersion:     "0.0.0-test",
		AppCommit:      "test",
		HTTPListenAddr: "127.0.0.1:0",
		BodyLimitBytes: 1 << 20,
		RequestTimeout: 5 * time.Second,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
		LogLevel:       "info",
		LogFormat:      "json",
		JWTSecretStub:  secret,
		EnableStubAuth: true,
	}

	srv = New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   pool,
		Audit:  auditW,
		Idem:   idemStore,
	})
	return srv, idemStore, auditW
}

// postEchoNoAuth sends POST /v1/echo with the given body and Idempotency-Key
// but WITHOUT an Authorization header.
func postEchoNoAuth(srv *Server, body, idemKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	// Deliberately omit Authorization header.
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	return rr
}

// =============================================================================
// Step 1-2 — POST /v1/echo without Authorization → HTTP 401
// =============================================================================

// TestEchoUnauth_Returns401 verifies that POST /v1/echo without an Authorization
// header returns HTTP 401 (Unauthorized).
func TestEchoUnauth_Returns401(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-1")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d; body=%s", rr.Code, rr.Body.String())
	}
}

// TestEchoUnauth_ResponseBodyIsJSON verifies that the 401 response body is
// valid JSON (the standard arena error envelope).
func TestEchoUnauth_ResponseBodyIsJSON(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-2")

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response is not valid JSON: %v; raw=%s", err, rr.Body.String())
	}
}

// TestEchoUnauth_ContentTypeIsJSON verifies that the 401 response carries
// Content-Type: application/json.
func TestEchoUnauth_ContentTypeIsJSON(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-3")

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json; got %q", ct)
	}
}

// =============================================================================
// Step 3 — Response code is 'auth.missing_token'
// =============================================================================

// TestEchoUnauth_CodeIsMissingToken verifies that the JSON error envelope
// carries code='auth.missing_token' when the Authorization header is absent.
func TestEchoUnauth_CodeIsMissingToken(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-4")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d", rr.Code)
	}

	var envelope map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("response must have top-level 'error' object; got %v", envelope)
	}
	code, _ := errObj["code"].(string)
	if code != "auth.missing_token" {
		t.Fatalf("expected code='auth.missing_token'; got %q", code)
	}
}

// TestEchoUnauth_ErrorEnvelopeHasMessageField verifies that the error object
// includes a non-empty 'message' field alongside the code.
func TestEchoUnauth_ErrorEnvelopeHasMessageField(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-5")

	var envelope map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("response must have top-level 'error' object")
	}
	msg, _ := errObj["message"].(string)
	if msg == "" {
		t.Fatalf("error.message must be non-empty; got %q", msg)
	}
}

// TestEchoUnauth_WWWAuthenticateHeaderPresent verifies that the 401 response
// includes a WWW-Authenticate: Bearer realm="arena" challenge header as
// required by RFC 7235.
func TestEchoUnauth_WWWAuthenticateHeaderPresent(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-6")

	wwwAuth := rr.Header().Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, "Bearer") {
		t.Fatalf("expected WWW-Authenticate to contain 'Bearer'; got %q", wwwAuth)
	}
}

// =============================================================================
// Step 4 — No audit_events row written
// =============================================================================

// TestEchoUnauth_NoAuditEventWritten verifies that the audit writer is never
// called when the request lacks an Authorization header. The auth middleware
// rejects the request before the handler (and thus before any audit writes)
// can execute.
func TestEchoUnauth_NoAuditEventWritten(t *testing.T) {
	t.Parallel()
	srv, _, auditW := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-7")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d", rr.Code)
	}

	events := auditW.getEvents()
	if len(events) != 0 {
		t.Fatalf("expected 0 audit events; got %d: %v", len(events), events)
	}
}

// =============================================================================
// Step 5 — No idempotency_keys row written (auth precedes idempotency check)
// =============================================================================

// TestEchoUnauth_NoIdempotencyLookup verifies that the idempotency store's
// Lookup method is never called when the auth middleware rejects the request.
// This confirms that auth executes before idempotency in the middleware chain.
func TestEchoUnauth_NoIdempotencyLookup(t *testing.T) {
	t.Parallel()
	srv, idemStore, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-8")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d", rr.Code)
	}

	if n := idemStore.lookupCalls.Load(); n != 0 {
		t.Fatalf("expected 0 idempotency Lookup calls; got %d", n)
	}
}

// TestEchoUnauth_NoIdempotencySave verifies that the idempotency store's
// Save method is never called on an unauthenticated request.
func TestEchoUnauth_NoIdempotencySave(t *testing.T) {
	t.Parallel()
	srv, idemStore, _ := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "key-abc-9")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401; got %d", rr.Code)
	}

	if n := idemStore.saveCalls.Load(); n != 0 {
		t.Fatalf("expected 0 idempotency Save calls; got %d", n)
	}
}

// =============================================================================
// Summary test — all 5 steps in one sweep
// =============================================================================

// TestEchoUnauth_FullVerification is a consolidated assertion that covers all
// five feature steps in a single request. Useful as a canary test and for
// future regression checking.
func TestEchoUnauth_FullVerification(t *testing.T) {
	t.Parallel()
	srv, idemStore, auditW := buildUnauthEchoServer(t)

	rr := postEchoNoAuth(srv, `{"message":"x"}`, "IDEM-KEY-FULL-43")

	// Step 1+2: HTTP 401
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("step 1+2: expected 401; got %d; body=%s", rr.Code, rr.Body.String())
	}

	// Step 3: code='auth.missing_token'
	var envelope map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if errObj, ok := envelope["error"].(map[string]any); ok {
		code, _ := errObj["code"].(string)
		if code != "auth.missing_token" {
			t.Errorf("step 3: expected code='auth.missing_token'; got %q", code)
		}
	} else {
		t.Errorf("step 3: response missing 'error' envelope object; got %v", envelope)
	}

	// Step 4: no audit events
	if events := auditW.getEvents(); len(events) != 0 {
		t.Errorf("step 4: expected 0 audit events; got %d", len(events))
	}

	// Step 5: no idempotency lookups
	if n := idemStore.lookupCalls.Load(); n != 0 {
		t.Errorf("step 5: expected 0 idempotency lookups; got %d", n)
	}
	if n := idemStore.saveCalls.Load(); n != 0 {
		t.Errorf("step 5: expected 0 idempotency saves; got %d", n)
	}
}

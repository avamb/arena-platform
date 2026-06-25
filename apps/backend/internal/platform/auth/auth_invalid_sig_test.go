// auth_invalid_sig_test.go covers feature #7 — JWT auth middleware rejects
// invalid signature.
//
// Verified steps:
//  1. Craft a JWT signed with a key DIFFERENT from the server's JWT_SIGNING_SECRET.
//  2. POST /v1/echo with Authorization: Bearer <bad-jwt>, Idempotency-Key: X.
//  3. Expect HTTP 401.
//  4. Expect error code: auth.invalid_token (NOT auth.invalid_signature — info hiding).
//  5. Response body does NOT contain "signature", "key", or "secret".
//  6. Downstream handler (audit_events writer) must NOT be called.
//  7. slog emits WARN-level entry mentioning "invalid_signature" with masked token.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// Shared test helpers for feature #7
// -------------------------------------------------------------------------

const (
	feature7CorrectSecret = "correct-secret-which-is-long-enough-for-hs256"
	feature7WrongSecret   = "wrong-secret-which-is-also-long-enough-for-hs256"
)

// mintWrongKeyJWT mints a JWT signed with a key that differs from the
// server's correct secret. The resulting token has a valid shape but
// will fail HMAC verification when the server checks it.
func mintWrongKeyJWT(t *testing.T) string {
	t.Helper()
	p, err := NewStubProvider(StubConfig{
		Secret:     feature7WrongSecret,
		Issuer:     "arena-test",
		Audience:   "arena-api",
		DefaultTTL: time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider(wrong key): %v", err)
	}
	tok, _, err := p.IssueToken(context.Background(), IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000099",
		ActorType: ActorTypeStubUser,
		Roles:     []string{"viewer"},
	})
	if err != nil {
		t.Fatalf("IssueToken(wrong key): %v", err)
	}
	return tok
}

// correctProvider builds a StubProvider using the correct server secret.
func correctProvider(t *testing.T) *StubProvider {
	t.Helper()
	p, err := NewStubProvider(StubConfig{
		Secret:     feature7CorrectSecret,
		Issuer:     "arena-test",
		Audience:   "arena-api",
		DefaultTTL: time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider(correct): %v", err)
	}
	return p
}

// -------------------------------------------------------------------------
// Step 2+3: POST with bad JWT → 401
// -------------------------------------------------------------------------

// TestMiddleware_InvalidSignature_Returns401 verifies that the auth middleware
// rejects a JWT whose signature was produced with the wrong secret and returns
// HTTP 401 (steps 2 and 3).
func TestMiddleware_InvalidSignature_Returns401(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	called := false
	h := Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	req.Header.Set("Idempotency-Key", "X")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if called {
		t.Fatal("downstream handler must NOT be called when signature is invalid")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type: got %q want application/json", ct)
	}
	if got := rr.Header().Get(HeaderWWWAuthenticate); got != WWWAuthenticateBearer {
		t.Fatalf("WWW-Authenticate: got %q want %q", got, WWWAuthenticateBearer)
	}
}

// -------------------------------------------------------------------------
// Step 4: error code == auth.invalid_token (not auth.invalid_signature)
// -------------------------------------------------------------------------

// TestMiddleware_InvalidSignature_ErrorCodeIsInvalidToken verifies that the
// HTTP response body carries error.code = "auth.invalid_token" rather than
// "auth.invalid_signature", so callers cannot distinguish a bad signature from
// other token problems (step 4 — information hiding).
func TestMiddleware_InvalidSignature_ErrorCodeIsInvalidToken(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	h := Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not be called for invalid-signature token")
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not valid JSON: %v; raw=%s", err, rr.Body.String())
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("response missing 'error' object; body=%s", rr.Body.String())
	}
	code, _ := errObj["code"].(string)
	if code != "auth.invalid_token" {
		t.Fatalf("error.code: got %q want auth.invalid_token (info-hiding requirement)", code)
	}
}

// TestMiddleware_InvalidSignature_CodeNotInvalidSignature additionally asserts
// that the *specific* code "auth.invalid_signature" is NEVER returned to
// clients — it would reveal that the token's cryptographic signature was
// inspected and found invalid, rather than some other validation failure.
func TestMiddleware_InvalidSignature_CodeNotInvalidSignature(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	h := Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not be called for invalid-signature token")
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "auth.invalid_signature") {
		t.Fatalf("response must NOT expose auth.invalid_signature code; body=%s", body)
	}
}

// -------------------------------------------------------------------------
// Step 5: message must not leak forbidden words
// -------------------------------------------------------------------------

// TestMiddleware_InvalidSignature_NoInfoLeakInMessage verifies that the HTTP
// response body does not contain the words "signature", "key", or "secret"
// anywhere — not in the code, the message, or any details field (step 5).
func TestMiddleware_InvalidSignature_NoInfoLeakInMessage(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	h := Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not be called for invalid-signature token")
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	bodyLower := strings.ToLower(rr.Body.String())
	for _, forbidden := range []string{"signature", "key", "secret"} {
		if strings.Contains(bodyLower, forbidden) {
			t.Fatalf("response body contains forbidden word %q (info leak); body=%s",
				forbidden, rr.Body.String())
		}
	}
}

// -------------------------------------------------------------------------
// Step 6: downstream handler (audit_events writer) NOT called
// -------------------------------------------------------------------------

// TestMiddleware_InvalidSignature_DownstreamNotCalled confirms that the request
// chain terminates at the auth middleware and does NOT reach the echo handler
// (which would insert an audit_events row). This is step 6 — failed auth must
// not produce audit records.
func TestMiddleware_InvalidSignature_DownstreamNotCalled(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	auditWriterCalled := false
	h := Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			// In production this handler would write to audit_events.
			auditWriterCalled = true
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if auditWriterCalled {
		t.Fatal("audit_events handler must NOT be invoked when token signature is invalid")
	}
	// Double-check HTTP status as well.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
}

// -------------------------------------------------------------------------
// Step 7: slog WARN-level entry with "invalid_signature" and masked token
// -------------------------------------------------------------------------

// TestMiddleware_InvalidSignature_SlogWarnContainsInvalidSignature verifies
// that the middleware emits a WARN-level slog record that:
//   - carries level WARN (step 7 — internal visibility for operators),
//   - mentions the string "invalid_signature" so log-aggregation queries can
//     filter on this specific failure class, and
//   - includes a masked token (first 8 chars + "...") rather than the full
//     bearer value.
func TestMiddleware_InvalidSignature_SlogWarnContainsInvalidSignature(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug, // capture everything including WARN
	}))

	h := Middleware(provider, MiddlewareOptions{Logger: testLogger})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not be called for invalid-signature token")
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("precondition: status %d; expected 401", rr.Code)
	}

	logOutput := buf.String()

	// Must have a WARN-level entry.
	if !strings.Contains(logOutput, `"WARN"`) {
		t.Fatalf("expected WARN-level slog entry; got:\n%s", logOutput)
	}

	// Must mention "invalid_signature" so operators can distinguish this class
	// of failure from expired tokens or other auth errors.
	if !strings.Contains(logOutput, "invalid_signature") {
		t.Fatalf("WARN log must contain 'invalid_signature'; got:\n%s", logOutput)
	}

	// Must contain the masked token prefix (first 8 chars of badTok).
	// A JWT always starts with the base64url-encoded header, which is >8 chars,
	// so the masked form is badTok[:8]+"...".
	maskedPrefix := badTok[:8]
	if !strings.Contains(logOutput, maskedPrefix) {
		t.Fatalf("WARN log must contain masked token prefix %q; got:\n%s",
			maskedPrefix, logOutput)
	}

	// Must NOT contain the full token (only the masked prefix).
	if strings.Contains(logOutput, badTok) {
		t.Fatalf("WARN log must NOT contain the full token value (only masked prefix); got:\n%s",
			logOutput)
	}
}

// TestMiddleware_InvalidSignature_SlogUsesDefaultWhenLoggerNil verifies that
// passing opts.Logger=nil does not panic — the middleware falls back to
// slog.Default() gracefully.
func TestMiddleware_InvalidSignature_SlogUsesDefaultWhenLoggerNil(t *testing.T) {
	provider := correctProvider(t)
	badTok := mintWrongKeyJWT(t)

	// No Logger in opts — must not panic.
	h := Middleware(provider, MiddlewareOptions{Logger: nil})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not be called")
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+badTok)
	rr := httptest.NewRecorder()

	// Must not panic.
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
}

// -------------------------------------------------------------------------
// maskToken unit tests
// -------------------------------------------------------------------------

// TestMaskToken_LongTokenShowsPrefix ensures tokens longer than 8 characters
// are masked to their 8-char prefix followed by "...".
func TestMaskToken_LongTokenShowsPrefix(t *testing.T) {
	tok := "abcdefghijklmnopqrstuvwxyz" // 26 chars
	masked := maskToken(tok)

	if !strings.HasPrefix(masked, "abcdefgh") {
		t.Fatalf("maskToken: expected prefix 'abcdefgh'; got %q", masked)
	}
	if !strings.HasSuffix(masked, "...") {
		t.Fatalf("maskToken: expected suffix '...'; got %q", masked)
	}
	if strings.Contains(masked, "ijklmnop") {
		t.Fatalf("maskToken: must not expose characters beyond prefix; got %q", masked)
	}
}

// TestMaskToken_ShortToken ensures tokens ≤8 chars are returned unchanged.
func TestMaskToken_ShortToken(t *testing.T) {
	for _, tok := range []string{"", "a", "abcdefgh"} {
		masked := maskToken(tok)
		if masked != tok {
			t.Fatalf("maskToken(%q): expected unchanged; got %q", tok, masked)
		}
	}
}

// TestMaskToken_ExactlyNineChars ensures a 9-char token is masked.
func TestMaskToken_ExactlyNineChars(t *testing.T) {
	tok := "123456789" // 9 chars — just over the 8-char threshold
	masked := maskToken(tok)
	if masked != "12345678..." {
		t.Fatalf("maskToken(%q): got %q want '12345678...'", tok, masked)
	}
}

// TestMiddleware_ValidToken_StillWorksAfterInvalidSigChange confirms that
// a correctly signed token still passes through the middleware after our
// invalid-signature handling changes. Regression guard for step 4.
func TestMiddleware_ValidToken_StillWorksAfterInvalidSigChange(t *testing.T) {
	provider := correctProvider(t)

	tok, _, err := provider.IssueToken(context.Background(), IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000001",
		ActorType: ActorTypeStubUser,
		Roles:     []string{"admin"},
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	called := false
	var seenActor Actor
	h := Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			seenActor, _ = ActorFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("downstream handler must be called for valid token; status=%d body=%s",
			rr.Code, rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
	if seenActor.ID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("actor.ID: got %q", seenActor.ID)
	}
}

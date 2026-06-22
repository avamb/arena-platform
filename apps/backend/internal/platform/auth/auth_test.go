// auth_test.go covers the slice of the auth boundary exercised by feature #6
// (JWT auth middleware rejects missing token):
//
//   - bearerFromHeader normalises every "no usable bearer token presented"
//     case into ErrMissingToken (absent header, wrong scheme, empty value).
//   - Middleware returns 401 with the standard error envelope on those cases.
//   - Middleware sets the WWW-Authenticate challenge header on 401 responses.
//   - The error envelope carries request_id, trace_id, code, and details.
//   - Accept-Language picks the right localized message (en / ru).
//   - The happy path (valid JWT) still passes through to the wrapped handler
//     so we do not regress feature #3 (POST /v1/echo).
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newStubProviderForTest(t *testing.T) *StubProvider {
	t.Helper()
	p, err := NewStubProvider(StubConfig{
		Secret:     "test-secret-which-is-long-enough-for-hs256",
		Issuer:     "arena-test",
		Audience:   "arena-api",
		DefaultTTL: time.Hour,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	return p
}

// wrapWithRequestID mimics the production chain: chi's RequestID + a small
// shim that mirrors the id onto the response header. Auth-middleware-only
// tests then see exactly the same context shape as the real server.
func wrapWithRequestID(next http.Handler) http.Handler {
	h := chimw.RequestID(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use a synthetic id so assertions are deterministic.
		r.Header.Set(chimw.RequestIDHeader, "test-req-id-001")
		// Stash a trace id on ctx so the envelope's trace_id is non-empty.
		ctx := logging.WithTraceID(r.Context(), "test-trace-id-abc123")
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
	})
}

func decodeEnvelope(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v\n  raw=%s", err, string(body))
	}
	if _, ok := env["error"]; !ok {
		t.Fatalf("envelope has no 'error' key: %s", string(body))
	}
	return env
}

func errSubObject(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	sub, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope.error is not an object: %v", env["error"])
	}
	return sub
}

// ---------------------------------------------------------------------------
// bearerFromHeader
// ---------------------------------------------------------------------------

func TestBearerFromHeader_NormalisesMissingTokenCases(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"absent_header", ""},
		{"only_whitespace", "   "},
		{"wrong_scheme_basic", "Basic abc"},
		{"wrong_scheme_digest", "Digest token=foo"},
		{"wrong_scheme_custom", "Token abc"},
		{"bearer_no_value", "Bearer "},
		{"bearer_only_whitespace_value", "Bearer    "},
		{"bearer_no_space_after_scheme", "Bearer"},
		{"lowercase_bearer_no_value", "bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			tok, err := bearerFromHeader(req)
			if tok != "" {
				t.Fatalf("expected empty token; got %q", tok)
			}
			if err == nil {
				t.Fatal("expected an error; got nil")
			}
			// Must wrap ErrMissingToken so the middleware classifies it
			// uniformly even when the surface text differs.
			if !isErrMissingToken(err) {
				t.Fatalf("expected ErrMissingToken; got %v", err)
			}
		})
	}
}

func TestBearerFromHeader_AcceptsWellFormedToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/protected", nil)
	req.Header.Set("Authorization", "Bearer abc.def.ghi")
	tok, err := bearerFromHeader(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "abc.def.ghi" {
		t.Fatalf("token mismatch: got %q want %q", tok, "abc.def.ghi")
	}
}

func isErrMissingToken(err error) bool {
	for e := err; e != nil; {
		if e == ErrMissingToken {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// Middleware — error envelope on missing token
// ---------------------------------------------------------------------------

func TestMiddleware_MissingHeader_Returns401WithEnvelope(t *testing.T) {
	provider := newStubProviderForTest(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h := wrapWithRequestID(Middleware(provider, MiddlewareOptions{})(next))

	req := httptest.NewRequest(http.MethodPost, "/v1/echo",
		bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("Idempotency-Key", "X")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if called {
		t.Fatal("next handler must NOT be called when token is missing")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
	if got := rr.Header().Get(HeaderWWWAuthenticate); got != WWWAuthenticateBearer {
		t.Fatalf("WWW-Authenticate header: got %q want %q", got, WWWAuthenticateBearer)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type: got %q want application/json...", ct)
	}

	env := decodeEnvelope(t, rr.Body.Bytes())
	errObj := errSubObject(t, env)
	if errObj["code"] != "auth.missing_token" {
		t.Fatalf("error.code: got %v want auth.missing_token", errObj["code"])
	}
	if msg, _ := errObj["message"].(string); msg == "" {
		t.Fatalf("error.message empty; got %v", errObj["message"])
	}
	if rid, _ := errObj["request_id"].(string); rid == "" {
		t.Fatalf("error.request_id empty; envelope=%v", env)
	}
	if tid, _ := errObj["trace_id"].(string); tid != "test-trace-id-abc123" {
		t.Fatalf("error.trace_id: got %v want test-trace-id-abc123", errObj["trace_id"])
	}
}

func TestMiddleware_BasicSchemeReturnsMissingToken(t *testing.T) {
	provider := newStubProviderForTest(t)
	h := wrapWithRequestID(Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not run")
		})))

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Basic abc")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
	if got := rr.Header().Get(HeaderWWWAuthenticate); got != WWWAuthenticateBearer {
		t.Fatalf("WWW-Authenticate header: got %q want %q", got, WWWAuthenticateBearer)
	}
	env := decodeEnvelope(t, rr.Body.Bytes())
	errObj := errSubObject(t, env)
	if errObj["code"] != "auth.missing_token" {
		t.Fatalf("error.code: got %v want auth.missing_token", errObj["code"])
	}
}

func TestMiddleware_BearerWithEmptyValueReturnsMissingToken(t *testing.T) {
	provider := newStubProviderForTest(t)
	h := wrapWithRequestID(Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not run")
		})))

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer    ")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
	if got := rr.Header().Get(HeaderWWWAuthenticate); got != WWWAuthenticateBearer {
		t.Fatalf("WWW-Authenticate header: got %q want %q", got, WWWAuthenticateBearer)
	}
	env := decodeEnvelope(t, rr.Body.Bytes())
	errObj := errSubObject(t, env)
	if errObj["code"] != "auth.missing_token" {
		t.Fatalf("error.code: got %v want auth.missing_token", errObj["code"])
	}
}

// ---------------------------------------------------------------------------
// Middleware — localization
// ---------------------------------------------------------------------------

func TestMiddleware_LocalisesMessageByAcceptLanguage(t *testing.T) {
	provider := newStubProviderForTest(t)
	h := wrapWithRequestID(Middleware(provider, MiddlewareOptions{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("downstream handler must not run")
		})))

	cases := []struct {
		name           string
		acceptLanguage string
		mustContain    string
	}{
		{"no_header_defaults_en", "", "Authentication required"},
		{"english_explicit", "en", "Authentication required"},
		{"english_with_region", "en-US", "Authentication required"},
		{"russian_explicit", "ru", "Требуется аутентификация"},
		{"russian_with_region", "ru-RU", "Требуется аутентификация"},
		{"multi_lang_prefer_ru", "ru;q=0.9, en;q=0.1", "Требуется аутентификация"},
		{"multi_lang_prefer_en", "en;q=0.9, ru;q=0.1", "Authentication required"},
		{"unsupported_falls_back_to_en", "ja-JP", "Authentication required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/echo", nil)
			if tc.acceptLanguage != "" {
				req.Header.Set("Accept-Language", tc.acceptLanguage)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			env := decodeEnvelope(t, rr.Body.Bytes())
			errObj := errSubObject(t, env)
			msg, _ := errObj["message"].(string)
			if !strings.Contains(msg, tc.mustContain) {
				t.Fatalf("message %q does not contain %q", msg, tc.mustContain)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// mapAuthErrorToStatus produces dotted codes
// ---------------------------------------------------------------------------

func TestMapAuthErrorToStatus_DottedCodes(t *testing.T) {
	cases := []struct {
		err      error
		wantStat int
		wantCode string
	}{
		{ErrMissingToken, http.StatusUnauthorized, "auth.missing_token"},
		{ErrMalformedToken, http.StatusUnauthorized, "auth.malformed_token"},
		{ErrInvalidSignature, http.StatusUnauthorized, "auth.invalid_signature"},
		{ErrTokenExpired, http.StatusUnauthorized, "auth.token_expired"},
		{ErrUnknownIssuer, http.StatusUnauthorized, "auth.unknown_issuer"},
		{ErrUnknownAudience, http.StatusUnauthorized, "auth.unknown_audience"},
		{ErrUnsupportedAlg, http.StatusUnauthorized, "auth.unsupported_alg"},
		{ErrDisabled, http.StatusServiceUnavailable, "auth.disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.wantCode, func(t *testing.T) {
			s, c := mapAuthErrorToStatus(tc.err)
			if s != tc.wantStat || c != tc.wantCode {
				t.Fatalf("got (%d,%q) want (%d,%q)", s, c, tc.wantStat, tc.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Happy path — token verified, downstream handler runs, actor attached.
// ---------------------------------------------------------------------------

func TestMiddleware_HappyPath_AttachesActor(t *testing.T) {
	provider := newStubProviderForTest(t)
	tok, _, err := provider.IssueToken(context.Background(), IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000042",
		ActorType: ActorTypeStubUser,
		Roles:     []string{"admin"},
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	var seenActor Actor
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := ActorFromContext(r.Context())
		if !ok {
			t.Fatal("actor not present on ctx")
		}
		seenActor = a
		w.WriteHeader(http.StatusNoContent)
	})

	h := wrapWithRequestID(Middleware(provider, MiddlewareOptions{})(next))
	req := httptest.NewRequest(http.MethodGet, "/v1/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status: got %d want 204; body=%s", rr.Code, rr.Body.String())
	}
	if seenActor.ID != "00000000-0000-0000-0000-000000000042" {
		t.Fatalf("actor.ID: got %q", seenActor.ID)
	}
	if seenActor.Type != ActorTypeStubUser {
		t.Fatalf("actor.Type: got %q", seenActor.Type)
	}
	if rr.Header().Get(HeaderWWWAuthenticate) != "" {
		t.Fatal("WWW-Authenticate must NOT be present on success")
	}
}

// ---------------------------------------------------------------------------
// negotiateLocale parser
// ---------------------------------------------------------------------------

func TestNegotiateLocale(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "en"},
		{"en", "en"},
		{"en-US", "en"},
		{"ru", "ru"},
		{"ru-RU", "ru"},
		{"ja-JP", "en"},
		{"ja-JP, ru", "ru"},
		{"ru;q=0.1, en;q=0.9", "en"},
		{"ru;q=0.9, en;q=0.1", "ru"},
		{"ru;q=0.5, en;q=0.5", "ru"},
		{"de, ja, ru;q=0.2", "ru"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := negotiateLocale(tc.in)
			if got != tc.want {
				t.Fatalf("negotiateLocale(%q): got %q want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// translateAuthCode
// ---------------------------------------------------------------------------

func TestTranslateAuthCode_FallbacksAreSafe(t *testing.T) {
	// Known code, known locale.
	m := translateAuthCode("auth.missing_token", "ru")
	if !strings.Contains(m, "Требуется аутентификация") {
		t.Fatalf("ru lookup wrong: %q", m)
	}
	// Known code, unknown locale falls back to English.
	m = translateAuthCode("auth.missing_token", "ja")
	if !strings.Contains(m, "Authentication required") {
		t.Fatalf("fallback to en wrong: %q", m)
	}
	// Unknown code returns the code itself rather than blank.
	m = translateAuthCode("auth.totally_unknown", "en")
	if m != "auth.totally_unknown" {
		t.Fatalf("unknown code fallback wrong: %q", m)
	}
}

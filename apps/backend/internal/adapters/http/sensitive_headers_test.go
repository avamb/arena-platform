// Package http_test — sensitive_headers_test.go verifies feature #10:
// "Authorization header masked in logs".
//
// The tests exercise two complementary contracts:
//
//  1. MaskSensitiveHeader (unit tests) — the pure function that decides how
//     each header is masked. These tests run in microseconds and have no I/O.
//
//  2. requestLogMiddleware via NewRouter (integration-style tests) — the
//     middleware added to the canonical chain confirms that raw bearer tokens
//     and cookie values never appear in slog output, while the masked
//     representation ("Bearer ***" and "<redacted>") is present.
//
// Both test groups are in the external _test package so they only rely on the
// exported API, keeping the boundary explicit.
package http_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// =============================================================================
// MaskSensitiveHeader — unit tests
// =============================================================================

// TestMaskSensitiveHeader_BearerToken verifies that the raw token portion of a
// "Bearer <token>" authorization value is replaced with "***" while the
// "Bearer " scheme prefix is preserved.
func TestMaskSensitiveHeader_BearerToken(t *testing.T) {
	raw := "Bearer SECRET_DO_NOT_LOG_ABCDEF"
	got := httpadapter.MaskSensitiveHeader("Authorization", raw)
	if got != "Bearer ***" {
		t.Errorf("want %q, got %q", "Bearer ***", got)
	}
}

// TestMaskSensitiveHeader_BearerTokenLowerCase verifies that the header name
// comparison is case-insensitive ("authorization" == "Authorization").
func TestMaskSensitiveHeader_BearerTokenLowerCase(t *testing.T) {
	raw := "Bearer mytoken123"
	got := httpadapter.MaskSensitiveHeader("authorization", raw)
	if got != "Bearer ***" {
		t.Errorf("want %q, got %q", "Bearer ***", got)
	}
}

// TestMaskSensitiveHeader_BasicScheme verifies that non-Bearer schemes are also
// masked: "Basic dXNlcjpwYXNz" → "Basic ***".
func TestMaskSensitiveHeader_BasicScheme(t *testing.T) {
	raw := "Basic dXNlcjpwYXNz"
	got := httpadapter.MaskSensitiveHeader("Authorization", raw)
	if got != "Basic ***" {
		t.Errorf("want %q, got %q", "Basic ***", got)
	}
}

// TestMaskSensitiveHeader_NoSchemePrefix verifies that an authorization value
// with no scheme prefix (no space) is redacted in full.
func TestMaskSensitiveHeader_NoSchemePrefix(t *testing.T) {
	raw := "justaplaintokennoscheme"
	got := httpadapter.MaskSensitiveHeader("Authorization", raw)
	if got != "<redacted>" {
		t.Errorf("want %q, got %q", "<redacted>", got)
	}
}

// TestMaskSensitiveHeader_ProxyAuthorization verifies that Proxy-Authorization
// receives the same masking treatment as Authorization.
func TestMaskSensitiveHeader_ProxyAuthorization(t *testing.T) {
	raw := "Bearer proxytoken"
	got := httpadapter.MaskSensitiveHeader("Proxy-Authorization", raw)
	if got != "Bearer ***" {
		t.Errorf("want %q, got %q", "Bearer ***", got)
	}
}

// TestMaskSensitiveHeader_CookieRedacted verifies that the entire Cookie header
// value is replaced with "<redacted>" to hide session identifiers.
func TestMaskSensitiveHeader_CookieRedacted(t *testing.T) {
	raw := "session=abc123; csrftoken=xyz789"
	got := httpadapter.MaskSensitiveHeader("Cookie", raw)
	if got != "<redacted>" {
		t.Errorf("want %q, got %q", "<redacted>", got)
	}
}

// TestMaskSensitiveHeader_SetCookieRedacted verifies that Set-Cookie is also
// redacted (it can contain new session identifiers sent from the server).
func TestMaskSensitiveHeader_SetCookieRedacted(t *testing.T) {
	raw := "session=newsession456; Path=/; HttpOnly"
	got := httpadapter.MaskSensitiveHeader("Set-Cookie", raw)
	if got != "<redacted>" {
		t.Errorf("want %q, got %q", "<redacted>", got)
	}
}

// TestMaskSensitiveHeader_NonSensitivePassThrough verifies that headers with
// no credential semantics (Content-Type, Accept-Language, X-Request-Id) are
// returned unchanged so log records remain useful for diagnostics.
func TestMaskSensitiveHeader_NonSensitivePassThrough(t *testing.T) {
	cases := []struct {
		name, value string
	}{
		{"Content-Type", "application/json"},
		{"Accept-Language", "ru,en;q=0.9"},
		{"X-Request-Id", "abc-123"},
		{"User-Agent", "curl/8.7.1"},
	}
	for _, tc := range cases {
		got := httpadapter.MaskSensitiveHeader(tc.name, tc.value)
		if got != tc.value {
			t.Errorf("header %q: want %q unchanged, got %q", tc.name, tc.value, got)
		}
	}
}

// TestMaskSensitiveHeader_EmptyAuthValue verifies that an empty authorization
// value (no scheme, no space) is returned as "<redacted>" without panic.
func TestMaskSensitiveHeader_EmptyAuthValue(t *testing.T) {
	got := httpadapter.MaskSensitiveHeader("Authorization", "")
	// No space in empty string → falls through to "<redacted>".
	if got != "<redacted>" {
		t.Errorf("want %q for empty authorization value, got %q", "<redacted>", got)
	}
}

// =============================================================================
// requestLogMiddleware via NewRouter — integration-style tests
// =============================================================================

// TestRequestLog_AuthorizationRawTokenNotLogged is the core assertion for
// feature #10, step 3: the literal raw bearer token must never appear in any
// slog record emitted by the middleware chain.
func TestRequestLog_AuthorizationRawTokenNotLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer SECRET_DO_NOT_LOG_ABCDEF")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if strings.Contains(logs, "SECRET_DO_NOT_LOG_ABCDEF") {
		t.Errorf("raw bearer token must NOT appear in logs; log output:\n%s", logs)
	}
}

// TestRequestLog_AuthorizationMaskedValueLogged is the core assertion for
// feature #10, step 4: the masked form "Bearer ***" must appear in log output.
func TestRequestLog_AuthorizationMaskedValueLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer SECRET_DO_NOT_LOG_ABCDEF")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if !strings.Contains(logs, "Bearer ***") {
		t.Errorf("masked token 'Bearer ***' must appear in logs; log output:\n%s", logs)
	}
}

// TestRequestLog_CookieRawValueNotLogged is the feature #10, step 5 assertion:
// raw cookie values (e.g. session identifiers) must never appear in log output.
func TestRequestLog_CookieRawValueNotLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Cookie", "session=abc123; csrftoken=xyz789")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if strings.Contains(logs, "abc123") {
		t.Errorf("raw cookie session id 'abc123' must NOT appear in logs; log output:\n%s", logs)
	}
	if strings.Contains(logs, "xyz789") {
		t.Errorf("raw cookie csrftoken 'xyz789' must NOT appear in logs; log output:\n%s", logs)
	}
}

// TestRequestLog_CookieRedactedValueLogged verifies that a "<redacted>"
// placeholder appears in logs when a Cookie header is present, confirming that
// the middleware actively records (and masks) the header rather than silently
// dropping it.
func TestRequestLog_CookieRedactedValueLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Cookie", "session=abc123")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if !strings.Contains(logs, "<redacted>") {
		t.Errorf("masked cookie '<redacted>' must appear in logs when Cookie header is set; log output:\n%s", logs)
	}
}

// TestRequestLog_RequestStartAndEndLogged verifies that both the "http request
// start" and "http.request.completed" log records are emitted for a normal request.
func TestRequestLog_RequestStartAndEndLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if !strings.Contains(logs, "http request start") {
		t.Errorf("expected 'http request start' in logs; log output:\n%s", logs)
	}
	if !strings.Contains(logs, "http.request.completed") {
		t.Errorf("expected 'http.request.completed' in logs; log output:\n%s", logs)
	}
}

// TestRequestLog_NoAuthHeaderMeansNoAuthInLog verifies that when no
// Authorization header is supplied the log output does NOT contain any
// "Bearer ***" — the middleware must not add phantom masked entries for headers
// that were not sent.
func TestRequestLog_NoAuthHeaderMeansNoAuthInLog(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	// Deliberately NO Authorization header.
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if strings.Contains(logs, "Bearer ***") {
		t.Errorf("'Bearer ***' must NOT appear when no Authorization header was sent; log output:\n%s", logs)
	}
}

// TestRequestLog_StatusCodeInEndLog verifies that the "http.request.completed" record
// includes the response status code.
func TestRequestLog_StatusCodeInEndLog(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if !strings.Contains(logs, "418") {
		t.Errorf("expected status 418 in 'http request end' log record; log output:\n%s", logs)
	}
}

// TestRequestLog_BothAuthAndCookieMaskedTogether verifies that when a single
// request carries BOTH Authorization and Cookie headers, both are masked and
// neither raw value appears in logs.
func TestRequestLog_BothAuthAndCookieMaskedTogether(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "json", "debug")

	r := httpadapter.NewRouter(httpadapter.Deps{Logger: logger})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer COMBINED_SECRET_TOKEN")
	req.Header.Set("Cookie", "session=COMBINED_SESSION_ID")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	logs := buf.String()
	if strings.Contains(logs, "COMBINED_SECRET_TOKEN") {
		t.Errorf("raw Authorization token must not appear in logs; log output:\n%s", logs)
	}
	if strings.Contains(logs, "COMBINED_SESSION_ID") {
		t.Errorf("raw Cookie session ID must not appear in logs; log output:\n%s", logs)
	}
	if !strings.Contains(logs, "Bearer ***") {
		t.Errorf("masked 'Bearer ***' must appear in logs; log output:\n%s", logs)
	}
	if !strings.Contains(logs, "<redacted>") {
		t.Errorf("masked '<redacted>' must appear in logs for cookie; log output:\n%s", logs)
	}
}

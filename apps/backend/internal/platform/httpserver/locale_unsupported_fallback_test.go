// locale_unsupported_fallback_test.go verifies feature #55:
// "Accept-Language with unsupported locale falls back to default"
//
// Unsupported locales must NOT 400/500. They fall back silently to the
// default locale ("en"). All four feature steps are covered:
//
//  1. GET /v1/info with Accept-Language: xx-XX → HTTP 200, active_locale='en'
//  2. Expect HTTP 200, active_locale='en'
//  3. POST /v1/echo with no token and Accept-Language: xx → error message in English
//  4. Verify slog DEBUG with locale_resolved=en, locale_requested=xx (audit-able)
//
// These tests reuse the buildV1TestServer helper (defined in v1_routes_test.go),
// the existing captureSlogHandler (defined in jwt_expired_test.go), and the
// in-memory test doubles already declared in echo_audit_test.go.
package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Locale-specific log capture helpers
// (reuses captureSlogHandler from jwt_expired_test.go)
// =============================================================================

// buildLocaleTestServer creates a Server wired with a captureSlogHandler at
// DEBUG level so locale resolution DEBUG records are captured. Returns both
// the server and the log handler for post-request assertions.
func buildLocaleTestServer(t *testing.T) (*Server, *captureSlogHandler) {
	t.Helper()

	logHandler := &captureSlogHandler{}
	logger := slog.New(logHandler)

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-locale-unsupported",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		// DefaultLocale and ActiveLocales are zero-valued; NegotiateLocale
		// falls back to DefaultLocale="en" and SupportedLocales=["en","ru"].
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

// findLocaleDebugRecord returns the attrs of the first DEBUG record whose
// message equals msg, along with a found flag. It uses slog.Record.Attrs to
// collect the attributes for assertion.
func findLocaleDebugRecord(h *captureSlogHandler, msg string) ([]slog.Attr, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != slog.LevelDebug || r.Message != msg {
			continue
		}
		var attrs []slog.Attr
		r.Attrs(func(a slog.Attr) bool {
			attrs = append(attrs, a)
			return true
		})
		return attrs, true
	}
	return nil, false
}

// localeAttrValue returns the string value for the given key from an slog.Attr
// slice, or ("", false) when the key is not present.
func localeAttrValue(attrs []slog.Attr, key string) (string, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value.String(), true
		}
	}
	return "", false
}

// =============================================================================
// Steps 1 + 2: GET /v1/info with Accept-Language: xx-XX → 200, active_locale='en'
// =============================================================================

// TestLocaleUnsupportedFallback_InfoXxXXReturns200 verifies step 1:
// An entirely unsupported locale tag does NOT cause 4xx or 5xx.
func TestLocaleUnsupportedFallback_InfoXxXXReturns200(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx-XX")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/info with Accept-Language: xx-XX: want 200, got %d (body: %s)",
			w.Code, w.Body.String())
	}
}

// TestLocaleUnsupportedFallback_InfoXxXXActiveLocaleIsEn verifies step 2:
// Unsupported locale "xx-XX" falls back silently to active_locale='en'.
func TestLocaleUnsupportedFallback_InfoXxXXActiveLocaleIsEn(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx-XX")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	activeLocale, ok := body["active_locale"].(string)
	if !ok {
		t.Fatalf("active_locale field missing or not a string: %v", body)
	}
	if activeLocale != "en" {
		t.Errorf("active_locale = %q with Accept-Language: xx-XX, want %q (fallback to default)", activeLocale, "en")
	}
}

// TestLocaleUnsupportedFallback_InfoXxActiveLocaleIsEn uses bare tag "xx".
func TestLocaleUnsupportedFallback_InfoXxActiveLocaleIsEn(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	activeLocale, _ := body["active_locale"].(string)
	if activeLocale != "en" {
		t.Errorf("active_locale = %q with Accept-Language: xx, want %q", activeLocale, "en")
	}
}

// TestLocaleUnsupportedFallback_InfoMultipleUnsupportedFallsToEn verifies that
// when every locale in a multi-value header is unsupported, fallback is "en".
func TestLocaleUnsupportedFallback_InfoMultipleUnsupportedFallsToEn(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx;q=0.9,yy;q=0.8,zz;q=0.5")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	activeLocale, _ := body["active_locale"].(string)
	if activeLocale != "en" {
		t.Errorf("active_locale = %q with all-unsupported locales, want %q", activeLocale, "en")
	}
}

// TestLocaleUnsupportedFallback_InfoContentTypeIsJSON verifies the unsupported-
// locale response still has the correct Content-Type.
func TestLocaleUnsupportedFallback_InfoContentTypeIsJSON(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx-XX")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// =============================================================================
// Step 3: POST /v1/echo with no token and Accept-Language: xx → English error
// =============================================================================

// TestLocaleUnsupportedFallback_EchoNoTokenXxReturns401 verifies step 3 (part 1):
// An unauthenticated POST /v1/echo with an unsupported locale still returns 401.
func TestLocaleUnsupportedFallback_EchoNoTokenXxReturns401(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "xx")
	// Deliberately no Authorization header.
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /v1/echo with no token, Accept-Language: xx: want 401, got %d", w.Code)
	}
}

// TestLocaleUnsupportedFallback_EchoNoTokenXxErrorIsEnglish verifies step 3
// (part 2): the 401 error message when locale is "xx" (unsupported) is in
// English — confirming fallback to default locale for error messages.
func TestLocaleUnsupportedFallback_EchoNoTokenXxErrorIsEnglish(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "xx")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	errMap, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing or wrong type: %v", body)
	}

	msg, _ := errMap["message"].(string)
	if msg == "" {
		t.Error("error message is empty in 401 response with Accept-Language: xx")
	}

	// The error message must not contain Cyrillic characters — if it did,
	// it would indicate the Russian locale was selected instead of the English
	// fallback.
	for _, r := range msg {
		if r >= 0x0400 && r <= 0x04FF {
			t.Errorf("401 error message contains Cyrillic char %q in %q — "+
				"expected English fallback, not Russian", r, msg)
			break
		}
	}
}

// TestLocaleUnsupportedFallback_EchoNoTokenXxXXErrorHasAuthCode verifies that
// the 401 error body has a machine-readable auth error code regardless of locale.
func TestLocaleUnsupportedFallback_EchoNoTokenXxXXErrorHasAuthCode(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "xx-XX")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	errMap, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing or wrong type: %v", body)
	}

	code, _ := errMap["code"].(string)
	if code == "" {
		t.Error("error code is empty in 401 response")
	}
	// Auth errors from the auth middleware carry an "auth." prefix.
	if !strings.HasPrefix(code, "auth.") {
		t.Errorf("error code %q does not have expected auth. prefix", code)
	}
}

// =============================================================================
// Step 4: slog DEBUG log emitted with locale_resolved + locale_requested attrs
// =============================================================================

// TestLocaleUnsupportedFallback_SlogDEBUGLocaleResolved verifies step 4:
// When an unsupported locale is requested, handleInfo emits a slog DEBUG log
// with locale_resolved=en and locale_requested=xx. This makes locale fallback
// audit-able in the server logs.
func TestLocaleUnsupportedFallback_SlogDEBUGLocaleResolved(t *testing.T) {
	srv, logHandler := buildLocaleTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	attrs, found := findLocaleDebugRecord(logHandler, "locale resolved")
	if !found {
		t.Fatal("slog DEBUG record with message 'locale resolved' not found after GET /v1/info")
	}

	resolved, ok := localeAttrValue(attrs, "locale_resolved")
	if !ok {
		t.Error("slog DEBUG 'locale resolved' record missing 'locale_resolved' attr")
	} else if resolved != "en" {
		t.Errorf("locale_resolved = %q, want %q (unsupported 'xx' should fall back to 'en')", resolved, "en")
	}

	requested, ok := localeAttrValue(attrs, "locale_requested")
	if !ok {
		t.Error("slog DEBUG 'locale resolved' record missing 'locale_requested' attr")
	} else if requested != "xx" {
		t.Errorf("locale_requested = %q, want %q (should record original Accept-Language header)", requested, "xx")
	}
}

// TestLocaleUnsupportedFallback_SlogDEBUGLocaleResolvedXxXX tests with xx-XX subtag.
func TestLocaleUnsupportedFallback_SlogDEBUGLocaleResolvedXxXX(t *testing.T) {
	srv, logHandler := buildLocaleTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "xx-XX")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	attrs, found := findLocaleDebugRecord(logHandler, "locale resolved")
	if !found {
		t.Fatal("slog DEBUG record 'locale resolved' not found for Accept-Language: xx-XX")
	}

	resolved, _ := localeAttrValue(attrs, "locale_resolved")
	if resolved != "en" {
		t.Errorf("locale_resolved = %q, want 'en'", resolved)
	}

	requested, _ := localeAttrValue(attrs, "locale_requested")
	if requested != "xx-XX" {
		t.Errorf("locale_requested = %q, want 'xx-XX' (verbatim header value)", requested)
	}
}

// TestLocaleUnsupportedFallback_SlogDEBUGAlsoFiresForSupportedLocale verifies
// that the DEBUG log fires for supported locales too (ru), confirming it's an
// audit log for ALL resolutions, not only fallbacks.
func TestLocaleUnsupportedFallback_SlogDEBUGAlsoFiresForSupportedLocale(t *testing.T) {
	srv, logHandler := buildLocaleTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "ru")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	attrs, found := findLocaleDebugRecord(logHandler, "locale resolved")
	if !found {
		t.Fatal("slog DEBUG record 'locale resolved' not found for Accept-Language: ru")
	}

	resolved, _ := localeAttrValue(attrs, "locale_resolved")
	if resolved != "ru" {
		t.Errorf("locale_resolved = %q, want 'ru'", resolved)
	}

	requested, _ := localeAttrValue(attrs, "locale_requested")
	if requested != "ru" {
		t.Errorf("locale_requested = %q, want 'ru'", requested)
	}
}

// =============================================================================
// Full sweep (all 4 steps combined)
// =============================================================================

// TestLocaleUnsupportedFallback_FullVerification runs all four feature steps
// in a single test to confirm the complete flow works end to end.
func TestLocaleUnsupportedFallback_FullVerification(t *testing.T) {
	// Step 1 + 2: GET /v1/info with xx-XX → 200, active_locale='en'
	t.Run("step1_2_info_xx_XX_returns_200_en", func(t *testing.T) {
		srv := buildV1TestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		req.Header.Set("Accept-Language", "xx-XX")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("step 1: want 200, got %d", w.Code)
		}

		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("step 2: decode: %v", err)
		}
		activeLocale, _ := body["active_locale"].(string)
		if activeLocale != "en" {
			t.Errorf("step 2: active_locale = %q, want 'en'", activeLocale)
		}
	})

	// Step 3: POST /v1/echo with no token, Accept-Language: xx → 401, English msg
	t.Run("step3_echo_no_token_xx_returns_401_english", func(t *testing.T) {
		srv := buildV1TestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", "xx")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("step 3: want 401, got %d", w.Code)
		}

		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("step 3: decode: %v", err)
		}
		errMap, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatalf("step 3: error envelope missing")
		}
		msg, _ := errMap["message"].(string)
		if msg == "" {
			t.Error("step 3: error message is empty")
		}
		for _, r := range msg {
			if r >= 0x0400 && r <= 0x04FF {
				t.Errorf("step 3: error message contains Cyrillic char %q — expected English", r)
				break
			}
		}
	})

	// Step 4: slog DEBUG with locale_resolved=en, locale_requested=xx
	t.Run("step4_slog_debug_locale_resolved_en", func(t *testing.T) {
		srv, logHandler := buildLocaleTestServer(t)

		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		req.Header.Set("Accept-Language", "xx")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("step 4: want 200, got %d", w.Code)
		}

		attrs, found := findLocaleDebugRecord(logHandler, "locale resolved")
		if !found {
			t.Fatal("step 4: slog DEBUG 'locale resolved' record not emitted")
		}

		resolved, _ := localeAttrValue(attrs, "locale_resolved")
		if resolved != "en" {
			t.Errorf("step 4: locale_resolved = %q, want 'en'", resolved)
		}

		requested, _ := localeAttrValue(attrs, "locale_requested")
		if requested != "xx" {
			t.Errorf("step 4: locale_requested = %q, want 'xx'", requested)
		}
	})
}

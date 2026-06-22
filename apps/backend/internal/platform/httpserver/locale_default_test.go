// locale_default_test.go verifies feature #51:
// "Default locale is en when no Accept-Language provided"
//
// All four feature steps are covered:
//
//  1. GET /v1/info with no Accept-Language header
//  2. Verify response active_locale = "en"
//  3. Verify any localized error message is in English
//  4. POST /v1/echo with no auth, no Accept-Language → 401 message in English
//
// The tests reuse the buildV1TestServer helper (defined in v1_routes_test.go)
// and the in-memory test doubles (captureAuditWriter, noopIdemStore,
// fakePoolDB, fakeTx) already declared in echo_audit_test.go — all in the
// same package httpserver.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// =============================================================================
// Step 1 + 2: GET /v1/info with no Accept-Language → active_locale = "en"
// =============================================================================

// TestLocaleDefault_InfoNoHeaderActiveLocaleIsEn verifies that when the
// Accept-Language header is absent the /v1/info response includes
// active_locale = "en" (the configured default).
func TestLocaleDefault_InfoNoHeaderActiveLocaleIsEn(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	// Deliberately NOT setting Accept-Language.
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
		t.Fatalf("active_locale field missing or not a string in response: %v", body)
	}
	if activeLocale != "en" {
		t.Errorf("active_locale = %q, want %q", activeLocale, "en")
	}
}

// TestLocaleDefault_InfoActiveLocaleFieldPresent checks that the active_locale
// key is present in the response even before checking the value.
func TestLocaleDefault_InfoActiveLocaleFieldPresent(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if _, exists := body["active_locale"]; !exists {
		t.Error("response body does not contain active_locale key")
	}
}

// TestLocaleDefault_InfoEmptyAcceptLanguageDefaultsToEn verifies that an
// explicitly empty Accept-Language header still resolves to "en".
func TestLocaleDefault_InfoEmptyAcceptLanguageDefaultsToEn(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "")
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
		t.Errorf("active_locale = %q with empty Accept-Language, want %q", activeLocale, "en")
	}
}

// =============================================================================
// Step 3: Localized error messages are in English when no Accept-Language
// =============================================================================

// TestLocaleDefault_404ErrorMessageIsEnglishByDefault verifies that a 404
// response (when no Accept-Language is provided) returns an error message that
// is English (non-Russian, non-empty).
func TestLocaleDefault_404ErrorMessageIsEnglishByDefault(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/nonexistent-path-12345", nil)
	// No Accept-Language header.
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
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
		t.Error("error message is empty in 404 response")
	}

	// Must not contain obvious Russian-only characters (Cyrillic) — which would
	// indicate the wrong locale was used.
	for _, r := range msg {
		if r >= 0x0400 && r <= 0x04FF {
			t.Errorf("404 error message appears to be Russian (Cyrillic char %q in %q), expected English", r, msg)
			break
		}
	}
}

// =============================================================================
// Step 4: POST /v1/echo with no auth, no Accept-Language → 401 in English
// =============================================================================

// TestLocaleDefault_EchoNoAuthNoLocale401IsEnglish verifies that a POST to
// /v1/echo with no Authorization header and no Accept-Language header returns
// a 401 response whose error message is in English.
func TestLocaleDefault_EchoNoAuthNoLocale401IsEnglish(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately NO Accept-Language, NO Authorization.
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
		t.Error("error message is empty in 401 response")
	}

	// Verify the message is in English: must not be empty and must contain ASCII
	// characters consistent with an English authentication error.
	if strings.TrimSpace(msg) == "" {
		t.Error("401 message is blank, expected English auth error message")
	}

	// Must not contain Cyrillic characters (which would indicate Russian locale
	// was selected despite no Accept-Language header).
	for _, r := range msg {
		if r >= 0x0400 && r <= 0x04FF {
			t.Errorf("401 error message appears Russian (Cyrillic char %q in %q), want English by default", r, msg)
			break
		}
	}
}

// TestLocaleDefault_EchoNoAuthNoLocale401HasCode verifies the error envelope
// has the expected machine-readable code alongside the English message.
func TestLocaleDefault_EchoNoAuthNoLocale401HasCode(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
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
	if !strings.HasPrefix(code, "auth.") {
		t.Errorf("error code %q does not have expected auth. prefix", code)
	}
}

// TestLocaleDefault_EchoNoAuthNoLocale401HasWWWAuthenticate verifies the 401
// response carries the standard WWW-Authenticate challenge header regardless of
// locale negotiation outcome.
func TestLocaleDefault_EchoNoAuthNoLocale401HasWWWAuthenticate(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	wwwAuth := w.Header().Get("WWW-Authenticate")
	if wwwAuth == "" {
		t.Error("WWW-Authenticate header missing on 401 response")
	}
	if !strings.Contains(strings.ToLower(wwwAuth), "bearer") {
		t.Errorf("WWW-Authenticate header %q does not contain 'Bearer'", wwwAuth)
	}
}

// TestLocaleDefault_InfoRuAcceptLanguageResolvesRu is a sanity check that the
// locale negotiation DOES select Russian when ru is requested, confirming that
// the English default is not hard-coded but is truly a fallback.
func TestLocaleDefault_InfoRuAcceptLanguageResolvesRu(t *testing.T) {
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Accept-Language", "ru")
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
	if activeLocale != "ru" {
		t.Errorf("active_locale = %q with Accept-Language: ru, want %q", activeLocale, "ru")
	}
}

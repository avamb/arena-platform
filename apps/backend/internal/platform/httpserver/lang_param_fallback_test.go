// lang_param_fallback_test.go verifies feature #56:
// "?lang=invalid returns default locale, not error"
//
// The ?lang= query parameter with an unknown locale MUST fall back to the
// default locale silently — it must not 400 or 500. Additionally, a valid
// ?lang= must override Accept-Language (highest-priority source).
//
// All four feature steps are covered:
//
//  1. GET /v1/info?lang=klingon  → HTTP 200, active_locale='en'
//  2. GET /v1/info?lang=ru       → HTTP 200, active_locale='ru'
//  3. GET /v1/info?lang=         → HTTP 200, fallback to Accept-Language or 'en'
//  4. Accept-Language: en + ?lang=ru  → active_locale='ru' (?lang takes priority)
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// getActiveLocaleWithLangParam issues GET /v1/info with optional ?lang= query
// parameter and optional Accept-Language header, returning the active_locale
// from the JSON response.
func getActiveLocaleWithLangParam(t *testing.T, langParam, acceptLang string) (statusCode int, activeLocale string) {
	t.Helper()
	srv := buildV1TestServer(t)

	url := "/v1/info"
	if langParam != "" {
		url = "/v1/info?lang=" + langParam
	}
	// Distinguish "no lang param" from "empty lang param"
	// For the empty-string case the test explicitly adds ?lang=

	req := httptest.NewRequest(http.MethodGet, url, nil)
	if acceptLang != "" {
		req.Header.Set("Accept-Language", acceptLang)
	}

	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	statusCode = w.Code
	if w.Code == http.StatusOK {
		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err == nil {
			activeLocale, _ = body["active_locale"].(string)
		}
	}
	return statusCode, activeLocale
}

// getActiveLocaleRaw issues GET /v1/info with the provided raw URL path (allows
// testing ?lang= empty string, e.g. "/v1/info?lang=").
func getActiveLocaleRaw(t *testing.T, rawURL, acceptLang string) (statusCode int, activeLocale string) {
	t.Helper()
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	if acceptLang != "" {
		req.Header.Set("Accept-Language", acceptLang)
	}

	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	statusCode = w.Code
	if w.Code == http.StatusOK {
		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err == nil {
			activeLocale, _ = body["active_locale"].(string)
		}
	}
	return statusCode, activeLocale
}

// =============================================================================
// Step 1: GET /v1/info?lang=klingon → 200, active_locale='en'
// =============================================================================

// TestLangParamFallback_KlingonReturns200 verifies that an unsupported ?lang=
// value does NOT cause a 4xx or 5xx response.
func TestLangParamFallback_KlingonReturns200(t *testing.T) {
	code, _ := getActiveLocaleWithLangParam(t, "klingon", "")
	if code != http.StatusOK {
		t.Errorf("GET /v1/info?lang=klingon: got status %d, want 200", code)
	}
}

// TestLangParamFallback_KlingonActiveLocaleIsEn verifies that an unsupported
// ?lang= value silently falls back to the default locale 'en'.
func TestLangParamFallback_KlingonActiveLocaleIsEn(t *testing.T) {
	_, locale := getActiveLocaleWithLangParam(t, "klingon", "")
	if locale != "en" {
		t.Errorf("GET /v1/info?lang=klingon: active_locale=%q, want 'en'", locale)
	}
}

// TestLangParamFallback_KlingonNot400 verifies specifically that an unsupported
// ?lang= does not return 400 Bad Request.
func TestLangParamFallback_KlingonNot400(t *testing.T) {
	code, _ := getActiveLocaleWithLangParam(t, "klingon", "")
	if code == http.StatusBadRequest {
		t.Error("GET /v1/info?lang=klingon: got 400, expected fallback to default (200)")
	}
}

// TestLangParamFallback_UnsupportedLangFallsToEn verifies a different
// unsupported value (fr - valid BCP-47 but not in active locales).
func TestLangParamFallback_UnsupportedLangFallsToEn(t *testing.T) {
	code, locale := getActiveLocaleWithLangParam(t, "fr", "")
	if code != http.StatusOK {
		t.Errorf("GET /v1/info?lang=fr: got %d, want 200", code)
	}
	if locale != "en" {
		t.Errorf("GET /v1/info?lang=fr: active_locale=%q, want 'en' (fr not supported)", locale)
	}
}

// TestLangParamFallback_ZhCNFallsToEn verifies a valid BCP-47 region tag that
// is not supported falls back to 'en'.
func TestLangParamFallback_ZhCNFallsToEn(t *testing.T) {
	code, locale := getActiveLocaleWithLangParam(t, "zh-CN", "")
	if code != http.StatusOK {
		t.Errorf("GET /v1/info?lang=zh-CN: got %d, want 200", code)
	}
	if locale != "en" {
		t.Errorf("GET /v1/info?lang=zh-CN: active_locale=%q, want 'en'", locale)
	}
}

// =============================================================================
// Step 2: GET /v1/info?lang=ru → 200, active_locale='ru'
// =============================================================================

// TestLangParamFallback_RuReturns200 verifies that a supported ?lang= value
// returns HTTP 200.
func TestLangParamFallback_RuReturns200(t *testing.T) {
	code, _ := getActiveLocaleWithLangParam(t, "ru", "")
	if code != http.StatusOK {
		t.Errorf("GET /v1/info?lang=ru: got %d, want 200", code)
	}
}

// TestLangParamFallback_RuActiveLocaleIsRu verifies that ?lang=ru selects
// the 'ru' locale.
func TestLangParamFallback_RuActiveLocaleIsRu(t *testing.T) {
	_, locale := getActiveLocaleWithLangParam(t, "ru", "")
	if locale != "ru" {
		t.Errorf("GET /v1/info?lang=ru: active_locale=%q, want 'ru'", locale)
	}
}

// TestLangParamFallback_EnReturnsEn verifies that ?lang=en selects 'en'.
func TestLangParamFallback_EnReturnsEn(t *testing.T) {
	_, locale := getActiveLocaleWithLangParam(t, "en", "")
	if locale != "en" {
		t.Errorf("GET /v1/info?lang=en: active_locale=%q, want 'en'", locale)
	}
}

// =============================================================================
// Step 3: GET /v1/info?lang= (empty) → 200, fallback to Accept-Language or 'en'
// =============================================================================

// TestLangParamFallback_EmptyLangReturns200 verifies that ?lang= with an empty
// value does not error and returns HTTP 200.
func TestLangParamFallback_EmptyLangReturns200(t *testing.T) {
	code, _ := getActiveLocaleRaw(t, "/v1/info?lang=", "")
	if code != http.StatusOK {
		t.Errorf("GET /v1/info?lang= (empty): got %d, want 200", code)
	}
}

// TestLangParamFallback_EmptyLangFallsToDefault verifies that an empty ?lang=
// falls back to the default locale 'en' (when no Accept-Language is set).
func TestLangParamFallback_EmptyLangFallsToDefault(t *testing.T) {
	_, locale := getActiveLocaleRaw(t, "/v1/info?lang=", "")
	if locale != "en" {
		t.Errorf("GET /v1/info?lang= (empty, no Accept-Language): active_locale=%q, want 'en'", locale)
	}
}

// TestLangParamFallback_EmptyLangFallsToAcceptLanguage verifies that an empty
// ?lang= allows the Accept-Language header to take effect.
func TestLangParamFallback_EmptyLangFallsToAcceptLanguage(t *testing.T) {
	_, locale := getActiveLocaleRaw(t, "/v1/info?lang=", "ru")
	if locale != "ru" {
		t.Errorf("GET /v1/info?lang= with Accept-Language: ru: active_locale=%q, want 'ru'", locale)
	}
}

// TestLangParamFallback_EmptyLangNot400 verifies empty ?lang= does not 400.
func TestLangParamFallback_EmptyLangNot400(t *testing.T) {
	code, _ := getActiveLocaleRaw(t, "/v1/info?lang=", "")
	if code == http.StatusBadRequest {
		t.Error("GET /v1/info?lang= (empty): got 400, expected fallback (200)")
	}
}

// =============================================================================
// Step 4: ?lang takes priority over Accept-Language when valid
//         Accept-Language: en, ?lang=ru → active_locale='ru'
// =============================================================================

// TestLangParamFallback_LangParamBeatsAcceptLanguage is the primary step-4
// verification: when ?lang=ru and Accept-Language: en, the result must be 'ru'.
func TestLangParamFallback_LangParamBeatsAcceptLanguage(t *testing.T) {
	_, locale := getActiveLocaleRaw(t, "/v1/info?lang=ru", "en")
	if locale != "ru" {
		t.Errorf("GET /v1/info?lang=ru with Accept-Language: en: active_locale=%q, want 'ru'", locale)
	}
}

// TestLangParamFallback_LangRuBeatsAcceptLanguageEn confirms step 4 with HTTP
// status check.
func TestLangParamFallback_LangRuBeatsAcceptLanguageEn_Returns200(t *testing.T) {
	code, locale := getActiveLocaleRaw(t, "/v1/info?lang=ru", "en")
	if code != http.StatusOK {
		t.Errorf("GET /v1/info?lang=ru with Accept-Language: en: got %d, want 200", code)
	}
	if locale != "ru" {
		t.Errorf("active_locale=%q, want 'ru'", locale)
	}
}

// TestLangParamFallback_LangEnBeatsAcceptLanguageRu verifies the reverse:
// ?lang=en with Accept-Language: ru must return 'en'.
func TestLangParamFallback_LangEnBeatsAcceptLanguageRu(t *testing.T) {
	_, locale := getActiveLocaleRaw(t, "/v1/info?lang=en", "ru")
	if locale != "en" {
		t.Errorf("GET /v1/info?lang=en with Accept-Language: ru: active_locale=%q, want 'en'", locale)
	}
}

// TestLangParamFallback_InvalidLangFallsToAcceptLanguage verifies that when
// ?lang= is invalid/unsupported, the Accept-Language header serves as fallback.
func TestLangParamFallback_InvalidLangFallsToAcceptLanguage(t *testing.T) {
	_, locale := getActiveLocaleRaw(t, "/v1/info?lang=klingon", "ru")
	if locale != "ru" {
		t.Errorf("GET /v1/info?lang=klingon with Accept-Language: ru: active_locale=%q, want 'ru' (klingon unsupported, fallback to Accept-Language)", locale)
	}
}

// TestLangParamFallback_InvalidLangNoAcceptLangFallsToDefault verifies that
// when both ?lang= is invalid and no Accept-Language header is set, the default
// locale 'en' is returned.
func TestLangParamFallback_InvalidLangNoAcceptLangFallsToDefault(t *testing.T) {
	_, locale := getActiveLocaleRaw(t, "/v1/info?lang=klingon", "")
	if locale != "en" {
		t.Errorf("GET /v1/info?lang=klingon (no Accept-Language): active_locale=%q, want 'en'", locale)
	}
}

// =============================================================================
// Full sweep — all four steps
// =============================================================================

// TestLangParamFallback_FullVerification exercises all four feature steps in
// a single table-driven test.
func TestLangParamFallback_FullVerification(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		acceptLang string
		wantStatus int
		wantLocale string
	}{
		// Step 1: ?lang=klingon → 200, active_locale='en'
		{
			name:       "step1_invalid_lang_param_200",
			url:        "/v1/info?lang=klingon",
			acceptLang: "",
			wantStatus: http.StatusOK,
			wantLocale: "en",
		},
		// Step 2: ?lang=ru → 200, active_locale='ru'
		{
			name:       "step2_valid_lang_ru",
			url:        "/v1/info?lang=ru",
			acceptLang: "",
			wantStatus: http.StatusOK,
			wantLocale: "ru",
		},
		// Step 3: ?lang= (empty) → 200, fallback to 'en'
		{
			name:       "step3_empty_lang_param",
			url:        "/v1/info?lang=",
			acceptLang: "",
			wantStatus: http.StatusOK,
			wantLocale: "en",
		},
		// Step 3b: ?lang= (empty) with Accept-Language: ru → fallback to 'ru'
		{
			name:       "step3b_empty_lang_param_accept_language_ru",
			url:        "/v1/info?lang=",
			acceptLang: "ru",
			wantStatus: http.StatusOK,
			wantLocale: "ru",
		},
		// Step 4: Accept-Language: en, ?lang=ru → ru (?lang takes priority)
		{
			name:       "step4_lang_param_beats_accept_language",
			url:        "/v1/info?lang=ru",
			acceptLang: "en",
			wantStatus: http.StatusOK,
			wantLocale: "ru",
		},
		// Additional: invalid ?lang= falls back to Accept-Language
		{
			name:       "additional_invalid_lang_fallback_to_accept_language",
			url:        "/v1/info?lang=klingon",
			acceptLang: "ru",
			wantStatus: http.StatusOK,
			wantLocale: "ru",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, locale := getActiveLocaleRaw(t, tc.url, tc.acceptLang)
			if code != tc.wantStatus {
				t.Errorf("%s: got status %d, want %d", tc.name, code, tc.wantStatus)
			}
			if locale != tc.wantLocale {
				t.Errorf("%s: active_locale=%q, want %q", tc.name, locale, tc.wantLocale)
			}
		})
	}
}

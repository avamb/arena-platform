// locale_qvalue_test.go verifies feature #54:
// "Accept-Language with multiple weighted locales selects highest-weight supported"
//
// The parser must honor q-values (RFC 4647 lookup). For example:
//   Accept-Language: fr;q=0.9,ru;q=0.8 (no fr support) selects ru.
//
// All six feature steps are covered:
//
//  1. GET /v1/info with Accept-Language: fr;q=0.9,ru;q=0.8,en;q=0.5
//     → active_locale='ru' (fr not supported, ru is next highest)
//  2. Verify response active_locale='ru'
//  3. GET with Accept-Language: en-US,en;q=0.9 → 'en'
//  4. GET with Accept-Language: ru-RU → 'ru' (region stripped)
//  5. GET with Accept-Language: zh-CN → 'en' (no zh support, fallback to default)
//  6. Locale negotiation function is unit-tested directly (see locale_unit_test.go)
//
// These tests reuse the buildV1TestServer helper (defined in v1_routes_test.go)
// and in-memory test doubles already declared in echo_audit_test.go.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// helper: performs GET /v1/info with the given Accept-Language header and
// returns the parsed active_locale field.
func getActiveLocale(t *testing.T, acceptLang string) string {
	t.Helper()
	srv := buildV1TestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	if acceptLang != "" {
		req.Header.Set("Accept-Language", acceptLang)
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	locale, _ := body["active_locale"].(string)
	return locale
}

// =============================================================================
// Steps 1+2: fr;q=0.9,ru;q=0.8,en;q=0.5 → 'ru' (fr unsupported, ru is next)
// =============================================================================

// TestLocaleQValue_FrUnsupportedFallsToRu verifies step 1+2:
// When fr (highest q) is not supported, ru (next highest) is selected.
func TestLocaleQValue_FrUnsupportedFallsToRu(t *testing.T) {
	locale := getActiveLocale(t, "fr;q=0.9,ru;q=0.8,en;q=0.5")
	if locale != "ru" {
		t.Errorf("active_locale = %q, want 'ru' (fr unsupported, ru is next highest q)", locale)
	}
}

// TestLocaleQValue_FrUnsupportedFallsToRu_NotFr ensures fr was NOT selected.
func TestLocaleQValue_FrUnsupportedFallsToRu_NotFr(t *testing.T) {
	locale := getActiveLocale(t, "fr;q=0.9,ru;q=0.8,en;q=0.5")
	if locale == "fr" {
		t.Error("active_locale is 'fr' but fr is not a supported locale")
	}
}

// TestLocaleQValue_FrUnsupportedFallsToRu_NotEn ensures en was NOT selected
// (ru has higher q-value than en, so ru wins even though both are supported).
func TestLocaleQValue_FrUnsupportedFallsToRu_NotEn(t *testing.T) {
	locale := getActiveLocale(t, "fr;q=0.9,ru;q=0.8,en;q=0.5")
	if locale == "en" {
		t.Error("active_locale is 'en' but ru has higher q-value and should win")
	}
}

// TestLocaleQValue_RuHigherThanEn verifies a simpler two-locale scenario:
// ru;q=0.8,en;q=0.5 → 'ru' (ru has higher q-value).
func TestLocaleQValue_RuHigherThanEn(t *testing.T) {
	locale := getActiveLocale(t, "ru;q=0.8,en;q=0.5")
	if locale != "ru" {
		t.Errorf("active_locale = %q, want 'ru' (ru has higher q-value)", locale)
	}
}

// =============================================================================
// Step 3: en-US,en;q=0.9 → 'en'
// =============================================================================

// TestLocaleQValue_EnUSReturnsEn verifies that 'en-US' (implicit q=1.0) is
// reduced to 'en' and selected as the best supported match.
func TestLocaleQValue_EnUSReturnsEn(t *testing.T) {
	locale := getActiveLocale(t, "en-US,en;q=0.9")
	if locale != "en" {
		t.Errorf("active_locale = %q, want 'en' for Accept-Language: en-US,en;q=0.9", locale)
	}
}

// TestLocaleQValue_EnUSImplicitQ1 confirms the region tag doesn't block matching.
func TestLocaleQValue_EnUSImplicitQ1(t *testing.T) {
	// en-US has implicit q=1.0, en has q=0.9; both reduce to "en".
	// Either way, result must be "en".
	locale := getActiveLocale(t, "en-US,en;q=0.9")
	if locale == "" {
		t.Error("active_locale is empty, expected 'en'")
	}
	if locale != "en" {
		t.Errorf("active_locale = %q, want 'en'", locale)
	}
}

// =============================================================================
// Step 4: ru-RU → 'ru' (region stripped)
// =============================================================================

// TestLocaleQValue_RuRURegionStripped verifies that 'ru-RU' is reduced to
// primary subtag 'ru' and matched against the supported locale set.
func TestLocaleQValue_RuRURegionStripped(t *testing.T) {
	locale := getActiveLocale(t, "ru-RU")
	if locale != "ru" {
		t.Errorf("active_locale = %q, want 'ru' for Accept-Language: ru-RU (region stripped)", locale)
	}
}

// TestLocaleQValue_RuRUWithQValue confirms region stripping works with q-values too.
func TestLocaleQValue_RuRUWithQValue(t *testing.T) {
	locale := getActiveLocale(t, "ru-RU;q=0.8")
	if locale != "ru" {
		t.Errorf("active_locale = %q, want 'ru' for Accept-Language: ru-RU;q=0.8", locale)
	}
}

// =============================================================================
// Step 5: zh-CN → 'en' (no zh support, fall back to default)
// =============================================================================

// TestLocaleQValue_ZhCNFallsToDefault verifies that an entirely unsupported
// language (zh) causes fallback to the configured default locale 'en'.
func TestLocaleQValue_ZhCNFallsToDefault(t *testing.T) {
	locale := getActiveLocale(t, "zh-CN")
	if locale != "en" {
		t.Errorf("active_locale = %q, want 'en' (zh not supported, fallback to default)", locale)
	}
}

// TestLocaleQValue_ZhFallsToDefault uses zh without region subtag.
func TestLocaleQValue_ZhFallsToDefault(t *testing.T) {
	locale := getActiveLocale(t, "zh")
	if locale != "en" {
		t.Errorf("active_locale = %q, want 'en' (zh not supported, fallback to default)", locale)
	}
}

// TestLocaleQValue_UnsupportedOnlyFallsToDefault verifies that when ALL
// supplied locales are unsupported the default 'en' is returned.
func TestLocaleQValue_UnsupportedOnlyFallsToDefault(t *testing.T) {
	locale := getActiveLocale(t, "fr;q=0.9,zh;q=0.8,de;q=0.7")
	if locale != "en" {
		t.Errorf("active_locale = %q, want 'en' (all locales unsupported)", locale)
	}
}

// =============================================================================
// Additional q-value edge cases
// =============================================================================

// TestLocaleQValue_ExplicitQ1SelectsCorrectly verifies that explicit q=1.0
// is handled correctly.
func TestLocaleQValue_ExplicitQ1SelectsCorrectly(t *testing.T) {
	locale := getActiveLocale(t, "ru;q=1.0,en;q=0.5")
	if locale != "ru" {
		t.Errorf("active_locale = %q, want 'ru' (explicit q=1.0)", locale)
	}
}

// TestLocaleQValue_ImplicitQ1BeatsSmallerQ verifies that a locale with no
// q-value (implicit 1.0) beats one with an explicit lower q-value.
func TestLocaleQValue_ImplicitQ1BeatsSmallerQ(t *testing.T) {
	// 'ru' has implicit q=1.0; 'en' has q=0.5 → ru wins
	locale := getActiveLocale(t, "ru,en;q=0.5")
	if locale != "ru" {
		t.Errorf("active_locale = %q, want 'ru' (implicit q=1.0 beats q=0.5)", locale)
	}
}

// TestLocaleQValue_Q0ExcludesLocale verifies that q=0 means "not acceptable"
// and the locale is excluded; the next supported locale wins.
func TestLocaleQValue_Q0ExcludesLocale(t *testing.T) {
	// ru;q=0 means ru is explicitly rejected; en is the only remaining supported locale.
	locale := getActiveLocale(t, "ru;q=0,en;q=0.5")
	if locale != "en" {
		t.Errorf("active_locale = %q, want 'en' (ru excluded by q=0)", locale)
	}
}

// =============================================================================
// Full sweep (Step 1-5 combined)
// =============================================================================

// TestLocaleQValue_FullVerification runs all five steps in a single test.
func TestLocaleQValue_FullVerification(t *testing.T) {
	cases := []struct {
		name       string
		header     string
		wantLocale string
	}{
		// Step 1+2: fr unsupported, ru is next highest
		{"fr_ru_en_qvalues", "fr;q=0.9,ru;q=0.8,en;q=0.5", "ru"},
		// Step 3: en-US region tag stripped → en
		{"en-US_regional", "en-US,en;q=0.9", "en"},
		// Step 4: ru-RU region stripped → ru
		{"ru-RU_regional", "ru-RU", "ru"},
		// Step 5: zh unsupported → fallback to en
		{"zh-CN_unsupported", "zh-CN", "en"},
		// Additional: explicit q=0 excluded
		{"ru_excluded_by_q0", "ru;q=0,en;q=0.5", "en"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locale := getActiveLocale(t, tc.header)
			if locale != tc.wantLocale {
				t.Errorf("Accept-Language: %q → active_locale=%q, want %q",
					tc.header, locale, tc.wantLocale)
			}
		})
	}
}

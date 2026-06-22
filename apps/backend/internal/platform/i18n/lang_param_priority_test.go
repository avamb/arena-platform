// lang_param_priority_test.go verifies feature #56:
// "?lang=invalid returns default locale, not error"
//
// Covers the NegotiateLocale priority chain at the unit level:
//   1. Invalid/unknown ?lang= falls back to Accept-Language then default
//   2. Valid ?lang=ru selects ru
//   3. Empty ?lang= falls through to Accept-Language
//   4. Valid ?lang= takes priority over Accept-Language (highest priority source)
package i18n

import "testing"

// =============================================================================
// Step 1: invalid ?lang= falls back to default 'en'
// =============================================================================

// TestLangParam_KlingonFallsToDefault verifies that an unknown locale tag
// passed as the ?lang= parameter is silently ignored and the default is used.
func TestLangParam_KlingonFallsToDefault(t *testing.T) {
	got := NegotiateLocale("", "klingon", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale(lang='klingon') = %q, want 'en' (klingon not supported)", got)
	}
}

// TestLangParam_UnsupportedFrFallsToDefault verifies that a valid BCP-47 tag
// that is not in the supported set ('fr') is silently ignored.
func TestLangParam_UnsupportedFrFallsToDefault(t *testing.T) {
	got := NegotiateLocale("", "fr", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale(lang='fr') = %q, want 'en' (fr not in supported)", got)
	}
}

// TestLangParam_InvalidNotError verifies that an unsupported ?lang= tag does NOT
// cause a panic or return an empty string — it must return the default locale.
func TestLangParam_InvalidNotError(t *testing.T) {
	got := NegotiateLocale("", "totally-invalid-tag", "", testDefault, testSupported)
	if got == "" {
		t.Error("NegotiateLocale(lang='totally-invalid-tag') returned empty string, want 'en'")
	}
	if got != "en" {
		t.Errorf("NegotiateLocale(lang='totally-invalid-tag') = %q, want 'en'", got)
	}
}

// TestLangParam_InvalidLangFallsToAcceptLanguage verifies that when ?lang= is
// invalid but Accept-Language contains a supported locale, Accept-Language wins.
func TestLangParam_InvalidLangFallsToAcceptLanguage(t *testing.T) {
	got := NegotiateLocale("ru", "klingon", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(acceptLang='ru', lang='klingon') = %q, want 'ru'", got)
	}
}

// =============================================================================
// Step 2: valid ?lang=ru selects ru
// =============================================================================

// TestLangParam_ValidRuSelectsRu verifies that ?lang=ru returns 'ru'.
func TestLangParam_ValidRuSelectsRu(t *testing.T) {
	got := NegotiateLocale("", "ru", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(lang='ru') = %q, want 'ru'", got)
	}
}

// TestLangParam_ValidEnSelectsEn verifies that ?lang=en returns 'en'.
func TestLangParam_ValidEnSelectsEn(t *testing.T) {
	got := NegotiateLocale("", "en", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale(lang='en') = %q, want 'en'", got)
	}
}

// TestLangParam_ValidLangWithRegionStripped verifies region tags in ?lang=
// are reduced to primary subtag (e.g. ru-RU → ru).
func TestLangParam_ValidLangWithRegionStripped(t *testing.T) {
	got := NegotiateLocale("", "ru-RU", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(lang='ru-RU') = %q, want 'ru' (region stripped)", got)
	}
}

// =============================================================================
// Step 3: empty ?lang= falls through to Accept-Language
// =============================================================================

// TestLangParam_EmptyFallsThroughToAcceptLanguage verifies that an empty
// string passed as ?lang= does not block the Accept-Language fallback.
func TestLangParam_EmptyFallsThroughToAcceptLanguage(t *testing.T) {
	got := NegotiateLocale("ru", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(acceptLang='ru', lang='') = %q, want 'ru'", got)
	}
}

// TestLangParam_EmptyFallsThroughToDefault verifies that empty ?lang= with no
// Accept-Language uses the default locale.
func TestLangParam_EmptyFallsThroughToDefault(t *testing.T) {
	got := NegotiateLocale("", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale(lang='', no header) = %q, want 'en'", got)
	}
}

// =============================================================================
// Step 4: valid ?lang= takes priority over Accept-Language
// =============================================================================

// TestLangParam_PriorityOverAcceptLanguage is the core step-4 test:
// Accept-Language: en + ?lang=ru → result must be 'ru'.
func TestLangParam_PriorityOverAcceptLanguage(t *testing.T) {
	got := NegotiateLocale("en", "ru", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(acceptLang='en', lang='ru') = %q, want 'ru' (?lang beats Accept-Language)", got)
	}
}

// TestLangParam_PriorityReverseCase verifies ?lang=en beats Accept-Language: ru.
func TestLangParam_PriorityReverseCase(t *testing.T) {
	got := NegotiateLocale("ru", "en", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale(acceptLang='ru', lang='en') = %q, want 'en' (?lang=en beats Accept-Language: ru)", got)
	}
}

// TestLangParam_PriorityWithQValues verifies ?lang= beats even a high-q
// Accept-Language header.
func TestLangParam_PriorityWithQValues(t *testing.T) {
	// Accept-Language: en;q=1.0 (highest possible) should be overridden by ?lang=ru
	got := NegotiateLocale("en;q=1.0", "ru", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(acceptLang='en;q=1.0', lang='ru') = %q, want 'ru'", got)
	}
}

// TestLangParam_InvalidLangDoesNotBlock verifies that an invalid ?lang= does
// not block Accept-Language resolution (i.e., it silently falls through).
func TestLangParam_InvalidLangDoesNotBlock(t *testing.T) {
	// ?lang=zz (unsupported) → fall through to Accept-Language: ru
	got := NegotiateLocale("ru", "zz", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale(acceptLang='ru', lang='zz') = %q, want 'ru'", got)
	}
}

// TestLangParam_FullPriorityChain verifies the complete priority chain:
// ?lang= > Accept-Language > preferred > default.
func TestLangParam_FullPriorityChain(t *testing.T) {
	// ?lang=ru wins (highest priority)
	got := NegotiateLocale("en", "ru", "en", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("full chain with lang=ru: got %q, want 'ru'", got)
	}

	// ?lang= invalid → Accept-Language wins
	got = NegotiateLocale("ru", "klingon", "en", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("full chain with invalid lang, acceptLang=ru: got %q, want 'ru'", got)
	}

	// ?lang= invalid, Accept-Language invalid → preferred wins
	got = NegotiateLocale("fr", "klingon", "ru", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("full chain with invalid lang+acceptLang, preferred=ru: got %q, want 'ru'", got)
	}

	// Everything invalid → default wins
	got = NegotiateLocale("fr", "klingon", "de", testDefault, testSupported)
	if got != "en" {
		t.Errorf("full chain all invalid: got %q, want 'en' (default)", got)
	}
}

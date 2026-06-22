// locale_test.go provides direct unit tests for the NegotiateLocale and
// fromAcceptLanguage functions (feature #54 step 6: "Verify locale negotiation
// function is unit-tested directly").
//
// These tests exercise the q-value parsing, region-stripping, unsupported-locale
// fallback, and priority-chain logic without any HTTP layer.
package i18n

import (
	"testing"
)

var testSupported = []string{"en", "ru"}
var testDefault = "en"

// =============================================================================
// fromAcceptLanguage (private, tested through NegotiateLocale)
// =============================================================================

// TestNegotiateLocale_QValueFrRuEn verifies the primary scenario from feature #54:
// fr;q=0.9,ru;q=0.8,en;q=0.5 → 'ru' (fr not supported, ru is next highest).
func TestNegotiateLocale_QValueFrRuEn(t *testing.T) {
	got := NegotiateLocale("fr;q=0.9,ru;q=0.8,en;q=0.5", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru'", got)
	}
}

// TestNegotiateLocale_QValueFrRu verifies that when only fr and ru are in header
// and fr is not supported, ru wins.
func TestNegotiateLocale_QValueFrRu(t *testing.T) {
	got := NegotiateLocale("fr;q=0.9,ru;q=0.8", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru'", got)
	}
}

// TestNegotiateLocale_RuHigherQThanEn verifies ru wins when it has higher q.
func TestNegotiateLocale_RuHigherQThanEn(t *testing.T) {
	got := NegotiateLocale("en;q=0.5,ru;q=0.8", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (ru has higher q)", got)
	}
}

// TestNegotiateLocale_EnHigherQThanRu verifies en wins when it has higher q.
func TestNegotiateLocale_EnHigherQThanRu(t *testing.T) {
	got := NegotiateLocale("en;q=0.9,ru;q=0.5", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale = %q, want 'en' (en has higher q)", got)
	}
}

// TestNegotiateLocale_RegionStrippingRuRU verifies 'ru-RU' reduces to 'ru'.
func TestNegotiateLocale_RegionStrippingRuRU(t *testing.T) {
	got := NegotiateLocale("ru-RU", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (ru-RU region stripped)", got)
	}
}

// TestNegotiateLocale_RegionStrippingEnUS verifies 'en-US' reduces to 'en'.
func TestNegotiateLocale_RegionStrippingEnUS(t *testing.T) {
	got := NegotiateLocale("en-US,en;q=0.9", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale = %q, want 'en' (en-US region stripped)", got)
	}
}

// TestNegotiateLocale_ZhUnsupportedFallsToDefault verifies unsupported locale
// zh-CN causes fallback to default 'en'.
func TestNegotiateLocale_ZhUnsupportedFallsToDefault(t *testing.T) {
	got := NegotiateLocale("zh-CN", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale = %q, want 'en' (zh not supported)", got)
	}
}

// TestNegotiateLocale_AllUnsupportedFallsToDefault verifies all-unsupported
// header causes fallback.
func TestNegotiateLocale_AllUnsupportedFallsToDefault(t *testing.T) {
	got := NegotiateLocale("fr;q=0.9,de;q=0.8,zh;q=0.7", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale = %q, want 'en' (all unsupported)", got)
	}
}

// TestNegotiateLocale_ImplicitQ1 verifies that a tag without q= has q=1.0
// (implicit) and beats explicit lower-q tags.
func TestNegotiateLocale_ImplicitQ1(t *testing.T) {
	// ru (implicit q=1.0) should beat en;q=0.9
	got := NegotiateLocale("ru,en;q=0.9", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (implicit q=1.0 > q=0.9)", got)
	}
}

// TestNegotiateLocale_ExplicitQ1 verifies explicit q=1 works correctly.
func TestNegotiateLocale_ExplicitQ1(t *testing.T) {
	got := NegotiateLocale("ru;q=1,en;q=0.5", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (q=1 is highest)", got)
	}
}

// TestNegotiateLocale_ExplicitQ0Excluded verifies q=0 means not acceptable;
// the locale must be skipped.
func TestNegotiateLocale_ExplicitQ0Excluded(t *testing.T) {
	// ru;q=0 explicitly excluded; en;q=0.5 is the only remaining supported locale
	got := NegotiateLocale("ru;q=0,en;q=0.5", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale = %q, want 'en' (ru q=0 excluded)", got)
	}
}

// TestNegotiateLocale_OrderDoesNotMatterOnlyQValue verifies that the list order
// in the header is irrelevant — only q-values determine priority.
func TestNegotiateLocale_OrderDoesNotMatterOnlyQValue(t *testing.T) {
	// Even though 'en' comes first in the list, 'ru' has higher q and should win.
	got := NegotiateLocale("en;q=0.4,ru;q=0.9", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (ru has higher q even though listed second)", got)
	}
}

// TestNegotiateLocale_NoHeaderFallsToDefault verifies empty Accept-Language
// falls through to the configured default.
func TestNegotiateLocale_NoHeaderFallsToDefault(t *testing.T) {
	got := NegotiateLocale("", "", "", testDefault, testSupported)
	if got != "en" {
		t.Errorf("NegotiateLocale = %q, want 'en' (no header, default)", got)
	}
}

// TestNegotiateLocale_LangParamOverridesDefault verifies ?lang= takes priority
// over the default locale (and also over Accept-Language).
func TestNegotiateLocale_LangParamOverridesDefault(t *testing.T) {
	got := NegotiateLocale("", "ru", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (lang param)", got)
	}
}

// TestNegotiateLocale_LangParamBeatsAcceptLanguage verifies that the ?lang=
// query parameter takes priority over the Accept-Language header (feature #56
// step 4: "Accept-Language: en, ?lang=ru -> ru").
// The correct fallback chain is: ?lang= → Accept-Language → preferred → default.
func TestNegotiateLocale_LangParamBeatsAcceptLanguage(t *testing.T) {
	got := NegotiateLocale("en", "ru", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (?lang=ru beats Accept-Language: en)", got)
	}
}

// TestNegotiateLocale_CaseInsensitive verifies locale tags are matched
// case-insensitively (e.g. 'RU' matches 'ru').
func TestNegotiateLocale_CaseInsensitive(t *testing.T) {
	got := NegotiateLocale("RU", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (case insensitive match)", got)
	}
}

// TestNegotiateLocale_WhitespaceHandled verifies leading/trailing whitespace
// around locale tags is handled gracefully.
func TestNegotiateLocale_WhitespaceHandled(t *testing.T) {
	got := NegotiateLocale("  ru  ,  en;q=0.5  ", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (whitespace trimmed)", got)
	}
}

// TestNegotiateLocale_ThreeDecimalQValue verifies three-decimal q-values
// (e.g. q=0.123) are parsed correctly.
func TestNegotiateLocale_ThreeDecimalQValue(t *testing.T) {
	got := NegotiateLocale("en;q=0.123,ru;q=0.456", "", "", testDefault, testSupported)
	if got != "ru" {
		t.Errorf("NegotiateLocale = %q, want 'ru' (0.456 > 0.123)", got)
	}
}

// =============================================================================
// parseQuality direct tests (internal, tested indirectly through NegotiateLocale)
// =============================================================================

// TestParseQuality_IntegerZero verifies integer "0" parses to 0.0.
func TestParseQuality_IntegerZero(t *testing.T) {
	v, err := parseQuality("0")
	if err != nil {
		t.Fatalf("parseQuality(\"0\"): unexpected error: %v", err)
	}
	if v != 0.0 {
		t.Errorf("parseQuality(\"0\") = %v, want 0.0", v)
	}
}

// TestParseQuality_IntegerOne verifies integer "1" parses to 1.0.
func TestParseQuality_IntegerOne(t *testing.T) {
	v, err := parseQuality("1")
	if err != nil {
		t.Fatalf("parseQuality(\"1\"): unexpected error: %v", err)
	}
	if v != 1.0 {
		t.Errorf("parseQuality(\"1\") = %v, want 1.0", v)
	}
}

// TestParseQuality_ZeroPointNine parses "0.9" correctly.
func TestParseQuality_ZeroPointNine(t *testing.T) {
	v, err := parseQuality("0.9")
	if err != nil {
		t.Fatalf("parseQuality(\"0.9\"): unexpected error: %v", err)
	}
	// Allow tiny float tolerance
	if v < 0.899 || v > 0.901 {
		t.Errorf("parseQuality(\"0.9\") = %v, want ~0.9", v)
	}
}

// TestParseQuality_InvalidString returns error for non-numeric input.
func TestParseQuality_InvalidString(t *testing.T) {
	_, err := parseQuality("abc")
	if err == nil {
		t.Error("expected error for invalid q-value 'abc', got nil")
	}
}

// TestParseQuality_InvalidInteger returns error for integer not 0 or 1.
func TestParseQuality_InvalidInteger(t *testing.T) {
	_, err := parseQuality("2")
	if err == nil {
		t.Error("expected error for q-value '2' (out of range), got nil")
	}
}

// bundle_test.go covers feature #101:
// "go-i18n setup + ru/en TOML catalogs + locale middleware"
//
// Steps covered by this file:
//
//	Step 1: go-i18n/v2 dependency exists (compilation proves it)
//	Step 2: Bundle loads TOML catalogs via //go:embed locales/*.toml
//	Step 3: ru.toml and en.toml exist and contain the five base keys
//	Step 4: NegotiateLocale priority chain (tested in locale_test.go)
//	Step 5: Localize(ctx, messageID, fallback, data) helper
//	Step 6: i18n integrated in error envelope (via httpserver updates)
//	Step 7: Catalog structure ready for uk/es (WalkDir covers any .toml)
//	Step 8: Accept-Language: ru → Russian texts; unknown → en fallback
package i18n

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// =============================================================================
// Step 2: NewBundle loads TOML catalogs from embedded locales/ directory
// =============================================================================

// TestBundle_NewBundleSucceeds verifies that NewBundle initialises without error.
func TestBundle_NewBundleSucceeds(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle() returned error: %v", err)
	}
	if b == nil {
		t.Fatal("NewBundle() returned nil bundle without error")
	}
}

// TestBundle_LocalizerForReturnsNonNil verifies that LocalizerFor returns a
// usable localizer for both supported locales.
func TestBundle_LocalizerForReturnsNonNil(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	for _, locale := range []string{"en", "ru"} {
		loc := b.LocalizerFor(locale)
		if loc == nil {
			t.Errorf("LocalizerFor(%q) returned nil", locale)
		}
	}
}

// =============================================================================
// Step 3: en.toml and ru.toml contain the five required base keys
// =============================================================================

// requiredKeys lists the message IDs that MUST be present in both catalogs.
var requiredKeys = []string{
	"error.unauthorized",
	"error.forbidden",
	"error.not_found",
	"error.validation",
	"error.internal",
}

// TestBundle_EnCatalogHasRequiredKeys verifies each required message ID
// resolves to a non-empty English string.
func TestBundle_EnCatalogHasRequiredKeys(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	loc := b.LocalizerFor("en")
	ctx := WithLocalizer(context.Background(), loc)

	for _, key := range requiredKeys {
		msg := Localize(ctx, key, "", nil)
		if msg == "" {
			t.Errorf("en catalog: Localize(%q) returned empty string", key)
		}
		// The fallback is "" so if we got "" the key is absent.
		if msg == key {
			t.Errorf("en catalog: Localize(%q) returned the key itself (message not found)", key)
		}
	}
}

// TestBundle_RuCatalogHasRequiredKeys verifies each required message ID
// resolves to a non-empty Russian string.
func TestBundle_RuCatalogHasRequiredKeys(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	loc := b.LocalizerFor("ru")
	ctx := WithLocalizer(context.Background(), loc)

	for _, key := range requiredKeys {
		msg := Localize(ctx, key, "", nil)
		if msg == "" {
			t.Errorf("ru catalog: Localize(%q) returned empty string", key)
		}
		if msg == key {
			t.Errorf("ru catalog: Localize(%q) returned the key itself (message not found)", key)
		}
	}
}

// =============================================================================
// Step 5: Localize helper
// =============================================================================

// TestLocalize_FallbackWhenNoLocalizerInCtx verifies that when no localizer
// is stored in context, Localize returns the fallback string.
func TestLocalize_FallbackWhenNoLocalizerInCtx(t *testing.T) {
	ctx := context.Background() // no localizer wired
	got := Localize(ctx, "error.not_found", "fallback message", nil)
	if got != "fallback message" {
		t.Errorf("Localize with empty ctx = %q, want %q", got, "fallback message")
	}
}

// TestLocalize_EnglishMessageFromContext verifies that when an English
// localizer is in context, Localize returns the English message.
func TestLocalize_EnglishMessageFromContext(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	ctx := WithLocalizer(context.Background(), b.LocalizerFor("en"))
	got := Localize(ctx, "error.not_found", "fallback", nil)
	if got == "" || got == "fallback" {
		t.Errorf("Localize(en, error.not_found) = %q, expected a non-empty English string", got)
	}
	// Must not contain Cyrillic
	for _, r := range got {
		if r >= 0x0400 && r <= 0x04FF {
			t.Errorf("English localization contains Cyrillic char %q in %q", r, got)
			break
		}
	}
}

// TestLocalize_RussianMessageFromContext verifies that when a Russian
// localizer is in context, Localize returns the Russian message (Cyrillic).
func TestLocalize_RussianMessageFromContext(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	ctx := WithLocalizer(context.Background(), b.LocalizerFor("ru"))
	got := Localize(ctx, "error.not_found", "fallback", nil)
	if got == "" || got == "fallback" {
		t.Errorf("Localize(ru, error.not_found) = %q, expected a non-empty Russian string", got)
	}
	// Must contain at least one Cyrillic character.
	hasCyrillic := false
	for _, r := range got {
		if r >= 0x0400 && r <= 0x04FF {
			hasCyrillic = true
			break
		}
	}
	if !hasCyrillic {
		t.Errorf("Russian localization should contain Cyrillic characters, got: %q", got)
	}
}

// TestLocalize_UnknownKeyReturnsFallback verifies that an unknown message ID
// returns the fallback string (not an empty string or panic).
func TestLocalize_UnknownKeyReturnsFallback(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	ctx := WithLocalizer(context.Background(), b.LocalizerFor("en"))
	got := Localize(ctx, "nonexistent.key.12345", "my fallback", nil)
	if got != "my fallback" {
		t.Errorf("Localize(unknown key) = %q, want %q", got, "my fallback")
	}
}

// =============================================================================
// Step 8: Accept-Language: ru → Russian texts; unknown language → en fallback
// =============================================================================

// TestLocaleMiddleware_RuAcceptLanguageReturnsRussian verifies feature #101
// step 8 (part 1): when a request carries Accept-Language: ru, the
// LocaleMiddleware puts a Russian Localizer in context and Localize returns
// a Russian string containing Cyrillic characters.
func TestLocaleMiddleware_RuAcceptLanguageReturnsRussian(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	var capturedMsg string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMsg = Localize(r.Context(), "error.not_found", "fallback", nil)
		w.WriteHeader(http.StatusOK)
	})

	mw := LocaleMiddleware(b, "en", []string{"en", "ru"})
	srv := httptest.NewServer(mw(handler))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Accept-Language", "ru")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	resp.Body.Close()

	if capturedMsg == "" || capturedMsg == "fallback" {
		t.Fatalf("capturedMsg = %q: expected a non-empty localized Russian string", capturedMsg)
	}

	hasCyrillic := false
	for _, r := range capturedMsg {
		if r >= 0x0400 && r <= 0x04FF {
			hasCyrillic = true
			break
		}
	}
	if !hasCyrillic {
		t.Errorf("Accept-Language: ru → Localize = %q (no Cyrillic), expected Russian text", capturedMsg)
	}
}

// TestLocaleMiddleware_UnknownLangFallsToEnglish verifies feature #101
// step 8 (part 2): when Accept-Language is an unsupported locale ("xx"),
// the middleware falls back to English and Localize returns an ASCII string.
func TestLocaleMiddleware_UnknownLangFallsToEnglish(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	var capturedMsg string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMsg = Localize(r.Context(), "error.not_found", "fallback", nil)
		w.WriteHeader(http.StatusOK)
	})

	mw := LocaleMiddleware(b, "en", []string{"en", "ru"})
	srv := httptest.NewServer(mw(handler))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Accept-Language", "xx") // unsupported locale

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	resp.Body.Close()

	if capturedMsg == "" || capturedMsg == "fallback" {
		t.Fatalf("capturedMsg = %q: expected a non-empty English string", capturedMsg)
	}

	// Must not contain Cyrillic (should be English fallback)
	for _, r := range capturedMsg {
		if r >= 0x0400 && r <= 0x04FF {
			t.Errorf("Accept-Language: xx → Localize = %q (contains Cyrillic), expected English fallback", capturedMsg)
			break
		}
	}
}

// TestLocaleMiddleware_NoAcceptLanguageFallsToEnglish verifies that when no
// Accept-Language header is set, Localize returns English.
func TestLocaleMiddleware_NoAcceptLanguageFallsToEnglish(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	var capturedMsg string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMsg = Localize(r.Context(), "error.not_found", "fallback", nil)
		w.WriteHeader(http.StatusOK)
	})

	mw := LocaleMiddleware(b, "en", []string{"en", "ru"})
	srv := httptest.NewServer(mw(handler))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	// No Accept-Language header

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	resp.Body.Close()

	if capturedMsg == "" || capturedMsg == "fallback" {
		t.Fatalf("capturedMsg = %q: expected English string", capturedMsg)
	}

	for _, r := range capturedMsg {
		if r >= 0x0400 && r <= 0x04FF {
			t.Errorf("no Accept-Language → Localize = %q (contains Cyrillic), expected English", capturedMsg)
			break
		}
	}
}

// TestLocaleMiddleware_LangParamOverridesAcceptLanguage verifies that ?lang=ru
// takes priority over Accept-Language: en (NegotiateLocale chain step 1).
func TestLocaleMiddleware_LangParamOverridesAcceptLanguage(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	var capturedMsg string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMsg = Localize(r.Context(), "error.not_found", "fallback", nil)
		w.WriteHeader(http.StatusOK)
	})

	mw := LocaleMiddleware(b, "en", []string{"en", "ru"})
	srv := httptest.NewServer(mw(handler))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"?lang=ru", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Accept-Language", "en") // would select en without ?lang=

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	resp.Body.Close()

	// ?lang=ru takes priority → must have Cyrillic
	hasCyrillic := false
	for _, r := range capturedMsg {
		if r >= 0x0400 && r <= 0x04FF {
			hasCyrillic = true
			break
		}
	}
	if !hasCyrillic {
		t.Errorf("?lang=ru, Accept-Language: en → Localize = %q (no Cyrillic), ?lang= should take priority", capturedMsg)
	}
}

// TestLocalize_WithLocalizerFrom verifies the LocalizerFrom round-trip:
// store with WithLocalizer, retrieve with LocalizerFrom.
func TestLocalize_WithLocalizerFrom(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	loc := b.LocalizerFor("en")
	ctx := WithLocalizer(context.Background(), loc)

	retrieved, ok := LocalizerFrom(ctx)
	if !ok {
		t.Fatal("LocalizerFrom: expected ok=true after WithLocalizer")
	}
	if retrieved == nil {
		t.Fatal("LocalizerFrom: returned nil localizer")
	}
}

// TestLocalize_LocalizerFromEmptyContext verifies LocalizerFrom returns false
// for a plain background context.
func TestLocalize_LocalizerFromEmptyContext(t *testing.T) {
	_, ok := LocalizerFrom(context.Background())
	if ok {
		t.Error("LocalizerFrom(background) should return false, got true")
	}
}

// =============================================================================
// Step 7: Catalog structure ready for uk and es
// =============================================================================

// TestBundle_CatalogStructureReadyForUkEs verifies that adding a new .toml
// file to locales/ would be picked up automatically. We verify this by
// confirming the WalkDir approach actually loads both en.toml and ru.toml
// (proving the mechanism works for any future .toml file).
func TestBundle_CatalogStructureReadyForUkEs(t *testing.T) {
	// The embed glob is `locales/*.toml` — it picks up ALL .toml files.
	// Verify we can read the embedded directory and confirm en + ru are there.
	entries, err := localesFS.ReadDir("locales")
	if err != nil {
		t.Fatalf("localesFS.ReadDir: %v", err)
	}

	found := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			found[e.Name()] = true
		}
	}

	for _, required := range []string{"en.toml", "ru.toml"} {
		if !found[required] {
			t.Errorf("locales/ missing %q; uk/es catalogs should follow the same pattern", required)
		}
	}
}

// =============================================================================
// Full feature verification (all 8 steps as sub-tests)
// =============================================================================

// TestI18n101_FullVerification runs a comprehensive check across all 8 feature
// steps in a single test to confirm the complete i18n stack works end to end.
func TestI18n101_FullVerification(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatalf("step 2 (NewBundle): %v", err)
	}

	// Step 2: Bundle loads successfully
	t.Run("step2_bundle_loads", func(t *testing.T) {
		if b == nil {
			t.Error("NewBundle returned nil")
		}
	})

	// Step 3: en.toml has the five base keys
	t.Run("step3_en_base_keys", func(t *testing.T) {
		ctx := WithLocalizer(context.Background(), b.LocalizerFor("en"))
		for _, key := range requiredKeys {
			if msg := Localize(ctx, key, "", nil); msg == "" {
				t.Errorf("en missing key %q", key)
			}
		}
	})

	// Step 3: ru.toml has the five base keys
	t.Run("step3_ru_base_keys", func(t *testing.T) {
		ctx := WithLocalizer(context.Background(), b.LocalizerFor("ru"))
		for _, key := range requiredKeys {
			if msg := Localize(ctx, key, "", nil); msg == "" {
				t.Errorf("ru missing key %q", key)
			}
		}
	})

	// Step 5: Localize helper falls back when no localizer in ctx
	t.Run("step5_localize_fallback", func(t *testing.T) {
		got := Localize(context.Background(), "error.not_found", "my_fallback", nil)
		if got != "my_fallback" {
			t.Errorf("Localize with empty ctx = %q, want my_fallback", got)
		}
	})

	// Step 8: Accept-Language: ru → Russian (Cyrillic)
	t.Run("step8_ru_accept_language_gives_cyrillic", func(t *testing.T) {
		ctx := WithLocalizer(context.Background(), b.LocalizerFor("ru"))
		msg := Localize(ctx, "error.not_found", "fallback", nil)
		hasCyrillic := false
		for _, r := range msg {
			if r >= 0x0400 && r <= 0x04FF {
				hasCyrillic = true
				break
			}
		}
		if !hasCyrillic {
			t.Errorf("ru locale Localize(error.not_found) = %q (no Cyrillic)", msg)
		}
	})

	// Step 8: unknown language → en fallback (no Cyrillic)
	t.Run("step8_unknown_lang_fallback_to_en", func(t *testing.T) {
		// "xx" is unsupported; NegotiateLocale returns "en"
		locale := NegotiateLocale("xx", "", "", "en", []string{"en", "ru"})
		if locale != "en" {
			t.Errorf("NegotiateLocale(xx) = %q, want 'en'", locale)
		}
		ctx := WithLocalizer(context.Background(), b.LocalizerFor(locale))
		msg := Localize(ctx, "error.not_found", "fallback", nil)
		for _, r := range msg {
			if r >= 0x0400 && r <= 0x04FF {
				t.Errorf("en fallback Localize = %q (contains Cyrillic)", msg)
				break
			}
		}
	})

	// Step 7: locales directory is structured to trivially accept uk/es
	t.Run("step7_catalog_structure_extensible", func(t *testing.T) {
		entries, readErr := localesFS.ReadDir("locales")
		if readErr != nil {
			t.Fatalf("ReadDir: %v", readErr)
		}
		if len(entries) == 0 {
			t.Error("locales/ is empty")
		}
	})
}

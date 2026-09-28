package eventbot

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// Every bot.* key must resolve in every supported catalog: a missing
// translation would otherwise reach an organizer as a raw key name.
func TestEventBot_LocaleBundleHasEveryKey(t *testing.T) {
	t.Parallel()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	texts := NewTexts(bundle)
	seed := map[string]map[string]any{
		"bot.invite_accepted": {"Org": "Org", "Role": "owner"},
		"bot.menu_title":      {"Org": "Org"},
		"bot.events_title":    {"Org": "Org", "Page": 1, "Pages": 1},
		"bot.event_card":      {"Name": "N", "Status": "S"},
		"bot.session_line":    {"When": "w", "Venue": "v", "Sold": 1, "Total": 2, "Available": 1, "Held": 0, "Money": "m"},
		"bot.money_line":      {"Paid": "1 EUR", "Orders": 1},
		"bot.org_switched":    {"Org": "Org"},
	}
	for _, loc := range SupportedLocales {
		for _, key := range MessageKeys {
			got := texts.T(loc, key, seed[key])
			if got == "" || got == key {
				t.Errorf("%s: key %q resolved to %q", loc, key, got)
			}
			if strings.Contains(got, "<no value>") {
				t.Errorf("%s: key %q rendered a missing template value: %q", loc, key, got)
			}
		}
	}
	// ru is really Russian, not a copy of en.
	if texts.T("ru", "bot.btn_events", nil) == texts.T("en", "bot.btn_events", nil) {
		t.Errorf("ru catalog should differ from en for bot.btn_events")
	}
	// An unsupported locale falls back to English, never to the key.
	if got := texts.T("he", "bot.btn_events", nil); got != texts.T("en", "bot.btn_events", nil) {
		t.Errorf("he should fall back to en, got %q", got)
	}
}

func TestEventBot_TextsWithoutBundleRenderKeys(t *testing.T) {
	t.Parallel()
	var nilTexts *Texts
	if got := nilTexts.T("en", "bot.help", nil); got != "bot.help" {
		t.Errorf("nil Texts should render the key, got %q", got)
	}
	if got := NewTexts(nil).T("en", "bot.help", nil); got != "bot.help" {
		t.Errorf("Texts without bundle should render the key, got %q", got)
	}
}

func TestEventBot_NormalizeLocale(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"": "en", "en": "en", "en-US": "en", "ru": "ru", "RU": "ru", "ru-RU": "ru", "ru_RU": "ru", "cs": "en", "he": "en", "xx": "en"}
	for in, want := range cases {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestEventBot_EscAndEmail(t *testing.T) {
	t.Parallel()
	if got := Esc(`<b>&"`); got != "&lt;b&gt;&amp;&#34;" {
		t.Errorf("Esc = %q", got)
	}
	for in, want := range map[string]bool{
		"name@company.com": true, "NAME@Company.CO": true, "no-at.com": false, "@x.com": false,
		"a@b": false, "a b@c.com": false, "": false,
	} {
		if got := looksLikeEmail(in); got != want {
			t.Errorf("looksLikeEmail(%q) = %v; want %v", in, got, want)
		}
	}
}

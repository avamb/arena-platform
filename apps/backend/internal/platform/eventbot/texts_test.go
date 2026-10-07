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
	// One bag of template values covers every templated key: a field a
	// message does not use is harmless, a missing one renders "<no value>".
	seed := map[string]any{
		"Org": "Org", "Role": "owner", "Page": 1, "Pages": 1, "Name": "N", "Status": "S", "Email": "a@b.c", "Marks": "",
		"When": "w", "Venue": "v", "Sold": 1, "Total": 2, "Available": 1, "Held": 0, "Money": "m",
		"Paid": "1 EUR", "Orders": 1, "Value": "x", "Label": "L", "Currency": "EUR", "N": 1, "Title": "t", "W": 1080, "H": 1350,
		"Reason": "r", "City": "c", "Capacity": 80, "Date": "d", "List": "l", "Price": "p",
		"Schedule": "", "Age": "18+", "Promoter": "", "Poster": "", "Published": "", "Link": "",
		"Warnings": "", "URL": "u", "State": "s", "Text": "tx", "Until": "d", "Limit": "5", "Raw": "r", "Tickets": "t", "Issued": 2, "Cancelled": 1, "Active": 1, "Min": "01.01.2026", "Max": "02.01.2026",
		"Old": "o", "New": "n", "Contact": "c", "Letters": "", "Time": "19:30", "Now": "now", "Word": "CANCEL", "Subject": "s", "Zone": "Europe/Madrid",
	}
	for _, loc := range SupportedLocales {
		for _, key := range MessageKeys {
			got := texts.T(loc, key, seed)
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
	cases := map[string]string{"": "en", "en": "en", "en-US": "en", "ru": "ru", "RU": "ru", "ru-RU": "ru", "ru_RU": "ru", "es": "es", "es-ES": "es", "ES": "es", "es_MX": "es", "cs": "en", "he": "en", "xx": "en"}
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

package eventbot

import (
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// A Telegram user whose language_code is uk gets Ukrainian, not en or ru.
func TestUkrainian_BotAnswersInUkrainian(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"uk": "uk", "UK": "uk", "uk-UA": "uk", "uk_UA": "uk"} {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q; want %q", in, got, want)
		}
	}
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	texts := NewTexts(bundle)
	for _, key := range []string{"bot.btn_events", "bot.menu_title", "bot.lang_choose", "bot.help", "bot.not_invited"} {
		uk := texts.T(NormalizeLocale("uk-UA"), key, nil)
		if uk == "" || uk == key {
			t.Fatalf("%s: no Ukrainian text", key)
		}
		if uk == texts.T("ru", key, nil) || uk == texts.T("en", key, nil) {
			t.Errorf("%s: the uk text equals ru or en: %q", key, uk)
		}
	}
	// The whole help screen is Ukrainian: it has a letter that Russian lacks.
	if help := texts.T("uk", "bot.help", nil); !strings.ContainsAny(help, "іїєґ") {
		t.Errorf("bot.help does not look Ukrainian: %q", help)
	}
}

func TestUkrainian_DatesAndMoney(t *testing.T) {
	t.Parallel()
	if got := DateWords("uk", "2026-10-20"); got != "вівторок, 20 жовтня 2026" {
		t.Errorf("DateWords(uk) = %q", got)
	}
	if got := FormatMoney(123450, "EUR", "uk"); got != "1 234,50 EUR" {
		t.Errorf("FormatMoney(uk) = %q", got)
	}
	today := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for raw, want := range map[string]string{
		"20 жовтня 2026": "2026-10-20",
		"5 січня":        "2027-01-05",
		"сьогодні":       "2026-11-18",
		"завтра":         "2026-11-19",
		"післязавтра":    "2026-11-20",
	} {
		got := DateCandidates(raw, today)
		if len(got) == 0 || got[0] != want {
			t.Errorf("DateCandidates(%q) = %v; want first %s", raw, got, want)
		}
	}
	if len(monthNames["uk"]) != 12 || dateLoc("uk") != "uk" {
		t.Errorf("uk date tables are not wired")
	}
}

func TestUkrainian_LanguageChooserOffersIt(t *testing.T) {
	t.Parallel()
	found := false
	for _, row := range languageRow().InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "lang:uk" && b.Text == "Українська" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("the language chooser has no Ukrainian button")
	}
}

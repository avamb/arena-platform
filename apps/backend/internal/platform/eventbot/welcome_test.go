package eventbot

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func welcomeTexts(t *testing.T) *Texts {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	return NewTexts(bundle)
}

// The first message a person reads after accepting the invitation says where
// they are and how to get to a published event, in every language, and names
// the buttons exactly as the menu below it labels them. Only the owner is told
// about inviting colleagues.
func TestWelcome_FirstMessageGivesTheSteps(t *testing.T) {
	texts := welcomeTexts(t)
	for _, loc := range SupportedLocales {
		newEvent := texts.T(loc, "bot.wz.new_event_btn", nil)
		myEvents := texts.T(loc, "bot.btn_events", nil)

		owner := texts.T(loc, "bot.invite_accepted", map[string]any{"Org": "Acme", "Role": "x", "Owner": true})
		manager := texts.T(loc, "bot.invite_accepted", map[string]any{"Org": "Acme", "Role": "x", "Owner": false})

		for name, text := range map[string]string{"owner": owner, "manager": manager} {
			if strings.Contains(text, "bot.") || strings.Contains(text, "<no value>") || !strings.Contains(text, "Acme") {
				t.Errorf("%s/%s renders badly:\n%s", loc, name, text)
			}
			for _, stale := range []string{"следующий шаг", "next step", "siguiente paso"} {
				if strings.Contains(text, stale) {
					t.Errorf("%s/%s still says %q:\n%s", loc, name, stale, text)
				}
			}
			// The three steps, and the labels of the two buttons they refer to.
			for _, n := range []string{"1.", "2.", "3."} {
				if !strings.Contains(text, n) {
					t.Errorf("%s/%s lacks step %q:\n%s", loc, name, n, text)
				}
			}
			if !strings.Contains(text, newEvent) || !strings.Contains(text, myEvents) {
				t.Errorf("%s/%s must name the buttons %q and %q:\n%s", loc, name, newEvent, myEvents, text)
			}
		}
		if len(owner) <= len(manager) {
			t.Errorf("%s: the owner's welcome must also mention inviting colleagues (owner %d, manager %d chars)", loc, len(owner), len(manager))
		}
	}
}

// Telegram caps the description of a bot at 512 characters and the short one
// at 120; a longer text is refused and the profile keeps its old words.
func TestWelcome_BotProfileTextsFitTelegramLimits(t *testing.T) {
	texts := welcomeTexts(t)
	for _, loc := range SupportedLocales {
		long := texts.T(loc, "bot.about_description", nil)
		short := texts.T(loc, "bot.about_short", nil)
		if long == "bot.about_description" || short == "bot.about_short" {
			t.Fatalf("%s: profile texts are missing", loc)
		}
		if n := utf8.RuneCountInString(long); n == 0 || n > 512 {
			t.Errorf("%s description is %d characters, Telegram allows 1..512", loc, n)
		}
		if n := utf8.RuneCountInString(short); n == 0 || n > 120 {
			t.Errorf("%s short description is %d characters, Telegram allows 1..120", loc, n)
		}
		if !strings.Contains(long, "Start") {
			t.Errorf("%s description must tell the person to press Start:\n%s", loc, long)
		}
	}
}

func TestLanguageRowOffersEverySupportedLanguage(t *testing.T) {
	row := languageRow().InlineKeyboard
	if len(row) != 1 {
		t.Fatalf("one row expected, got %d", len(row))
	}
	got := map[string]bool{}
	for _, b := range row[0] {
		got[strings.TrimPrefix(b.CallbackData, "lang:")] = true
	}
	for _, loc := range SupportedLocales {
		if !got[loc] {
			t.Errorf("the language row has no button for %q", loc)
		}
	}
}

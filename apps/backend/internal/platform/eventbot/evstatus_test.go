package eventbot

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The card shows exactly the buttons the lifecycle allows from the event's
// status, then Delete — and nothing to a role that may not change a status.
func TestEventStatusRows_FollowTheLifecycleAndTheRole(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ev := uuid.New()
	act := func(a string) string { return "ec:ev:" + ev.String() + ":" + a }
	owner := &Identity{Current: &Membership{Role: membershipRoleOwner}}
	manager := &Identity{Current: &Membership{Role: membershipRoleManager}}
	operator := &Identity{Current: &Membership{Role: "agent"}, Superadmin: true}
	agent := &Identity{Current: &Membership{Role: "agent"}}

	cases := []struct {
		status string
		want   []string
	}{
		{"draft", []string{act("pub"), act("del")}},
		{"published", []string{act("off"), act("arc"), act("del")}},
		{"cancelled", []string{act("arc"), act("del")}},
		{"archived", []string{act("del")}},
	}
	for _, id := range []*Identity{owner, manager, operator} {
		for _, c := range cases {
			rows := b.eventStatusRows("en", id, c.status, ev)
			var got []string
			for _, r := range rows {
				for _, btn := range r {
					got = append(got, btn.CallbackData)
					if len(btn.CallbackData) >= 64 {
						t.Errorf("callback data %q is %d bytes, Telegram allows 64", btn.CallbackData, len(btn.CallbackData))
					}
				}
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("status %s: buttons = %v, want %v", c.status, got, c.want)
			}
		}
	}
	for _, id := range []*Identity{agent, nil, {}} {
		if rows := b.eventStatusRows("en", id, "published", ev); len(rows) != 0 {
			t.Errorf("a role that cannot change a status got buttons: %+v", rows)
		}
	}
}

// Publish appears for a draft only; Delete is never a one-press action (the
// callback only asks), and the typed word is localized with the English one
// accepted everywhere.
func TestEvsDeleteWord(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for loc, word := range map[string]string{"en": "DELETE", "ru": "УДАЛИТЬ", "es": "ELIMINAR"} {
		if got := b.evsDeleteWord(loc); got != word {
			t.Errorf("%s: delete word = %q, want %q", loc, got, word)
		}
		for _, typed := range []string{word, strings.ToLower(word), "  " + word + " ", "«" + word + "»", word + "."} {
			if !b.evsIsDeleteWord(loc, typed) {
				t.Errorf("%s: %q must be accepted", loc, typed)
			}
		}
		if !b.evsIsDeleteWord(loc, "delete") || !b.evsIsDeleteWord(loc, "Delete") {
			t.Errorf("%s: the English word is accepted in any language", loc)
		}
		for _, typed := range []string{"", "yes", "да", "sí", "deleted", "delete it", "удалить всё", "cancel", "ОТМЕНИТЬ"} {
			if b.evsIsDeleteWord(loc, typed) {
				t.Errorf("%s: %q must not delete", loc, typed)
			}
		}
	}
}

// Every screen of the status buttons renders in every language, names the
// word where it asks for it, and the two confirmations say plainly that sold
// tickets stay valid.
func TestEvsScreensRenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	valid := map[string]string{"en": "stay valid", "ru": "остаются действительными", "es": "siguen siendo válidas"}
	for _, loc := range SupportedLocales {
		args := map[string]any{"Name": "Swan Lake", "N": 2, "Orders": 3, "Tickets": 7, "Word": b.evsDeleteWord(loc)}
		for _, key := range evsKeys {
			got := b.texts.T(loc, key, args)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s: %s rendered %q", loc, key, got)
			}
		}
		for _, key := range []string{"bot.evs.off_ask", "bot.evs.archive_ask", "bot.evs.off_done", "bot.evs.archived_done"} {
			if !strings.Contains(b.texts.T(loc, key, args), valid[loc]) {
				t.Errorf("%s: %s must say that sold tickets stay valid", loc, key)
			}
		}
		if ask := b.texts.T(loc, "bot.evs.delete_ask", args); !strings.Contains(ask, b.evsDeleteWord(loc)) || !strings.Contains(ask, "Swan Lake") {
			t.Errorf("%s: the delete question must name the event and the word: %s", loc, ask)
		}
		blocked := b.texts.T(loc, "bot.evs.delete_blocked", args)
		if !strings.Contains(blocked, "3") || !strings.Contains(blocked, "7") {
			t.Errorf("%s: the refusal must say the counts: %s", loc, blocked)
		}
	}
	// ru and es are really translated, not copies of en.
	for _, loc := range []string{"ru", "es"} {
		if b.texts.T(loc, "bot.evs.delete_btn", nil) == b.texts.T("en", "bot.evs.delete_btn", nil) {
			t.Errorf("%s: delete_btn is not translated", loc)
		}
	}
}

// The event name is HTML-escaped wherever it is printed.
func TestEvsNameIsEscaped(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	got := b.texts.T("en", "bot.evs.off_ask", map[string]any{"Name": Esc("<b>x</b>")})
	if strings.Contains(got, "<b>x</b>") || !strings.Contains(got, "&lt;b&gt;x") {
		t.Errorf("the name must be escaped: %s", got)
	}
}

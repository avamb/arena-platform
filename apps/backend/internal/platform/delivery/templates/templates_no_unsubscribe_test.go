package templates

import (
	"strings"
	"testing"
)

// unsubscribeWords lists the ways a letter can offer to leave a mailing list,
// in every language arena writes letters in (en, ru, es, cs, de, fr, he).
// Every letter built from these templates is transactional: a ticket, a
// change, a refund. Brevo blocks a recipient who unsubscribes from ALL later
// mail of the account, so a buyer who pressed such a link would stop getting
// his tickets and refund notices. All words are lower case.
var unsubscribeWords = []string{
	"unsubscribe", "opt out", "opt-out", // en
	"отписат", "отписк", "отказаться от подписки", "отказ от подписки", // ru
	"darse de baja", "darte de baja", "cancelar la suscripción", "cancelar suscripción", "anular la suscripción", // es
	"odhlásit", "odhlášení", "zrušit odběr", "zrušení odběru", // cs
	"abmelden", "abbestellen", "abmeldung", // de
	"désabonner", "désinscri", "desabonner", // fr
	"הסרה מרשימת", "להסיר מרשימת", "ביטול מנוי", "ביטול הרשמה", "הסר אותי", // he
}

// TestTransactionalTemplates_CarryNoUnsubscribe fails when any embedded
// letter template (HTML part or text part, any locale) offers an
// unsubscribe. The "write to the organizer" footer is a different thing and
// stays.
func TestTransactionalTemplates_CarryNoUnsubscribe(t *testing.T) {
	entries, err := templateFS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tmpl") {
			continue
		}
		raw, err := templateFS.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		checked++
		low := strings.ToLower(string(raw))
		for _, w := range unsubscribeWords {
			if strings.Contains(low, w) {
				t.Errorf("%s offers an unsubscribe (%q): every arena letter is transactional, "+
					"see AGENTS.md about Brevo's List-Unsubscribe", e.Name(), w)
			}
		}
	}
	if checked < 49 {
		t.Fatalf("only %d templates scanned, the guard is not looking at the letters", checked)
	}
}

// TestTransactionalTemplates_RenderedLettersCarryNoUnsubscribe renders every
// kind in every locale and checks the finished HTML and text, so a footer
// assembled from Data fields cannot slip an unsubscribe in either.
func TestTransactionalTemplates_RenderedLettersCarryNoUnsubscribe(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{
		TemplateKindTicket, TemplateKindInvitation, TemplateKindChange, TemplateKindCancel,
		TemplateKindRefund, TemplateKindRefundBlocked, TemplateKindScanException,
	}
	rendered := 0
	for _, kind := range kinds {
		for _, loc := range []string{"en", "ru", "es", "cs", "de", "fr", "he"} {
			out, err := r.Render(kind, loc, Data{})
			if err != nil {
				continue // a kind that needs richer data is covered by the file scan above
			}
			rendered++
			low := strings.ToLower(out.HTMLBody + "\n" + out.TextBody)
			for _, w := range unsubscribeWords {
				if strings.Contains(low, w) {
					t.Errorf("%s.%s renders an unsubscribe (%q)", kind, loc, w)
				}
			}
		}
	}
	if rendered == 0 {
		t.Fatal("no letter rendered, the guard checks nothing")
	}
}

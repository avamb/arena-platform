package eventbot

import (
	"context"
	"strings"
	"testing"
)

// The first question names two neutral examples (no product the organizers do
// not sell: owner feedback 2026-10-07) and says both ways in: type the name,
// or send a poster. The poster way is named only when the button is on screen.
func TestWizard_FirstQuestionIsNeutralAndExplainsBothWays(t *testing.T) {
	for _, loc := range []string{"ru", "en", "es"} {
		for _, posters := range []bool{true, false} {
			r := newWizardRun(t, newFakeRefs())
			r.w.WithPosterHints(posters)
			r.ws.Locale = loc
			r.d.Step = stEvName

			screen, err := r.w.Render(context.Background(), r.ws, r.d)
			if err != nil {
				t.Fatalf("%s posters=%v: %v", loc, posters, err)
			}
			text := strings.ToLower(screen.Text)

			for _, banned := range []string{"wine", "piedmont", "вин", "пьемонт", "vino", "cata"} {
				if strings.Contains(text, banned) {
					t.Errorf("%s posters=%v: the example must be neutral, found %q:\n%s", loc, posters, banned, screen.Text)
				}
			}
			example := "мадрид"
			posterWord := "афиш"
			switch loc {
			case "en":
				example, posterWord = "madrid", "poster"
			case "es":
				example, posterWord = "madrid", "cartel"
			}
			if !strings.Contains(text, example) {
				t.Errorf("%s posters=%v: want a Madrid example:\n%s", loc, posters, screen.Text)
			}

			hasButton := false
			for _, row := range screen.Buttons {
				for _, b := range row {
					if b.Data == "poster" {
						hasButton = true
					}
				}
			}
			if hasButton != posters {
				t.Errorf("%s posters=%v: poster button present=%v", loc, posters, hasButton)
			}
			// Both of the two ways are explained when the button is there; the
			// poster is not mentioned when it is not.
			if got := strings.Contains(text, posterWord); got != posters {
				t.Errorf("%s posters=%v: text mentions the poster=%v:\n%s", loc, posters, got, screen.Text)
			}
		}
	}
}

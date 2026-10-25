package eventbot

import (
	"context"
	"strings"
	"testing"
)

// UXH-02 (Hermes UX run, 24.10.2026): a poster that says Valencia made the
// country question offer "Keep: Latvia" as its first button. The poster's
// city now names the country first, and "Keep" stays below it for a poster
// that is wrong, or disappears when it is the same country.
func TestWizard_CountryQuestionPutsThePosterCityCountryFirst(t *testing.T) {
	render := func(t *testing.T, city string, keep RefItem) []Button {
		t.Helper()
		r := newWizardRun(t, newFakeRefs())
		r.ws.Defaults = Defaults{CountryID: keep.ID, CountryName: keep.Name}
		r.d.Step = stSCountry
		r.d.Hints.City = city
		screen, err := r.w.Render(context.Background(), r.ws, r.d)
		if err != nil {
			t.Fatal(err)
		}
		var flat []Button // the order a person reads them in
		for _, row := range screen.Buttons {
			flat = append(flat, row...)
		}
		return flat
	}
	czechia := RefItem{ID: "cz", Name: "Czechia"}
	spain := RefItem{ID: "es", Name: "Spain"}

	t.Run("poster city of another country comes before keep", func(t *testing.T) {
		rows := render(t, "Valencia", czechia)
		if len(rows) < 2 || rows[0].Data != "country:es" || !strings.Contains(rows[0].Label, "Spain") {
			t.Fatalf("first button = %+v, want the poster's Spain", rows)
		}
		var keepAt = -1
		for i, b := range rows {
			if b.Data == "keep" {
				keepAt = i
			}
		}
		if keepAt <= 0 {
			t.Fatalf("keep must stay, after the poster's answer: %+v", rows)
		}
	})

	t.Run("a Cyrillic city name works too", func(t *testing.T) {
		rows := render(t, "Валенсия", czechia)
		if rows[0].Data != "country:es" {
			t.Fatalf("first button = %+v", rows[0])
		}
	})

	t.Run("poster country equal to keep shows one button for it", func(t *testing.T) {
		rows := render(t, "Valencia", spain)
		if rows[0].Data != "country:es" {
			t.Fatalf("first button = %+v", rows[0])
		}
		for _, b := range rows {
			if b.Data == "keep" {
				t.Fatalf("keep repeats the poster's country: %+v", rows)
			}
		}
	})

	t.Run("no poster city keeps the old order", func(t *testing.T) {
		rows := render(t, "", czechia)
		if rows[0].Data != "keep" {
			t.Fatalf("first button = %+v, want keep", rows[0])
		}
	})

	t.Run("a city the platform's countries do not have changes nothing", func(t *testing.T) {
		rows := render(t, "Nowhereville Xyz", czechia)
		if rows[0].Data != "keep" {
			t.Fatalf("first button = %+v, want keep", rows[0])
		}
	})
}

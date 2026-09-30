package eventbot

import (
	"context"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

// The poster's reading becomes a "From the poster" button on every question
// it answers; pressing it is the same as typing the value, and a question
// the poster did not answer shows no such button. A poster sent with the
// first question is kept and the poster step then offers to keep it.
func TestWizard_PosterHintsAreButtonsThePersonConfirms(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.w.WithPosterHints(true)

	// The first question asks for the name and offers the poster as a button;
	// the button opens a screen of its own that names where the poster goes.
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen.Text, "Claude") || len(screen.Buttons) != 2 || screen.Buttons[0][0].Data != "poster" {
		t.Fatalf("first question without a poster: %q buttons=%v", screen.Text, screen.Buttons)
	}
	r.press("poster", stEvPosterAsk)
	screen, err = r.w.Render(context.Background(), r.ws, r.d)
	if err != nil || !strings.Contains(screen.Text, "Claude") {
		t.Fatalf("poster screen: %q %v", screen.Text, err)
	}
	r.text("просто текст", stEvPosterAsk) // a typed text is not a poster

	// The poster arrives: kept, back at the name question.
	note := r.step(WizInput{Poster: &PosterAccepted{MediaID: "m1", W: 1080, H: 1350}}, stEvName)
	if !strings.Contains(note, "1080×1350") || r.d.Event.PosterMediaID != "m1" {
		t.Fatalf("poster at the name step: note=%q media=%q", note, r.d.Event.PosterMediaID)
	}
	r.d.Hints = HintsFromFacts(posterread.Facts{
		Name: "Ночь открытой сцены", Age: "16+", Date: "2026-10-24", Time: "19:00",
		VenueName: "Test Hall Rika", City: "Madrid", Description: "Открытый микрофон.",
		Categories: []posterread.Category{{Name: "Партер", PriceMinor: 3490, Currency: "EUR"}},
	})

	hintLabel := func(step string) string {
		t.Helper()
		if r.d.Step != step {
			t.Fatalf("at %s, want %s", r.d.Step, step)
		}
		screen, err := r.w.Render(context.Background(), r.ws, r.d)
		if err != nil {
			t.Fatal(err)
		}
		if len(screen.Buttons) == 0 || len(screen.Buttons[0]) != 1 || screen.Buttons[0][0].Data != hintButtonData {
			return ""
		}
		return screen.Buttons[0][0].Label
	}

	if l := hintLabel(stEvName); !strings.Contains(l, "Ночь открытой сцены") {
		t.Fatalf("name hint = %q", l)
	}
	r.press(hintButtonData, stEvAge)
	if r.d.Event.Name != "Ночь открытой сцены" {
		t.Fatalf("name = %q", r.d.Event.Name)
	}
	if l := hintLabel(stEvAge); !strings.Contains(l, "16+") {
		t.Fatalf("age hint = %q", l)
	}
	r.press(hintButtonData, stEvPromoter)
	if r.d.Event.Age != "16+" {
		t.Fatalf("age = %q", r.d.Event.Age)
	}
	if l := hintLabel(stEvPromoter); l != "" {
		t.Fatalf("the promoter question must offer no poster hint, got %q", l)
	}
	r.press("prom:lmp", stEvPoster)

	// The poster is already in: keep it.
	screen, _ = r.w.Render(context.Background(), r.ws, r.d)
	if !strings.Contains(screen.Text, "1080×1350") || screen.Buttons[0][0].Data != "keep" {
		t.Fatalf("poster step with a poster: %q %v", screen.Text, screen.Buttons)
	}
	r.press("keep", stSDate)
	if r.d.Event.PosterMediaID != "m1" {
		t.Fatalf("keep dropped the poster")
	}

	if l := hintLabel(stSDate); !strings.Contains(l, "24.10.2026") {
		t.Fatalf("date hint = %q", l)
	}
	r.press(hintButtonData, stSTime)
	if r.d.Sessions[0].Date != "2026-10-24" {
		t.Fatalf("date = %q", r.d.Sessions[0].Date)
	}
	if l := hintLabel(stSTime); !strings.Contains(l, "19:00") {
		t.Fatalf("time hint = %q", l)
	}
	r.press(hintButtonData, stSCountry)
	r.press("country:es", stSCity)
	// Spain has no city in the fake: a new one, from the poster.
	r.press("city:new", stCityName)
	if l := hintLabel(stCityName); !strings.Contains(l, "Madrid") {
		t.Fatalf("city hint = %q", l)
	}
	r.press(hintButtonData, stSVenue)
	r.press("venue:new", stVName)
	if l := hintLabel(stVName); !strings.Contains(l, "Test Hall Rika") {
		t.Fatalf("venue hint = %q", l)
	}
	r.press(hintButtonData, stVAddress)
	r.press("skip", stVCapacity)
	r.text("120", stVTz) // Spain: the wizard asks the zone
	r.text("Europe/Madrid", stSMore)
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	if l := hintLabel(stTName); !strings.Contains(l, "Партер") {
		t.Fatalf("category hint = %q", l)
	}
	r.press(hintButtonData, stTPrice)
	if l := hintLabel(stTPrice); !strings.Contains(l, "34.90") {
		t.Fatalf("price hint = %q", l)
	}
	r.press(hintButtonData, stTChanges)
	if r.d.Tickets.Categories[0].Name != "Партер" || r.d.Tickets.Categories[0].PriceMinor != 3490 {
		t.Fatalf("category = %+v", r.d.Tickets.Categories[0])
	}
	r.press("no", stXDescription)
	if l := hintLabel(stXDescription); !strings.Contains(l, "Открытый микрофон") {
		t.Fatalf("description hint = %q", l)
	}
	r.press(hintButtonData, stXCurrency)
	if r.d.Event.Description != "Открытый микрофон." {
		t.Fatalf("description = %q", r.d.Event.Description)
	}
	if l := hintLabel(stXCurrency); !strings.Contains(l, "EUR") {
		t.Fatalf("currency hint = %q", l)
	}
	r.press(hintButtonData, stXPublish) // one channel: no channel question
	if r.d.Currency != "EUR" {
		t.Fatalf("currency = %q", r.d.Currency)
	}
	// A hint pressed where the poster has nothing is a no-op.
	r.d.Step = stXPublish
	if note := r.press(hintButtonData, stXPublish); note != "" {
		t.Fatalf("stray hint press: %q", note)
	}
}

// The note after a reading lists what was found, in the person's language,
// and says so when nothing was.
func TestWizard_HintsNote(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	h := HintsFromFacts(posterread.Facts{Name: "Концерт", Date: "2026-12-15", Categories: []posterread.Category{{Name: "Ticket", PriceMinor: 2500}}})
	note := r.w.hintsNote("ru", h)
	for _, want := range []string{"Концерт", "15.12.2026", "Ticket — 25 …", "С афиши"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lacks %q:\n%s", want, note)
		}
	}
	if none := r.w.hintsNote("en", DraftHints{}); !strings.Contains(none, "nothing") {
		t.Fatalf("empty note = %q", none)
	}
}

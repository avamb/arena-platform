package eventbot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventpage"
)

func pageReading() PageReading {
	return PageReading{
		Facts: eventpage.Facts{
			Name: "Вишнёвый сад", Age: "16+", Date: "2026-11-20", Time: "19:00",
			VenueName: "Teatro Kolybel", City: "Madrid", Country: "ES",
			Description: "Комедия Чехова.",
			Dates: []eventpage.DateFact{
				{Date: "2026-11-20", Time: "19:00", UTCOffset: "+01:00"},
				{Date: "2026-11-21", Time: "17:00"},
			},
			Categories: []eventpage.Category{{Name: "Партер", PriceMinor: 2500, Currency: "EUR"}},
			Image:      &eventpage.Image{MediaID: "img-1", Width: 1080, Height: 1350, FitsPoster: true},
		},
		Sources:  map[string]string{"name": "json_ld", "dates": "json_ld"},
		Warnings: []eventpage.Warning{{Code: eventpage.WarnDatesSkipped, Count: 3}},
	}
}

// "Fill in from a link": a button on the first question opens the link
// question; the bot reads the page and the wizard goes back to the name,
// whose hint is now the page's. Nothing is set before a press.
func TestWizard_PageLinkHintsAreButtonsThePersonConfirms(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.w.WithPosterHints(true).WithPageHints(true)

	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if len(screen.Buttons[0]) != 2 || screen.Buttons[0][0].Data != "poster" || screen.Buttons[0][1].Data != "link" {
		t.Fatalf("first question: %v", screen.Buttons)
	}
	r.press("link", stEvLinkAsk)
	if note := r.text("https://teatro.example.org/cerezos", stEvLinkAsk); note == "" {
		t.Fatal("a typed text at the link question is not a reading: it must say what to do")
	}
	r.d.Hints = HintsFromPage(pageReading())
	r.step(WizInput{PageRead: true}, stEvName)
	if r.d.Event.Name != "" {
		t.Fatal("reading a page must not fill the name by itself")
	}

	hint := func(step string) Button {
		t.Helper()
		if r.d.Step != step {
			t.Fatalf("at %s, want %s", r.d.Step, step)
		}
		s, err := r.w.Render(context.Background(), r.ws, r.d)
		if err != nil {
			t.Fatal(err)
		}
		return s.Buttons[0][0]
	}
	b := hint(stEvName)
	if b.Data != hintButtonData || !strings.Contains(b.Label, "Вишнёвый сад") || !strings.Contains(b.Label, "Со страницы") {
		t.Fatalf("name hint = %+v", b)
	}
	r.press(hintButtonData, stEvAge)
	r.press(hintButtonData, stEvPromoter)
	if r.d.Event.Name != "Вишнёвый сад" || r.d.Event.Age != "16+" {
		t.Fatalf("event = %+v", r.d.Event)
	}
	r.press("prom:lmp", stEvPoster)
	// The page's picture is a button at the poster question.
	b = hint(stEvPoster)
	if b.Data != pageImageData || !strings.Contains(b.Label, "1080×1350") {
		t.Fatalf("poster hint = %+v", b)
	}
	r.press(pageImageData, stSDate)
	if r.d.Event.PosterMediaID != "img-1" || r.d.Event.PosterW != 1080 {
		t.Fatalf("poster = %q %d", r.d.Event.PosterMediaID, r.d.Event.PosterW)
	}
	if b = hint(stSDate); !strings.Contains(b.Label, "20.11.2026") {
		t.Fatalf("first date hint = %+v", b)
	}
	// The second date question offers the page's second date.
	r.d.Cur = 1
	if label, _, _ := r.d.hintValue(); !strings.Contains(label, "21.11.2026") {
		t.Fatalf("second date hint = %q", label)
	}
	r.d.Step = stSTime
	if label, _, _ := r.d.hintValue(); label != "17:00" {
		t.Fatalf("second time hint = %q", label)
	}
	r.d.Cur = 2
	if label, _, _ := r.d.hintValue(); label != "" {
		t.Fatalf("a third date the page does not list has no hint, got %q", label)
	}
}

func TestHintsFromPage_PictureOnlyWhenItFitsThePoster(t *testing.T) {
	p := pageReading()
	p.Facts.Image.FitsPoster = false
	if h := HintsFromPage(p); h.Image != nil {
		t.Error("a picture that does not fit the poster rules is no hint")
	}
	h := HintsFromPage(pageReading())
	if h.Source != hintSourcePage || len(h.Dates) != 2 || h.Country != "ES" || len(h.Offsets) != 1 {
		t.Errorf("hints = %+v", h)
	}
}

func TestLooksLikePageURL(t *testing.T) {
	for s, want := range map[string]bool{
		"https://teatro.example.org/obra": true,
		"HTTP://x.org":                    true,
		"https://":                        false,
		"Концерт https://x.org":           false,
		"Вишнёвый сад":                    false,
		"www.teatro.es":                   false,
	} {
		if got := looksLikePageURL(s); got != want {
			t.Errorf("%q = %v", s, got)
		}
	}
}

func TestPageErrorKey(t *testing.T) {
	cases := map[string]string{
		"event_page.ticketing_platform":      "bot.wz.page_err_ticketing",
		"event_page.destination_not_allowed": "bot.wz.page_err_not_public",
		"event_page.nothing_found":           "bot.wz.page_err_nothing",
		"event_page.rate_limited":            "bot.wz.page_err_rate",
		"something.else":                     "bot.wz.page_err_unavailable",
	}
	for code, want := range cases {
		if got := pageErrorKey(&APIError{Status: 422, Code: code}); got != want {
			t.Errorf("%s -> %s", code, got)
		}
	}
	if got := pageErrorKey(errors.New("network")); got != "bot.wz.page_err_unavailable" {
		t.Errorf("network -> %s", got)
	}
}

func TestPageNote_ListsDatesAndWarnings(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	p := pageReading()
	note := r.w.pageNote(r.ws.Locale, HintsFromPage(p), p)
	for _, want := range []string{"Вишнёвый сад", "20.11.2026 19:00 (UTC+01:00)", "21.11.2026 17:00", "Партер", "1080×1350", "(3)"} {
		if !strings.Contains(note, want) {
			t.Errorf("note misses %q:\n%s", want, note)
		}
	}
}

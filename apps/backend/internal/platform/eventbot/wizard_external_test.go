package eventbot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// The very first question of a new event is "Who sells the tickets?", with the
// two answers as buttons, in every language the bot speaks.
func TestWizardExternal_FirstQuestionAsksWhoSells(t *testing.T) {
	want := map[string][2]string{
		"ru": {"Мы, на сайте и в виджете", "Партнёр"},
		"en": {"We do, on the site and in the widget", "A partner"},
		"es": {"Nosotros, en la web y en el widget", "Un socio"},
		"uk": {"Ми, на сайті й у віджеті", "Партнер"},
	}
	for loc, labels := range want {
		r := newWizardRun(t, newFakeRefs())
		r.ws.Locale = loc
		d := NewDraft()
		if d.Step != stEvSeller {
			t.Fatalf("a new draft starts at %s, want %s", d.Step, stEvSeller)
		}
		screen, err := r.w.Render(context.Background(), r.ws, d)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, row := range screen.Buttons {
			for _, b := range row {
				got[b.Data] = b.Label
			}
		}
		if got["seller:platform"] != labels[0] || got["seller:partner"] != labels[1] {
			t.Errorf("%s: buttons = %v, want %v", loc, got, labels)
		}
	}
}

// A typed text on the first question is not taken for the name: the person is
// told to press a button.
func TestWizardExternal_TextOnTheFirstQuestionIsRefused(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.d = NewDraft()
	note, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Text: "Концерт"})
	if err != nil || r.d.Step != stEvSeller || note == "" || r.d.Event.Name != "" {
		t.Fatalf("step=%s name=%q note=%q err=%v", r.d.Step, r.d.Event.Name, note, err)
	}
}

// "Partner": address, name and price "from", then the event's own questions
// with no ticket questions at all, and the bundle carries externalSale.
func TestWizardExternal_PartnerWalkSkipsTicketsAndBuildsTheBundle(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.d = NewDraft()
	r.press("seller:partner", stExtURL)
	r.text("http://eventim.de/x", stExtURL) // not https: asked again
	r.text("https://www.eventim.de/event/1", stExtName)
	r.text("   ", stExtName)
	r.text("Eventim", stExtPrice)
	r.text("дорого", stExtPrice)
	r.text("39,50", stEvName)
	if e := r.d.External; e == nil || e.URL != "https://www.eventim.de/event/1" || e.SellerName != "Eventim" || e.PriceFromMinor == nil || *e.PriceFromMinor != 3950 {
		t.Fatalf("external = %+v", r.d.External)
	}

	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	// No hall plan and no places question for a date a partner sells: the
	// venue answer lands on "one more date?".
	r.press("venue:akr", stSMore)
	r.press("next", stXDescription)
	r.text("Описание", stXCurrency)
	r.press("cur:EUR", stXPublish)
	r.press("pub:now", stSummary)

	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Продаёт партнёр: Eventim") {
		t.Errorf("the summary does not name the partner:\n%s", screen.Text)
	}
	if !strings.Contains(screen.Text, "https://www.eventim.de/event/1") || !strings.Contains(screen.Text, "39,50") {
		t.Errorf("the summary lacks the box office or the price:\n%s", screen.Text)
	}

	req := BuildBundle(r.d, 0, 0, "", "tg:ref:1")
	if len(req.CategoryList) != 0 {
		t.Fatalf("a partner date carries categories: %+v", req.CategoryList)
	}
	var sale bil24compat.ImportExternalSale
	if err := json.Unmarshal(req.ActionEvent.ExternalSale, &sale); err != nil {
		t.Fatalf("externalSale %s: %v", req.ActionEvent.ExternalSale, err)
	}
	if sale.URL != "https://www.eventim.de/event/1" || sale.SellerName != "Eventim" || sale.PriceFrom == nil || *sale.PriceFrom != 39.5 || sale.Currency != "EUR" {
		t.Fatalf("externalSale = %+v", sale)
	}
	kind, _, err := req.ActionEvent.ParseExternalSale()
	if err != nil || kind != bil24compat.ExternalSaleObject {
		t.Fatalf("kind=%v err=%v", kind, err)
	}
}

// The price is optional, and without it the wire block carries no price.
func TestWizardExternal_PriceCanBeSkipped(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.d = NewDraft()
	r.press("seller:partner", stExtURL)
	r.text("https://tickets.example.com/e", stExtName)
	r.text("Example Tickets", stExtPrice)
	r.press("skip", stEvName)
	r.d.Currency = "EUR"
	r.d.Sessions = []DraftSession{{Date: "2026-12-15", Time: "20:00", VenueID: "akr", VenueName: "Akropolis", Timezone: "Europe/Prague"}}
	var sale bil24compat.ImportExternalSale
	if err := json.Unmarshal(BuildBundle(r.d, 0, 0, "", "ref").ActionEvent.ExternalSale, &sale); err != nil || sale.PriceFrom != nil || sale.Currency != "" {
		t.Fatalf("externalSale = %+v err=%v", sale, err)
	}
}

// "We do" puts the platform back: no partner block, the ticket questions follow.
func TestWizardExternal_PlatformAnswerClearsThePartner(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.d = NewDraft()
	r.press("seller:partner", stExtURL)
	r.press("back", stEvSeller)
	r.press("seller:platform", stEvName)
	if r.d.External != nil {
		t.Fatalf("External = %+v after the platform answer", r.d.External)
	}
	r.d.Sessions = []DraftSession{{Date: "2026-12-15", Time: "20:00"}}
	if raw := BuildBundle(r.d, 0, 0, "", "ref").ActionEvent.ExternalSale; len(raw) != 0 {
		t.Fatalf("a platform date sends externalSale %s", raw)
	}
}

// A partner event read back from the API opens on its card without the ticket
// questions, shows the box office, and a change of it is a change to publish.
func TestWizardExternal_EditedEventShowsAndChangesTheBoxOffice(t *testing.T) {
	d := &Draft{Version: draftSchemaVersion, Mode: ModeEdit, Step: stEditMenu, Currency: "EUR", Publish: true,
		Event: DraftEvent{EventID: "ev", Name: "Show", Age: "16+"}}
	box := &DraftExternal{URL: "https://www.eventim.de/e", SellerName: "Eventim"}
	d.External = box.clone()
	d.Sessions = []DraftSession{{SessionID: "s1", Date: "2026-12-15", Time: "20:00", VenueID: "akr", VenueName: "Akropolis",
		External: box.clone(), Tickets: &DraftTickets{}}}
	d.Loaded = snapshotLoaded(d)

	r := newWizardRun(t, newFakeRefs())
	r.d = d
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Продаёт партнёр: Eventim") {
		t.Errorf("the card does not show the box office:\n%s", screen.Text)
	}
	if len(d.changes()) != 0 {
		t.Fatalf("a clean card lists changes: %+v", d.changes())
	}

	r.press("e:tickets", stExtURL)
	r.text("https://www.eventim.de/new", stExtName)
	r.text("Eventim DE", stExtPrice)
	r.press("skip", stEditMenu)
	if len(d.changes()) != 1 || d.changes()[0].Field != "external" || !d.Sessions[0].Dirty {
		t.Fatalf("changes = %+v dirty=%v", d.changes(), d.Sessions[0].Dirty)
	}
	req := BuildBundle(d, 0, 0, "", "")
	if !strings.Contains(string(req.ActionEvent.ExternalSale), "https://www.eventim.de/new") || req.ActionEvent.ArenaSessionID != "s1" {
		t.Fatalf("bundle = %+v", req.ActionEvent)
	}

	// Taking the change back leaves nothing to publish.
	r.press("e:undo:external", stEditMenu)
	if len(d.changes()) != 0 || d.Sessions[0].External.URL != "https://www.eventim.de/e" || d.Sessions[0].Dirty {
		t.Fatalf("after undo: changes=%+v session=%+v", d.changes(), d.Sessions[0])
	}
}

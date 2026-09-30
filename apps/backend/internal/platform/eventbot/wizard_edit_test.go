package eventbot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestParseCategoryEnd(t *testing.T) {
	type res struct {
		until string
		limit int
		ok    bool
	}
	cases := map[string]res{
		"31.12.2026":          {"2026-12-31", 0, true},
		"50":                  {"", 50, true},
		"1 200":               {"", 1200, true},
		"31.12.2026 50":       {"2026-12-31", 50, true},
		"50, 31.12.2026":      {"2026-12-31", 50, true},
		"31.12.2026; 50":      {"2026-12-31", 50, true},
		"":                    {"", 0, false},
		"banana":              {"", 0, false},
		"0":                   {"", 0, false},
		"31.12.2026 1.1.2027": {"", 0, false}, // two dates
		"5 6 7":               {"", 0, false},
	}
	for in, want := range cases {
		until, limit, ok := ParseCategoryEnd(in)
		if until != want.until || limit != want.limit || ok != want.ok {
			t.Errorf("ParseCategoryEnd(%q) = %q,%d,%v want %+v", in, until, limit, ok, want)
		}
	}
}

// loadedSingle opens the edit card of an event with one category.
func loadedSingle(t *testing.T) (*wizardRun, *Draft) {
	t.Helper()
	r, f := loadRun(t)
	s := f.sessions[0]
	tier := openapi.TicketTierItem{Id: uuid.New(), SessionId: s.Id, Name: "Вход", PriceAmount: 50000, Currency: "CZK", Capacity: i32p(250)}
	f.tiers[s.Id] = []openapi.TicketTierItem{tier}
	d, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	return r, d
}

func cardButtons(t *testing.T, r *wizardRun) string {
	t.Helper()
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(screen.Buttons)
}

// Editing is a set of short, separate screens: each part is changed on its
// own and the answer returns to the event's card, which marks what changed
// and offers "Publish changes" only once there is something to publish.
func TestEdit_EachPartIsOneScreenAndReturnsToTheCard(t *testing.T) {
	r, d := loadedSingle(t)
	if strings.Contains(cardButtons(t, r), "publish") {
		t.Fatal("nothing changed yet, but the card offers to publish")
	}

	// Name.
	r.press("e:name", stEvName)
	screen, _ := r.w.Render(context.Background(), r.ws, d)
	if !strings.Contains(screen.Text, "Загруженный концерт") || strings.Contains(fmt.Sprint(screen.Buttons), "poster") || !strings.Contains(fmt.Sprint(screen.Buttons), "e:home") || strings.Contains(fmt.Sprint(screen.Buttons), "cancel") {
		t.Fatalf("name screen = %q %v", screen.Text, screen.Buttons)
	}
	note := r.text("Новое имя", stEditMenu)
	if !strings.Contains(note, "Новое имя") {
		t.Fatalf("the card must confirm the change, note = %q", note)
	}
	if !strings.Contains(cardButtons(t, r), "publish") || !strings.Contains(cardButtons(t, r), "✎ Название") {
		t.Fatalf("card after a change: %s", cardButtons(t, r))
	}

	// Description: shown, replaced, removed.
	r.press("e:desc", stXDescription)
	screen, _ = r.w.Render(context.Background(), r.ws, d)
	if !strings.Contains(screen.Text, "Описание из Arena") || !strings.Contains(fmt.Sprint(screen.Buttons), "clear") {
		t.Fatalf("description screen = %q %v", screen.Text, screen.Buttons)
	}
	r.text("Свежий текст", stEditMenu)
	if d.Event.Description != "Свежий текст" {
		t.Fatalf("description = %q", d.Event.Description)
	}
	r.press("e:desc", stXDescription)
	r.press("clear", stEditMenu)
	if d.Event.Description != "" {
		t.Fatalf("description not cleared: %q", d.Event.Description)
	}

	// A poster replaced in the middle of the flow goes back to the card; the
	// name question of the card must not accept a stray poster.
	r.press("e:poster", stEvPoster)
	screen, _ = r.w.Render(context.Background(), r.ws, d)
	if strings.Contains(fmt.Sprint(screen.Buttons), "skip") || strings.Contains(fmt.Sprint(screen.Buttons), "keep") {
		t.Fatalf("poster screen offers skip/keep: %v", screen.Buttons)
	}
	r.step(WizInput{Poster: &PosterAccepted{MediaID: "m9", W: 1080, H: 1350}}, stEditMenu)
	if d.Event.PosterMediaID != "m9" {
		t.Fatalf("poster = %q", d.Event.PosterMediaID)
	}
	if waitsForPoster(&Draft{Mode: ModeEdit, Step: stEvName}) || !waitsForPoster(&Draft{Mode: ModeEdit, Step: stEvPoster}) || !waitsForPoster(&Draft{Mode: ModeCreate, Step: stEvPosterAsk}) {
		t.Fatal("waitsForPoster: a poster may only replace an existing event's poster on the poster screen")
	}

	for _, want := range []string{"name", "description", "poster"} {
		if !contains(d.Changed, want) {
			t.Fatalf("Changed = %v, lacks %q", d.Changed, want)
		}
	}
	if len(d.Changed) != 3 {
		t.Fatalf("Changed = %v, want each part once", d.Changed)
	}
}

// "Back to the event" leaves a part without applying it; the categories a
// re-entry had taken apart are put back and a half-entered date is dropped.
func TestEdit_BackToTheEventRestoresWhatWasHalfEdited(t *testing.T) {
	r, d := loadedSingle(t)
	before := append([]DraftCategory(nil), d.Tickets.Categories...)

	r.press("e:tickets", stTName)
	r.text("Другое имя", stTPrice)
	if len(d.Tickets.Categories) != 1 || d.Tickets.Categories[0].Name != "Другое имя" {
		t.Fatalf("categories while editing = %+v", d.Tickets.Categories)
	}
	r.press("e:home", stEditMenu)
	if len(d.Tickets.Categories) != 1 || d.Tickets.Categories[0] != before[0] {
		t.Fatalf("categories after going back = %+v, want %+v", d.Tickets.Categories, before)
	}
	if contains(d.Changed, "tickets") {
		t.Fatalf("going back must not mark the prices as changed: %v", d.Changed)
	}

	r.press("e:date", stSDate)
	r.text("20.12.2026", stSTime)
	r.press("e:home", stEditMenu)
	if len(d.Sessions) != 1 {
		t.Fatalf("a half-entered date survived: %+v", d.Sessions)
	}
	if contains(d.Changed, "dates") {
		t.Fatalf("dates marked changed without a new date: %v", d.Changed)
	}

	// A date that was finished stays, and is marked.
	r.press("e:date", stSDate)
	r.text("20.12.2026", stSTime)
	r.press("default", stSSame)
	r.press("same", stSMore)
	r.press("e:home", stEditMenu)
	if len(d.Sessions) != 2 || !contains(d.Changed, "dates") {
		t.Fatalf("finished date: sessions=%d changed=%v", len(d.Sessions), d.Changed)
	}
}

// Every screen of every edited part renders in both locales, with the way
// out being "back to the event".
func TestEdit_EveryScreenRendersInBothLocales(t *testing.T) {
	r, d := loadedSingle(t)
	for _, loc := range SupportedLocales {
		r.ws.Locale = loc
		for _, field := range []struct{ edit, step string }{
			{"name", stEvName}, {"age", stEvAge}, {"promoter", stEvPromoter}, {"poster", stEvPoster},
			{"description", stXDescription}, {"currency", stXCurrency},
		} {
			d.Scratch = DraftScratch{Edit: field.edit}
			d.Step = field.step
			screen, err := r.w.Render(context.Background(), r.ws, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", loc, field.edit, err)
			}
			if strings.Contains(screen.Text, "bot.wz.") || strings.Contains(screen.Text, "<no value>") || strings.Contains(screen.Text, "Шаг") {
				t.Fatalf("%s/%s renders badly:\n%s", loc, field.edit, screen.Text)
			}
			flat := fmt.Sprint(screen.Buttons)
			if !strings.Contains(flat, "e:home") || strings.Contains(flat, "cancel") {
				t.Fatalf("%s/%s buttons = %s", loc, field.edit, flat)
			}
		}
		d.Scratch = DraftScratch{}
		d.Step = stEditMenu
		d.Changed = []string{"name", "tickets"}
		screen, err := r.w.Render(context.Background(), r.ws, d)
		if err != nil || strings.Contains(screen.Text, "<no value>") || !strings.Contains(fmt.Sprint(screen.Buttons), "publish") {
			t.Fatalf("%s card: %q %v %v", loc, screen.Text, screen.Buttons, err)
		}
	}
}

// The current age and promoter are marked on their screens.
func TestEdit_CurrentAgeAndPromoterAreMarked(t *testing.T) {
	r, d := loadedSingle(t)
	r.press("e:age", stEvAge)
	if flat := cardButtons(t, r); !strings.Contains(flat, "✔ "+d.Event.Age) {
		t.Fatalf("age screen does not mark %q: %s", d.Event.Age, flat)
	}
	r.press("e:home", stEditMenu)
	d.Event.PromoterID = "lmp"
	r.press("e:promoter", stEvPromoter)
	if flat := cardButtons(t, r); !strings.Contains(flat, "✔ ") {
		t.Fatalf("promoter screen marks nothing: %s", flat)
	}
}

// "Cancel" on the card asks first; dropping an edit deletes it, keeping it
// leaves the card where it was.
func TestEdit_ExitAsksAndKeepsTheCard(t *testing.T) {
	r, d := loadedSingle(t)
	r.press("cancel", stCancel)
	screen, _ := r.w.Render(context.Background(), r.ws, d)
	if !strings.Contains(screen.Text, "правк") || !strings.Contains(fmt.Sprint(screen.Buttons), "Удалить изменения") {
		t.Fatalf("exit confirmation = %q %v", screen.Text, screen.Buttons)
	}
	r.press("cancel:no", stEditMenu)
	r.press("cancel", stCancel)
	if _, err := r.w.Apply(context.Background(), r.ws, d, WizInput{Data: "cancel:keep"}); err == nil || d.Step != stEditMenu {
		t.Fatalf("keep: err=%v step=%s", err, d.Step)
	}
}

// A new venue costs one question about places, not two, and it cannot be
// skipped: the number is the session's capacity.
func TestWizard_NewVenueAsksPlacesOnce(t *testing.T) {
	refs := newFakeRefs()
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	r.press("venue:new", stVName)
	r.text("Малый зал", stVAddress)
	r.press("skip", stVCapacity)
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	if strings.Contains(fmt.Sprint(screen.Buttons), "skip") || !strings.Contains(screen.Text, "Сколько мест продаём") {
		t.Fatalf("places question = %q %v", screen.Text, screen.Buttons)
	}
	if note := r.text("abc", stVCapacity); note == "" {
		t.Fatal("a non-number must be refused")
	}
	note := r.text("80", stSMore) // Czechia has one zone: the venue is created at once
	if !strings.Contains(note, "80") || r.d.Sessions[0].Capacity != 80 || r.d.Sessions[0].VenueName != "Малый зал" {
		t.Fatalf("session = %+v note=%q", r.d.Sessions[0], note)
	}
	if len(refs.created) != 1 || !strings.HasPrefix(refs.created[0], "venue:Малый зал@") || refs.venues[len(refs.venues)-1].Capacity != 80 {
		t.Fatalf("created = %v venues = %+v", refs.created, refs.venues)
	}
}

// A date that repeats an existing one (same time, same venue) sends the
// person back to the date question.
func TestWizard_FinishSessionRefusesAnExactRepeat(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	s := DraftSession{Date: "2026-12-15", Time: "20:00", VenueID: "akr", VenueName: "Akropolis", Capacity: 100}
	r.d.Sessions = []DraftSession{s, s}
	r.d.Cur = 1
	note, err := r.w.finishSession("ru", r.d)
	if err != nil || note == "" || r.d.Step != stSDate {
		t.Fatalf("repeat: note=%q err=%v step=%s", note, err, r.d.Step)
	}
}

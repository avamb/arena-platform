package eventbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// render draws the draft's current screen.
func render(t *testing.T, r *wizardRun) Screen {
	t.Helper()
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatalf("render at %s: %v", r.d.Step, err)
	}
	return screen
}

func changeKeys(d *Draft) string {
	keys := []string{}
	for _, c := range d.changes() {
		keys = append(keys, c.Key)
	}
	return strings.Join(keys, " ")
}

// addSameDate adds a date 18.12.2026 18:00 at the same venue with the
// categories of the date before.
func addSameDate(r *wizardRun) {
	r.t.Helper()
	r.press("e:date", stSDate)
	r.text("18.12.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	r.press("st:same", stSConfirm)
	r.press("sc:next", stSMore)
	r.press("next", stEditMenu)
}

// U-5b: every change on the card has its own undo, and an undone change is
// no longer a change — the card lists what is left and offers "Publish
// changes" only while something is left.
func TestUndo_EveryKindOfChangeGoesBackToWhatWasLoaded(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	loaded := *d.Loaded
	before1 := cloneTickets(*d.Sessions[0].Tickets)
	cap1 := d.Sessions[0].Capacity

	r.press("e:name", stEvName)
	r.text("Новое имя", stEditMenu)
	r.press("e:desc", stXDescription)
	r.text("Новый текст", stEditMenu)
	r.press("e:poster", stEvPoster)
	r.step(WizInput{Poster: &PosterAccepted{MediaID: "m9", W: 1080, H: 1350}}, stEditMenu)
	r.press("e:age", stEvAge)
	r.press("age:6+", stEditMenu)
	r.press("e:promoter", stEvPromoter)
	r.press("prom:org", stEditMenu)
	r.press("e:currency", stXCurrency)
	r.press("cur:EUR", stEditMenu)
	addSameDate(r)
	r.press("e:tickets", stTDate)
	r.press("td:1", stTCatName)
	r.text("Взрослый", stTCatPrice)
	r.text("60", stTCatPlaces)
	r.text("20", stTCatMore)
	r.press("more", stTCatName)
	r.text("Детский", stTCatPrice)
	r.text("30", stTCatPlaces)
	r.text("5", stTCatMore)
	r.press("done", stEditMenu)

	if got, want := changeKeys(d), "name description poster age promoter currency tickets:1 date:3"; got != want {
		t.Fatalf("changes = %q want %q", got, want)
	}
	screen := render(t, r)
	data := buttonsData(screen)
	for _, k := range strings.Fields("name description poster age promoter currency tickets:1 date:3") {
		if !strings.Contains(data, "e:undo:"+k) {
			t.Fatalf("card lacks the undo of %s: %s", k, data)
		}
	}
	for _, want := range []string{"Возраст: 16+ → 6+", "Название: «Загруженный концерт» → «Новое имя»", "Валюта: CZK → EUR",
		"Новая дата 18.12.2026 18:00", "Категории сеанса 15.12.2026 20:00", "Опубликовать изменения", "Выйти без публикации"} {
		if !strings.Contains(screen.Text, want) {
			t.Fatalf("card lacks %q:\n%s", want, screen.Text)
		}
	}

	note := r.press("e:undo:age", stEditMenu)
	if d.Event.Age != loaded.Event.Age || !strings.Contains(note, "Отменено: Возраст") || contains(d.Changed, "age") {
		t.Fatalf("age after undo = %q note=%q changed=%v", d.Event.Age, note, d.Changed)
	}
	r.press("e:undo:name", stEditMenu)
	r.press("e:undo:description", stEditMenu)
	r.press("e:undo:poster", stEditMenu)
	r.press("e:undo:promoter", stEditMenu)
	r.press("e:undo:currency", stEditMenu)
	if d.Event != loaded.Event || d.Currency != loaded.Currency {
		t.Fatalf("event after undo = %+v / %s, want %+v / %s", d.Event, d.Currency, loaded.Event, loaded.Currency)
	}

	r.press("e:undo:date:3", stEditMenu)
	if len(d.Sessions) != 2 {
		t.Fatalf("the added date survived its undo: %d dates", len(d.Sessions))
	}
	r.press("e:undo:tickets:1", stEditMenu)
	s1 := d.Sessions[0]
	if s1.Dirty || !ticketsEqual(*s1.Tickets, before1) || s1.Capacity != cap1 || !ticketsEqual(d.Tickets, before1) {
		t.Fatalf("date 1 after undo = %+v cap %d dirty %v", s1.Tickets, s1.Capacity, s1.Dirty)
	}

	if len(d.changes()) != 0 || len(d.Changed) != 0 {
		t.Fatalf("changes left: %v / %v", changeKeys(d), d.Changed)
	}
	screen = render(t, r)
	if strings.Contains(buttonsData(screen), "publish") || strings.Contains(buttonsData(screen), "e:undo:") {
		t.Fatalf("nothing is left to publish, card buttons = %s", buttonsData(screen))
	}
	// A stale "Publish changes" press with nothing left changes nothing.
	r.press("publish", stEditMenu)
	// An undo of a change that is gone does nothing either.
	if note := r.press("e:undo:age", stEditMenu); note != "" {
		t.Fatalf("stale undo note = %q", note)
	}
}

// Categories re-entered exactly as they were are not a change: the date is
// not sent again.
func TestUndo_SameCategoriesAgainAreNoChange(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:tickets", stTDate)
	r.press("td:2", stTCatName)
	r.text("Ребёнок от 6 лет + взрослый", stTCatPrice)
	r.text("50", stTCatPlaces)
	r.text("11", stTCatMore)
	r.press("more", stTCatName)
	r.text("Дополнительный детский", stTCatPrice)
	r.text("35", stTCatPlaces)
	r.text("3", stTCatMore)
	r.press("done", stEditMenu)
	if d.Sessions[1].Dirty || len(d.changes()) != 0 {
		t.Fatalf("same categories counted as a change: dirty=%v changes=%q", d.Sessions[1].Dirty, changeKeys(d))
	}
}

// After a save that stopped half way, the card does not offer undo: some of
// the changes are on the site already, and taking them back in the draft
// would not take them back there.
func TestUndo_NotOfferedAfterAPartSave(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:age", stEvAge)
	r.press("age:6+", stEditMenu)
	d.Saved.Sessions = []SavedSession{{Done: true}, {}}
	screen := render(t, r)
	if strings.Contains(buttonsData(screen), "e:undo:") || !strings.Contains(screen.Text, "Часть изменений уже на сайте") || !strings.Contains(buttonsData(screen), "publish") {
		t.Fatalf("card after a part save = %q %s", screen.Text, buttonsData(screen))
	}
	r.press("e:undo:age", stEditMenu)
	if d.Event.Age != "6+" {
		t.Fatal("undo worked after a part save")
	}
}

// A date changed after a save that stopped half way is sent again.
func TestSetDateTickets_ResetsThePartSave(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	d.Saved.Sessions = []SavedSession{{Done: true}, {Done: true}}
	r.press("e:tickets", stTDate)
	r.press("td:1", stTCatName)
	r.text("Взрослый", stTCatPrice)
	r.text("60", stTCatPlaces)
	r.text("20", stTCatMore)
	r.press("more", stTCatName)
	r.text("Детский", stTCatPrice)
	r.text("30", stTCatPlaces)
	r.text("5", stTCatMore)
	r.press("done", stEditMenu)
	if d.Saved.Sessions[0].Done || !d.Saved.Sessions[1].Done {
		t.Fatalf("saved = %+v, want date 1 to be sent again", d.Saved.Sessions)
	}
}

// "Publish changes" first shows what goes to the site; a renamed category is
// a new one, and the preview says the old one is closed. Only the preview's
// own button saves.
func TestPreview_SaysWhatGoesAndWarnsAboutARenamedCategory(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:tickets", stTDate)
	r.press("td:1", stTCatName)
	r.text("Ребёнок 4–7 лет + взрослый", stTCatPrice)
	r.text("50", stTCatPlaces)
	r.text("11", stTCatMore)
	r.press("more", stTCatName)
	r.text("Дополнительный детский", stTCatPrice)
	r.text("40", stTCatPlaces)
	r.text("3", stTCatMore)
	r.press("done", stEditMenu)
	r.press("publish", stPreview)
	screen := render(t, r)
	for _, want := range []string{"Что уйдёт на сайт", "Сеанс 15.12.2026 20:00, новые категории", "Не меняются: 16.12.2026 20:00",
		"категория «Ребёнок 4–6 лет + взрослый» будет закрыта", "будет создана «Ребёнок 4–7 лет + взрослый»"} {
		if !strings.Contains(screen.Text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, screen.Text)
		}
	}
	if strings.Contains(screen.Text, "«Дополнительный детский» будет закрыта") {
		t.Fatalf("a category kept by name is not closed:\n%s", screen.Text)
	}
	if data := buttonsData(screen); !strings.Contains(data, "publish:go") || !strings.Contains(data, "e:home") {
		t.Fatalf("preview buttons = %s", data)
	}
	r.press("e:home", stEditMenu)
	if !d.Sessions[0].Dirty {
		t.Fatal("going back from the preview dropped the change")
	}
	r.press("publish", stPreview)
	r.press("back", stEditMenu)
}

// A new date is summed up before "one more date?": its own categories,
// other places, away with it, or on.
func TestConfirmDate_OwnCategoriesPlacesAndRemove(t *testing.T) {
	r, d := loadedSingle(t)
	r.press("e:date", stSDate)
	r.text("18.12.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	r.press("st:same", stSConfirm)
	screen := render(t, r)
	if !strings.Contains(screen.Text, "как на сеансе 15.12.2026 20:00") || !strings.Contains(screen.Text, "250 мест") || !strings.Contains(buttonsData(screen), "sc:cap") {
		t.Fatalf("summary = %q %s", screen.Text, buttonsData(screen))
	}
	if !strings.Contains(screen.Text, "Всё верно") || !strings.Contains(strings.Join(buttonLabels(screen), "|"), "К карточке мероприятия") {
		t.Fatalf("summary text or way out = %q %v", screen.Text, buttonLabels(screen))
	}

	// Other places: the places question, then the summary again.
	r.press("sc:cap", stSCapacity)
	r.text("120", stSConfirm)
	if d.Sessions[1].Capacity != 120 {
		t.Fatalf("capacity = %d", d.Sessions[1].Capacity)
	}

	// Its own categories: the questions, then the summary again.
	r.press("sc:own", stTName)
	r.text("Свой вход", stTPrice)
	r.text("300", stTChanges)
	r.press("no", stSConfirm)
	screen = render(t, r)
	if d.Sessions[1].CopiedFrom != "" || strings.Contains(screen.Text, "как на сеансе") || !strings.Contains(screen.Text, "Свой вход") {
		t.Fatalf("own categories summary = %q", screen.Text)
	}
	if d.Sessions[0].Dirty || d.Sessions[0].Tickets.Categories[0].Name != "Вход" {
		t.Fatalf("date 1 changed: %+v", d.Sessions[0])
	}

	// Away with it.
	note := r.press("sc:drop", stSMore)
	if len(d.Sessions) != 1 || !strings.Contains(note, "18.12.2026 18:00") || !strings.Contains(note, "убран") {
		t.Fatalf("after remove: %d dates, note %q", len(d.Sessions), note)
	}
	r.press("next", stEditMenu)
	if len(d.changes()) != 0 || strings.Contains(buttonsData(render(t, r)), "publish") {
		t.Fatalf("a removed date left changes: %q", changeKeys(d))
	}
}

// A copy of the categories never ends after the new date: a later price
// change is dropped, a stage end later than the date is moved onto it.
func TestFitTicketsToDate(t *testing.T) {
	single := DraftTickets{Mode: ModeSingle, Categories: []DraftCategory{{Name: "Вход"}}, Schedule: []DraftPriceStep{
		{From: "2026-11-01", PriceMinor: 1}, {From: "2026-12-01", PriceMinor: 2}}}
	if n := fitTicketsToDate(&single, "2026-11-15"); n != 1 || len(single.Schedule) != 1 || single.Schedule[0].From != "2026-11-01" {
		t.Fatalf("single: n=%d schedule=%+v", n, single.Schedule)
	}
	seq := DraftTickets{Mode: ModeSequence, Categories: []DraftCategory{
		{Name: "Early", SellUntil: "2026-12-31", SellLimit: 20}, {Name: "Regular", SellUntil: "2026-11-01"}, {Name: "Last"}}}
	if n := fitTicketsToDate(&seq, "2026-11-15"); n != 1 || seq.Categories[0].SellUntil != "2026-11-15" || seq.Categories[0].SellLimit != 20 || seq.Categories[1].SellUntil != "2026-11-01" {
		t.Fatalf("sequence: n=%d cats=%+v", n, seq.Categories)
	}
	if n := fitTicketsToDate(&seq, ""); n != 0 {
		t.Fatalf("no date: n=%d", n)
	}
}

// "Same as the date before" trims the copy, and says so.
func TestSameTickets_TrimsPriceChangesAfterTheNewDate(t *testing.T) {
	r, f := loadRun(t)
	s := f.sessions[0]
	tier := openapi.TicketTierItem{Id: uuid.New(), SessionId: s.Id, Name: "Вход", PriceAmount: 50000, Currency: "CZK", Capacity: i32p(250)}
	f.tiers[s.Id] = []openapi.TicketTierItem{tier}
	f.windows[tier.Id] = []openapi.TierPriceWindow{
		{Id: uuid.New(), TierId: tier.Id, ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, prague).UTC(), PriceAmount: 60000},
		{Id: uuid.New(), TierId: tier.Id, ValidFrom: time.Date(2026, 12, 1, 0, 0, 0, 0, prague).UTC(), PriceAmount: 70000},
	}
	d, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	r.press("e:date", stSDate)
	r.text("15.11.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	note := r.press("st:same", stSConfirm)
	if got := d.Sessions[1].Tickets.Schedule; len(got) != 1 || got[0].From != "2026-11-01" {
		t.Fatalf("copied schedule = %+v", got)
	}
	if len(d.Sessions[0].Tickets.Schedule) != 2 {
		t.Fatalf("the source date lost its schedule: %+v", d.Sessions[0].Tickets.Schedule)
	}
	if !strings.Contains(note, "15.11.2026") {
		t.Fatalf("trim note = %q", note)
	}
}

// A date read back with no category is not sent for a new currency (a bundle
// with no category describes nothing); the card says it has none and the
// preview warns.
func TestSendPlan_SkipsADateWithoutCategoriesOnACurrencyChange(t *testing.T) {
	x := loadTwoDates(t)
	x.f.tiers[x.s2.Id] = nil
	d, _, err := x.r.w.LoadEventDraft(context.Background(), x.f, x.r.ws, x.f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	r := x.r
	r.d = d
	if text := render(t, r).Text; !strings.Contains(text, "16.12.2026 20:00: без категорий") {
		t.Fatalf("card does not say the date has no categories:\n%s", text)
	}
	r.press("e:currency", stXCurrency)
	r.press("cur:EUR", stEditMenu)
	if got := d.sendPlan(); !got[0] || got[1] {
		t.Fatalf("plan = %v", got)
	}
	r.press("publish", stPreview)
	if text := render(t, r).Text; !strings.Contains(text, "У сеанса 16.12.2026 20:00 нет категорий") {
		t.Fatalf("preview does not warn:\n%s", text)
	}
}

// "For all dates" says before the press that the first date's way of selling
// becomes every date's.
func TestTicketsDate_ForAllNamesTheMode(t *testing.T) {
	x := loadTwoDates(t)
	x.r.press("e:tickets", stTDate)
	if text := render(t, x.r).Text; !strings.Contains(text, "даёт всем сеансам способ продажи первого") || !strings.Contains(text, "одновременно") {
		t.Fatalf("date choice:\n%s", text)
	}
}

// Leaving an edit lists what the draft holds before "keep" and "delete".
func TestCancel_EditListsTheDraft(t *testing.T) {
	r, d := loadedSingle(t)
	r.press("cancel", stCancel)
	if text := render(t, r).Text; !strings.Contains(text, "В черновике изменений нет") {
		t.Fatalf("empty draft exit = %q", text)
	}
	r.press("cancel:no", stEditMenu)
	r.press("e:age", stEvAge)
	r.press("age:6+", stEditMenu)
	addSameDate(r)
	r.press("cancel", stCancel)
	screen := render(t, r)
	if !strings.Contains(screen.Text, "В черновике:") || !strings.Contains(screen.Text, "Возраст: 16+ → 6+") || !strings.Contains(screen.Text, "Новая дата 18.12.2026 18:00") {
		t.Fatalf("exit screen = %q", screen.Text)
	}
	if got := buttonsData(screen); got != "cancel:keep cancel:drop cancel:no" {
		t.Fatalf("exit buttons = %q", got)
	}
	_ = d
}

// The import's "categories closed" warning reaches the organizer in words.
func TestWarningText_TierNotInPayload(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	if r.w.warningText("ru", "import.tier_not_in_payload") == "" {
		t.Fatal("no text for import.tier_not_in_payload")
	}
}

// The ticket questions of an edited event say which step of how many.
func TestTicketStepNo(t *testing.T) {
	cases := []struct {
		mode, step string
		n, total   int
	}{
		{ModeSingle, stTName, 1, 3}, {ModeSingle, stTPrice, 2, 3}, {ModeSingle, stTChangeDate, 3, 3},
		{ModeParallel, stTCatName, 1, 4}, {ModeParallel, stTCatPlaces, 3, 4}, {ModeParallel, stTCatMore, 4, 4},
		{ModeSequence, stTCatPrice, 2, 3}, {ModeSequence, stTCatUntil, 3, 3}, {ModeSequence, stTCatLast, 3, 3},
	}
	for _, c := range cases {
		if n, total := ticketStepNo(c.mode, c.step); n != c.n || total != c.total {
			t.Errorf("%s/%s = %d of %d, want %d of %d", c.mode, c.step, n, total, c.n, c.total)
		}
	}
}

// Several categories closed at once are named together, in the plural.
func TestPreview_SeveralClosedCategories(t *testing.T) {
	x := loadTwoDates(t)
	r := x.r
	r.press("e:tickets", stTDate)
	r.press("td:1", stTCatName)
	r.text("Взрослый", stTCatPrice)
	r.text("60", stTCatPlaces)
	r.text("20", stTCatMore)
	r.press("more", stTCatName)
	r.text("Детский", stTCatPrice)
	r.text("30", stTCatPlaces)
	r.text("5", stTCatMore)
	r.press("done", stEditMenu)
	r.press("publish", stPreview)
	text := render(t, r).Text
	if !strings.Contains(text, "категории «Ребёнок 4–6 лет + взрослый», «Дополнительный детский» будут закрыты") || !strings.Contains(text, "появятся новые: «Взрослый», «Детский»") {
		t.Fatalf("preview:\n%s", text)
	}
}

func buttonLabels(s Screen) []string {
	out := []string{}
	for _, row := range s.Buttons {
		for _, b := range row {
			out = append(out, b.Label)
		}
	}
	return out
}

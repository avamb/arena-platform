package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// fakeRefs is an in-memory RefIO with two countries, one city, one venue,
// one promoter and a configurable number of channels.
type fakeRefs struct {
	countries []RefItem
	cities    map[string][]RefItem
	venues    []VenueRef
	promoters []RefItem
	channels  []RefItem
	created   []string
}

func newFakeRefs() *fakeRefs {
	return &fakeRefs{
		countries: []RefItem{{ID: "cz", Name: "Czechia", ISO2: "CZ", Currency: "CZK"}, {ID: "es", Name: "Spain", ISO2: "ES", Currency: "EUR"}},
		cities:    map[string][]RefItem{"cz": {{ID: "prg", Name: "Prague", ISO2: "CZ"}}, "es": {}},
		venues:    []VenueRef{{ID: "akr", Name: "Palác Akropolis", CityID: "prg", CountryISO2: "CZ", Timezone: "Europe/Prague", Capacity: 300}},
		promoters: []RefItem{{ID: "lmp", Name: "Lampyris s.r.o."}},
		channels:  []RefItem{{ID: "ch1", Name: "lampyrisevents.com"}},
	}
}

func (f *fakeRefs) Countries(context.Context, string, string) ([]RefItem, error) {
	return f.countries, nil
}
func (f *fakeRefs) Cities(_ context.Context, _ string, countryID, _ string) ([]RefItem, error) {
	return f.cities[countryID], nil
}
func (f *fakeRefs) Venues(context.Context, string, uuid.UUID) ([]VenueRef, error) {
	return f.venues, nil
}
func (f *fakeRefs) Promoters(context.Context, string, uuid.UUID) ([]RefItem, error) {
	return f.promoters, nil
}
func (f *fakeRefs) Channels(context.Context, string, uuid.UUID) ([]RefItem, error) {
	return f.channels, nil
}
func (f *fakeRefs) CreateCity(_ context.Context, _ string, _ uuid.UUID, countryID, name, _ string) (RefItem, error) {
	c := RefItem{ID: "city-" + strings.ToLower(name), Name: name}
	f.cities[countryID] = append(f.cities[countryID], c)
	f.created = append(f.created, "city:"+name)
	return c, nil
}
func (f *fakeRefs) CreateVenue(_ context.Context, _ string, _ uuid.UUID, v VenueCreate) (VenueRef, error) {
	r := VenueRef{ID: "venue-" + strings.ToLower(v.Name), Name: v.Name, CityID: v.CityID, CountryISO2: v.CountryISO2, Timezone: v.Timezone, Capacity: v.Capacity}
	f.venues = append(f.venues, r)
	f.created = append(f.created, "venue:"+v.Name+"@"+v.Timezone)
	return r, nil
}
func (f *fakeRefs) CreatePromoter(_ context.Context, _ string, _ uuid.UUID, name, legalID string) (RefItem, error) {
	p := RefItem{ID: "prom-" + strings.ToLower(name), Name: name}
	f.promoters = append(f.promoters, p)
	f.created = append(f.created, "promoter:"+name+"/"+legalID)
	return p, nil
}

type wizardRun struct {
	t  *testing.T
	w  *Wizard
	ws WizSession
	d  *Draft
}

func newWizardRun(t *testing.T, refs RefIO) *wizardRun {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	w := NewWizard(NewTexts(bundle), refs)
	w.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) } // the tests' "today"
	return &wizardRun{t: t, w: w, ws: WizSession{JWT: "jwt", OrgID: uuid.New(), Locale: "ru"}, d: NewDraft()}
}

// step feeds one answer and asserts the step the draft lands on.
func (r *wizardRun) step(in WizInput, want string) string {
	r.t.Helper()
	note, err := r.w.Apply(context.Background(), r.ws, r.d, in)
	if err != nil {
		r.t.Fatalf("Apply(%+v) at %s: %v", in, r.d.Step, err)
	}
	if r.d.Step != want {
		r.t.Fatalf("after %+v: step=%s want %s (note %q)", in, r.d.Step, want, note)
	}
	if _, err := r.w.Render(context.Background(), r.ws, r.d); err != nil {
		r.t.Fatalf("Render at %s: %v", r.d.Step, err)
	}
	return note
}

// text types an answer. A date typed at a date question must be confirmed, so
// when the wizard offers readings the first one is pressed, the way a person
// would. A date refused as out of range leaves the step where it was, and the
// note comes back.
func (r *wizardRun) text(s, want string) string {
	r.t.Helper()
	note, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Text: s})
	if err != nil {
		r.t.Fatalf("Apply(%q) at %s: %v", s, r.d.Step, err)
	}
	if len(r.d.Scratch.DatePending) > 0 {
		return r.step(WizInput{Data: confirmPrefix + r.d.Scratch.DatePending[0]}, want)
	}
	if r.d.Step != want {
		r.t.Fatalf("after %q: step=%s want %s (note %q)", s, r.d.Step, want, note)
	}
	if _, err := r.w.Render(context.Background(), r.ws, r.d); err != nil {
		r.t.Fatalf("Render at %s: %v", r.d.Step, err)
	}
	return note
}
func (r *wizardRun) press(s, want string) string { return r.step(WizInput{Data: s}, want) }

// eventHead walks name → age → promoter (existing) → poster (skip).
func (r *wizardRun) eventHead() {
	r.t.Helper()
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvPoster)
	r.press("skip", stSDate)
}

// firstDate walks date → time → country → city → venue → capacity.
func (r *wizardRun) firstDate() {
	r.t.Helper()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	r.press("venue:akr", stSCapacity)
	r.press("keep", stSMore)
}

// The wizard names the organization ("Arena Test Promotions", never "your
// organization"), tells the venue's capacity with the venue, and the
// currency with the price question — the three things the first organizer
// to use it stumbled on (2026-09-29).
func TestWizard_ScreensNameTheOrgVenueCapacityAndCurrency(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.ws.OrgName = "Arena Test Promotions"
	r.text("Ночь открытой сцены", stEvAge)
	r.press("age:16+", stEvPromoter)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Arena Test Promotions") {
		t.Errorf("promoter question does not name the organization:\n%s", screen.Text)
	}
	if !strings.Contains(fmt.Sprint(screen.Buttons), "Arena Test Promotions") {
		t.Errorf("no button carries the organization's name: %v", screen.Buttons)
	}
	if note := r.press("prom:org", stEvPoster); !strings.Contains(note, "Промоутер: Arena Test Promotions") {
		t.Errorf("ack = %q, want the organization by name", note)
	}
	r.press("skip", stSDate)
	r.text("01.01.2027", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	if note := r.press("venue:akr", stSCapacity); !strings.Contains(note, "Palác Akropolis, вместимость 300") {
		t.Errorf("venue ack = %q, want the capacity with it", note)
	}
	screen, _ = r.w.Render(context.Background(), r.ws, r.d)
	if !strings.Contains(screen.Text, "вмещает 300") {
		t.Errorf("capacity question does not tell the venue's capacity:\n%s", screen.Text)
	}
	r.press("keep", stSMore)
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	r.press("default", stTPrice)
	screen, _ = r.w.Render(context.Background(), r.ws, r.d)
	if !strings.Contains(screen.Text, "в CZK") {
		t.Errorf("price question does not name the currency:\n%s", screen.Text)
	}
}

func TestWizard_SingleCategoryWithSchedule_HappyPath(t *testing.T) {
	refs := newFakeRefs()
	r := newWizardRun(t, refs)
	r.eventHead()
	r.firstDate()
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	r.press("default", stTPrice)
	r.text("199", stTChanges)
	r.press("yes", stTChangeDate)
	r.text("01.11.2026", stTChangePrice)
	r.text("249,50", stTChangeMore)
	r.press("more", stTChangeDate)
	r.text("01.10.2026", stTChangeDate) // earlier than the previous step: refused
	r.text("01.12.2026", stTChangePrice)
	r.text("299", stTChangeMore)
	r.press("done", stXDescription)
	r.text("Описание", stXCurrency)
	r.press("cur:CZK", stXPublish) // one channel: chosen silently
	r.press("pub:now", stSummary)

	d := r.d
	if d.Event.Name != "Концерт" || d.Event.Age != "16+" || d.Event.PromoterID != "lmp" {
		t.Fatalf("event = %+v", d.Event)
	}
	if len(d.Sessions) != 1 || d.Sessions[0].Date != "2026-12-15" || d.Sessions[0].Time != "20:00" || d.Sessions[0].Capacity != 300 || d.Sessions[0].Timezone != "Europe/Prague" {
		t.Fatalf("sessions = %+v", d.Sessions)
	}
	if d.Tickets.Mode != ModeSingle || len(d.Tickets.Categories) != 1 || d.Tickets.Categories[0].PriceMinor != 19900 {
		t.Fatalf("tickets = %+v", d.Tickets)
	}
	if len(d.Tickets.Schedule) != 2 || d.Tickets.Schedule[0].PriceMinor != 24950 || d.Tickets.Schedule[1].From != "2026-12-01" {
		t.Fatalf("schedule = %+v", d.Tickets.Schedule)
	}
	if d.Currency != "CZK" || len(d.Channels) != 1 || d.Channels[0].ID != "ch1" || !d.Publish {
		t.Fatalf("extra = %s %+v %v", d.Currency, d.Channels, d.Publish)
	}

	req := BuildBundle(d, 0, 0, "https://poster", "tg:x:1")
	if req.Source != bil24compat.SourceArena || req.ExternalRef != "tg:x:1" || req.ActionEvent.Day != "15.12.2026" || req.ActionEvent.Time != "20:00" || req.ActionEvent.Currency != "CZK" {
		t.Fatalf("req head = %+v", req)
	}
	if req.Venue.ArenaVenueID != "akr" || !req.Publish || len(req.ChannelIDs) != 1 || req.ChannelIDs[0] != "ch1" {
		t.Fatalf("venue/publish = %+v %v %v", req.Venue, req.Publish, req.ChannelIDs)
	}
	if req.Action.PromoterID == nil || *req.Action.PromoterID != "lmp" || req.Action.BigPosterURL != "https://poster" || req.Action.Age != "16+" {
		t.Fatalf("action = %+v", req.Action)
	}
	if len(req.CategoryList) != 1 || req.CategoryList[0].Availability != 300 || req.CategoryList[0].Price != 199 {
		t.Fatalf("categories = %+v", req.CategoryList)
	}
	windows := *req.CategoryList[0].PriceSchedule
	if len(windows) != 2 || windows[0].Price != 249.5 || windows[0].ValidFrom != "2026-11-01T00:00:00+01:00" || windows[0].ValidTo != "2026-12-01T00:00:00+01:00" || windows[1].ValidTo != "" {
		t.Fatalf("windows = %+v", windows)
	}
	// The wire carries money as JSON numbers in major units.
	raw, _ := json.Marshal(req)
	if !strings.Contains(string(raw), `"price":249.5`) || strings.Contains(string(raw), `24950`) {
		t.Fatalf("wire = %s", raw)
	}
}

func TestWizard_ParallelCategories_NewPromoterAndSecondDateSameVenue(t *testing.T) {
	refs := newFakeRefs()
	r := newWizardRun(t, refs)
	r.text("Фестиваль", stEvAge)
	r.press("age:0+", stEvPromoter)
	r.press("prom:new", stPromoterName)
	r.text("Lampyris s.r.o.", stEvPromoter) // duplicate name: back to the chooser
	r.press("prom:new", stPromoterName)
	r.text("Actorre", stPromoterLegal)
	r.text("B12345678", stEvPoster)
	if r.d.Event.PromoterID != "prom-actorre" || len(refs.created) != 1 || refs.created[0] != "promoter:Actorre/B12345678" {
		t.Fatalf("promoter = %+v created=%v", r.d.Event, refs.created)
	}
	r.step(WizInput{Poster: &PosterAccepted{MediaID: "m1", W: 1080, H: 1350}}, stSDate)
	r.firstDate()
	r.press("more", stSDate)
	r.text("16.12.2026", stSTime)
	r.text("19.30", stSSame)
	r.press("same", stSMore)
	r.press("more", stSDate)
	r.text("16.12.2026", stSTime)
	r.text("19:30", stSSame)
	if note := r.press("same", stSSame); note == "" { // the very same date and time again
		t.Fatal("expected a duplicate-session note")
	}
	r.press("back", stSTime)
	r.press("back", stSDate)
	r.press("back", stSMore)
	r.d.Sessions = r.d.Sessions[:2]
	r.d.Cur = 1
	r.press("next", stTMode)
	r.press("mode:multi", stTKind)
	r.press("kind:parallel", stTCatName)
	r.text("Партер", stTCatPrice)
	r.text("1 200", stTCatPlaces)
	r.text("200", stTCatMore)
	r.press("done", stTCatMore) // fewer than two categories
	r.press("more", stTCatName)
	r.text("партер", stTCatName) // duplicate, case-insensitive
	r.text("Балкон", stTCatPrice)
	r.text("800", stTCatPlaces)
	r.text("100", stTCatMore)
	r.press("done", stXDescription)
	r.press("skip", stXCurrency)
	r.text("czk", stXPublish)
	r.press("pub:later", stSummary)

	d := r.d
	if d.Sessions[1].VenueID != "akr" || d.Sessions[1].Time != "19:30" || d.Sessions[1].Capacity != 300 {
		t.Fatalf("second date = %+v", d.Sessions[1])
	}
	req := BuildBundle(d, 1, 777, "", "ref2")
	if req.Action.ActionID != 777 || req.Publish || req.ActionEvent.Day != "16.12.2026" || req.ActionEvent.Time != "19:30" {
		t.Fatalf("req = %+v", req)
	}
	if len(req.CategoryList) != 2 || req.CategoryList[0].Availability != 200 || req.CategoryList[1].Availability != 100 || req.CategoryList[0].Price != 1200 {
		t.Fatalf("categories = %+v", req.CategoryList)
	}
	if req.Action.BigPosterURL != "" {
		t.Fatalf("poster url must be empty when not resolved: %q", req.Action.BigPosterURL)
	}
}

func TestWizard_SequenceCategories_NewCityAndVenue(t *testing.T) {
	refs := newFakeRefs()
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("20.03.2027", stSTime)
	r.text("21:00", stSCountry)
	r.press("country:es", stSCity) // Spain has no cities yet
	r.press("city:new", stCityName)
	r.text("Madrid", stSVenue)
	r.press("venue:new", stVName)
	r.text("Teatro", stVAddress)
	r.text("Calle Mayor 1", stVCapacity)
	r.text("50", stVTz) // the one question about places; Spain has two zones (Canaries): asked
	r.text("Mars/Olympus", stVTz)
	r.text("Europe/Madrid", stSMore) // the places were answered already: straight to "one more date?"
	if want := "venue:Teatro@Europe/Madrid"; len(refs.created) != 2 || refs.created[1] != want {
		t.Fatalf("created = %v", refs.created)
	}
	if s := r.d.Sessions[0]; s.CityID != "city-madrid" || s.VenueID != "venue-teatro" || s.Capacity != 50 || s.Timezone != "Europe/Madrid" {
		t.Fatalf("session = %+v", s)
	}
	r.press("next", stTMode)
	r.press("mode:multi", stTKind)
	r.press("kind:sequence", stTCatName)
	r.text("Early bird", stTCatPrice)
	r.text("20", stTCatUntil)     // the first category cannot be the last: no such question
	r.text("banana", stTCatUntil) // neither a date nor a number: asked again
	r.text("31.12.2026 20", stTCatName)
	r.text("Regular", stTCatPrice)
	r.text("30", stTCatLast)
	r.press("next", stTCatUntil)
	r.text("01.12.2026", stTCatUntil) // earlier than the previous end: refused
	r.text("28.02.2027", stTCatName)
	r.text("Last minute", stTCatPrice)
	r.text("40", stTCatLast)
	r.press("last", stXDescription)
	r.press("skip", stXCurrency)
	r.press("cur:EUR", stXPublish)
	r.press("pub:now", stSummary)

	cats := r.d.Tickets.Categories
	if len(cats) != 3 || cats[0].SellLimit != 20 || cats[0].SellUntil != "2026-12-31" || cats[1].SellUntil != "2027-02-28" || cats[1].SellLimit != 0 {
		t.Fatalf("categories = %+v", cats)
	}
	if cats[2].SellUntil != "" || cats[2].SellLimit != 0 {
		t.Fatalf("the last category must sell until the session: %+v", cats[2])
	}
	req := BuildBundle(r.d, 0, 0, "", "ref")
	cl := req.CategoryList
	if len(cl) != 3 || cl[0].Availability != 50 || cl[1].Availability != 0 || cl[2].Availability != 0 {
		t.Fatalf("availability = %+v", cl)
	}
	if *cl[0].NextCategoryIndex != 1 || *cl[0].SellLimit != 20 || cl[0].SellEndTime != "2027-01-01T00:00:00+01:00" {
		t.Fatalf("first = %+v", cl[0])
	}
	if cl[1].SellStartTime != "" {
		t.Fatalf("a category after a limited step must not get a start time: %+v", cl[1])
	}
	if *cl[1].NextCategoryIndex != 2 || *cl[1].SellLimit != 0 || cl[1].SellEndTime != "2027-03-01T00:00:00+01:00" {
		t.Fatalf("second = %+v", cl[1])
	}
	if cl[2].SellStartTime != cl[1].SellEndTime || *cl[2].NextCategoryIndex != -1 || *cl[2].SellLimit != 0 || cl[2].SellEndTime != "" {
		t.Fatalf("third = %+v", cl[2])
	}
	sum := r.w.Summary("ru", r.d)
	for _, want := range []string{"Концерт", "Early bird", "Regular", "Last minute", "Teatro", "20.03.2027"} {
		if !strings.Contains(sum, want) {
			t.Fatalf("summary lacks %q:\n%s", want, sum)
		}
	}
}

func TestWizard_DefaultsAndTwoChannels(t *testing.T) {
	refs := newFakeRefs()
	refs.channels = append(refs.channels, RefItem{ID: "ch2", Name: "tickets.arenasoldout.com"})
	r := newWizardRun(t, refs)
	r.ws.Defaults = Defaults{Age: "12+", PromoterID: "lmp", PromoterName: "Lampyris s.r.o.", CountryID: "cz", CountryName: "Czechia", CityID: "prg", CityName: "Prague", VenueID: "akr", VenueName: "Palác Akropolis", Currency: "CZK"}
	r.text("Опера", stEvAge)
	r.press("keep", stEvPromoter)
	r.press("keep", stEvPoster)
	r.press("skip", stSDate)
	r.text("1.1.27", stSTime)
	r.press("default", stSCountry)
	r.press("keep", stSCity)
	r.press("keep", stSVenue)
	r.press("keep", stSCapacity)
	r.text("250", stSMore)
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	r.text("Вход", stTPrice)
	r.text("500", stTChanges)
	r.press("no", stXDescription)
	r.press("skip", stXCurrency)
	r.press("cur:CZK", stXChannels) // two channels and no remembered choice: asked
	r.press("done", stXChannels)    // nothing chosen yet
	r.press("ch:ch1", stXChannels)
	r.press("ch:ch2", stXChannels)
	r.press("ch:ch1", stXChannels) // toggled off again
	r.press("done", stXPublish)
	r.press("pub:now", stSummary)
	d := r.d
	if d.Event.Age != "12+" || d.Event.PromoterID != "lmp" || d.Sessions[0].Date != "2027-01-01" || d.Sessions[0].VenueID != "akr" || d.Sessions[0].Capacity != 250 {
		t.Fatalf("draft = %+v %+v", d.Event, d.Sessions)
	}
	if len(d.Channels) != 1 || d.Channels[0].ID != "ch2" {
		t.Fatalf("channels = %+v", d.Channels)
	}
	def := RememberDefaults(r.ws.Defaults, d)
	if def.Capacity != 250 || def.Currency != "CZK" || len(def.Channels) != 1 || def.Channels[0].ID != "ch2" || def.Age != "12+" {
		t.Fatalf("defaults = %+v", def)
	}
	// Next time the remembered channel is applied without asking.
	r2 := newWizardRun(t, refs)
	r2.ws.Defaults = def
	r2.d.Step = stXCurrency
	r2.press("cur:CZK", stXPublish)
	if len(r2.d.Channels) != 1 || r2.d.Channels[0].ID != "ch2" {
		t.Fatalf("remembered channels = %+v", r2.d.Channels)
	}
	// Summary edits jump back and the draft survives a JSON round-trip.
	r.press("edit:tickets", stTMode)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back Draft
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Step != stTMode || back.Event.Name != "Опера" || back.Sessions[0].Timezone != "Europe/Prague" || !back.Publish {
		t.Fatalf("round-trip = %+v", back)
	}
}

func TestWizard_CancelAndBack(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.text("X", stEvAge)
	r.press("back", stEvName)
	r.text("Y", stEvAge)
	// "Cancel" asks first, and keeping the draft is the default way out.
	r.press("cancel", stCancel)
	r.press("cancel:no", stEvAge)
	r.press("cancel", stCancel)
	if _, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Data: "cancel:keep"}); !errors.Is(err, ErrSavedExit) || r.d.Step != stEvAge {
		t.Fatalf("keep: err=%v step=%s", err, r.d.Step)
	}
	r.press("cancel", stCancel)
	if _, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Data: "cancel:drop"}); !errors.Is(err, ErrCancelled) {
		t.Fatalf("drop: %v", err)
	}
}

func TestWizard_Parsers(t *testing.T) {
	dates := map[string]string{"15.12.2026": "2026-12-15", "1/2/27": "2027-02-01", "2026-13-01": "", "31.02.2026": "", "abc": ""}
	for in, want := range dates {
		got, ok := ParseDate(in)
		if (want == "") == ok || got != want {
			t.Errorf("ParseDate(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	times := map[string]string{"20:00": "20:00", "9.5": "09:05", "1930": "19:30", "24:00": "", "x": ""}
	for in, want := range times {
		got, ok := ParseTime(in)
		if (want == "") == ok || got != want {
			t.Errorf("ParseTime(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	prices := map[string]int64{"199": 19900, "99,90": 9990, "1 200": 120000, "1.200,50": 120050, "1,200.5": 120050, "0": 0, "-5": -1, "12.345": 1234500, "abc": -1, "20 €": 2000}
	for in, want := range prices {
		got, ok := ParsePriceMinor(in)
		if want < 0 {
			if ok {
				t.Errorf("ParsePriceMinor(%q) accepted %d", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("ParsePriceMinor(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	counts := map[string]int{"80": 80, "1 200": 1200, "0": 0, "-1": 0, "x": 0}
	for in, want := range counts {
		got, ok := ParseCount(in)
		if (want == 0) == ok || got != want {
			t.Errorf("ParseCount(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	if DisplayDate("2026-12-05") != "05.12.2026" || DisplayDate("junk") != "junk" {
		t.Error("DisplayDate")
	}
}

func TestPosterCheck(t *testing.T) {
	cases := map[[2]int]string{
		{1080, 1350}: "", {1200, 1200}: "", {1080, 1360}: "", {1080, 1080}: "",
		{1080, 1920}: "bot.wz.poster_bad_ratio", {1920, 1080}: "bot.wz.poster_bad_ratio",
		{800, 1000}: "bot.wz.poster_too_small", {0, 0}: "bot.wz.poster_bad_type",
	}
	for wh, want := range cases {
		if got := PosterCheck(wh[0], wh[1]); got != want {
			t.Errorf("PosterCheck(%d,%d) = %q want %q", wh[0], wh[1], got, want)
		}
	}
}

// fakeSave records event-bundle posts and fails the ones it is told to.
type fakeSave struct {
	posts   []bil24compat.ImportSessionRequest
	failRef map[string]error
	next    int64
}

func (f *fakeSave) MediaSignedURL(context.Context, string, string) (string, error) {
	return "https://signed/poster", nil
}

func (f *fakeSave) ImportEventBundle(_ context.Context, _ string, _ uuid.UUID, req bil24compat.ImportSessionRequest) (ImportResult, error) {
	if err := f.failRef[req.ExternalRef]; err != nil {
		return ImportResult{}, err
	}
	if req.ExternalRef == "" && req.ActionEvent.ArenaSessionID == "" {
		return ImportResult{}, errors.New("externalRef must be set unless the session is addressed by id")
	}
	f.posts = append(f.posts, req)
	f.next++
	var res ImportResult
	res.EventID = "event-1"
	res.SessionID = fmt.Sprintf("session-%d", f.next)
	res.CompatIDs.ActionID = 4242
	res.Warnings = append(res.Warnings, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "import.venue_timezone_kept"})
	return res, nil
}

func TestWizard_Save_RetriesOnlyTheMissingDates(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.d.Event.PosterMediaID = "m1"
	r.firstDate()
	r.press("more", stSDate)
	r.text("16.12.2026", stSTime)
	r.press("default", stSSame)
	r.press("same", stSMore)
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	r.press("default", stTPrice)
	r.text("10", stTChanges)
	r.press("no", stXDescription)
	r.press("skip", stXCurrency)
	r.press("cur:CZK", stXPublish)
	r.press("pub:now", stSummary)

	org := r.ws.OrgID.String()
	io := &fakeSave{failRef: map[string]error{"tg:" + org + ":d1:2": &APIError{Status: 422, Code: "import.invalid_venue", Message: "venue gone"}}}
	out := r.w.Save(context.Background(), io, r.ws, r.d, "d1")
	if out.Done || out.FailedIdx != 1 || !strings.Contains(out.Reason, "venue gone") {
		t.Fatalf("first save = %+v", out)
	}
	if len(io.posts) != 1 || io.posts[0].Action.ActionID != 0 || io.posts[0].Action.BigPosterURL != "https://signed/poster" {
		t.Fatalf("posts = %+v", io.posts)
	}
	if r.d.Saved.ActionID != 4242 || r.d.Saved.EventID != "event-1" || r.d.Saved.Sessions[0].SessionID != "session-1" || r.d.Saved.Sessions[1].SessionID != "" {
		t.Fatalf("saved = %+v", r.d.Saved)
	}

	io.failRef = nil
	out = r.w.Save(context.Background(), io, r.ws, r.d, "d1")
	if !out.Done || len(io.posts) != 2 {
		t.Fatalf("retry = %+v posts=%d", out, len(io.posts))
	}
	second := io.posts[1]
	if second.Action.ActionID != 4242 || second.ExternalRef != "tg:"+org+":d1:2" || second.ActionEvent.Day != "16.12.2026" {
		t.Fatalf("second post = %+v", second)
	}
	if len(out.Warnings) != 1 || r.d.Saved.LastReason != "" {
		t.Fatalf("warnings = %v reason=%q", out.Warnings, r.d.Saved.LastReason)
	}
}

// A copied event starts with no channels. With one channel the save takes it,
// with two or more and nothing remembered the person is asked — the event must
// never be published into no channel (found on prod 2026-10-07: a published
// event with no public page and no warning).
func TestWizard_EnsureChannelsBeforeSaving(t *testing.T) {
	ctx := context.Background()
	t.Run("one channel is taken", func(t *testing.T) {
		r := newWizardRun(t, newFakeRefs())
		r.d.Publish = true
		ask, err := r.w.EnsureChannels(ctx, r.ws, r.d)
		if err != nil || ask || len(r.d.Channels) != 1 || r.d.Channels[0].ID != "ch1" {
			t.Fatalf("ask=%v err=%v channels=%+v", ask, err, r.d.Channels)
		}
	})
	t.Run("two channels are asked", func(t *testing.T) {
		refs := newFakeRefs()
		refs.channels = append(refs.channels, RefItem{ID: "ch2", Name: "tickets.arenasoldout.com"})
		r := newWizardRun(t, refs)
		r.d.Publish = true
		r.d.Step = stSummary
		ask, err := r.w.EnsureChannels(ctx, r.ws, r.d)
		if err != nil || !ask || r.d.Step != stXChannels || len(r.d.Channels) != 0 {
			t.Fatalf("ask=%v err=%v step=%s channels=%+v", ask, err, r.d.Step, r.d.Channels)
		}
	})
	t.Run("chosen channels, a draft and an edit are left alone", func(t *testing.T) {
		refs := newFakeRefs()
		refs.channels = append(refs.channels, RefItem{ID: "ch2", Name: "x"})
		r := newWizardRun(t, refs)
		r.d.Publish = true
		r.d.Channels = []RefItem{{ID: "ch2", Name: "x"}}
		if ask, _ := r.w.EnsureChannels(ctx, r.ws, r.d); ask {
			t.Fatal("asked although a channel was chosen")
		}
		r.d.Channels, r.d.Publish = nil, false
		if ask, _ := r.w.EnsureChannels(ctx, r.ws, r.d); ask {
			t.Fatal("asked for an event that is saved as a draft")
		}
		r.d.Publish, r.d.Mode = true, ModeEdit
		if ask, _ := r.w.EnsureChannels(ctx, r.ws, r.d); ask {
			t.Fatal("asked while editing an existing event")
		}
	})
}

// The zone question is answerable without knowing IANA names: the typed city
// becomes a button to confirm, the country's biggest zones are buttons, and a
// many-zone country is never guessed from another venue.
func TestWizard_VenueZoneByCityAndButtons(t *testing.T) {
	newVenue := func(country, city string) (*wizardRun, *fakeRefs) {
		refs := newFakeRefs()
		refs.countries = append(refs.countries, RefItem{ID: "us", Name: "United States", ISO2: "US", Currency: "USD"})
		refs.cities["us"] = nil
		// Another venue in the same country: it must NOT decide the zone.
		refs.venues = append(refs.venues, VenueRef{ID: "ny", Name: "Old", CityID: "x", CountryISO2: "US", Timezone: "America/New_York", Capacity: 10})
		r := newWizardRun(t, refs)
		r.eventHead()
		r.text("20.03.2027", stSTime)
		r.text("21:00", stSCountry)
		r.press("country:"+country, stSCity)
		r.press("city:new", stCityName)
		r.text(city, stSVenue)
		r.press("venue:new", stVName)
		r.text("Hall", stVAddress)
		r.text("Main St 1", stVCapacity)
		r.text("80", stVTz)
		return r, refs
	}
	labels := func(r *wizardRun) []string {
		scr, err := r.w.Render(context.Background(), r.ws, r.d)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, row := range scr.Buttons {
			for _, b := range row {
				if strings.HasPrefix(b.Data, "tz:") {
					out = append(out, b.Data+"|"+b.Label)
				}
			}
		}
		return out
	}

	t.Run("the session's own city is the first button", func(t *testing.T) {
		r, _ := newVenue("us", "Los Angeles")
		got := labels(r)
		if len(got) < 2 || !strings.HasPrefix(got[0], "tz:America/Los_Angeles|✔ ") {
			t.Fatalf("buttons = %v", got)
		}
		if len(got) > maxZoneButtons {
			t.Fatalf("too many buttons: %v", got)
		}
		r.press("tz:America/Los_Angeles", stSMore)
		if r.d.Sessions[0].Timezone != "America/Los_Angeles" {
			t.Fatalf("session = %+v", r.d.Sessions[0])
		}
	})
	t.Run("a typed city is offered, then confirmed", func(t *testing.T) {
		r, _ := newVenue("us", "Springfield-on-nothing")
		if got := labels(r); len(got) == 0 || strings.Contains(got[0], "✔") {
			t.Fatalf("an unknown city must not be marked as a suggestion: %v", got)
		}
		note, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Text: "Chicago"})
		if err != nil || r.d.Step != stVTz || !strings.Contains(note, "America/Chicago") {
			t.Fatalf("step=%s note=%q err=%v", r.d.Step, note, err)
		}
		if got := labels(r); !strings.HasPrefix(got[0], "tz:America/Chicago|✔ ") {
			t.Fatalf("buttons = %v", got)
		}
		r.press("tz:America/Chicago", stSMore)
	})
	t.Run("a bad zone through a button is refused", func(t *testing.T) {
		r, _ := newVenue("us", "Boston")
		_, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Data: "tz:Mars/Olympus"})
		if err != nil || r.d.Step != stVTz {
			t.Fatalf("step=%s err=%v", r.d.Step, err)
		}
	})
}

// The city list holds only cities somebody already used, so a city typed
// straight onto the question must work: a listed name is picked, any other is
// added, and neither needs the "+ Another city" button.
func TestWizard_CityTypedOntoTheQuestion(t *testing.T) {
	start := func(refs *fakeRefs) *wizardRun {
		r := newWizardRun(t, refs)
		r.eventHead()
		r.text("20.03.2027", stSTime)
		r.text("21:00", stSCountry)
		r.press("country:cz", stSCity)
		return r
	}
	t.Run("a listed city is picked, not created", func(t *testing.T) {
		refs := newFakeRefs()
		r := start(refs)
		r.text("  prague ", stSVenue)
		if r.d.Sessions[0].CityID != "prg" || len(refs.created) != 0 {
			t.Fatalf("city=%q created=%v", r.d.Sessions[0].CityID, refs.created)
		}
	})
	t.Run("an unlisted city is added", func(t *testing.T) {
		refs := newFakeRefs()
		r := start(refs)
		r.text("Brno", stSVenue)
		if s := r.d.Sessions[0]; s.CityID != "city-brno" || s.CityName != "Brno" {
			t.Fatalf("session = %+v", s)
		}
		if len(refs.created) != 1 || refs.created[0] != "city:Brno" {
			t.Fatalf("created = %v", refs.created)
		}
	})
}

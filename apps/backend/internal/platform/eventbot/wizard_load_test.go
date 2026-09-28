package eventbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// fakeEdit is an in-memory EditIO: one event with its sessions, tiers and
// price windows, exactly as arena-api would answer.
type fakeEdit struct {
	event    openapi.EventItem
	sessions []openapi.SessionItem
	tiers    map[uuid.UUID][]openapi.TicketTierItem
	windows  map[uuid.UUID][]openapi.TierPriceWindow
}

func (f *fakeEdit) GetEvent(_ context.Context, _ string, id uuid.UUID) (openapi.EventItem, error) {
	if id != f.event.Id {
		return openapi.EventItem{}, &APIError{Status: 404, Code: "event.not_found"}
	}
	return f.event, nil
}
func (f *fakeEdit) ListSessions(context.Context, string, uuid.UUID, uuid.UUID) ([]openapi.SessionItem, error) {
	return f.sessions, nil
}
func (f *fakeEdit) ListTiers(_ context.Context, _ string, _, _, sessionID uuid.UUID) ([]openapi.TicketTierItem, error) {
	return f.tiers[sessionID], nil
}
func (f *fakeEdit) TierPriceSchedule(_ context.Context, _ string, _, _, _, tierID uuid.UUID) ([]openapi.TierPriceWindow, error) {
	return f.windows[tierID], nil
}

var prague = func() *time.Location { l, _ := time.LoadLocation("Europe/Prague"); return l }()

func strp(s string) *string { return &s }
func i32p(n int32) *int32   { return &n }

// newFakeEvent builds a published event of the fakeRefs organization with
// one session at Palác Akropolis on 15.12.2026 20:00 Prague time.
func newFakeEvent(orgID uuid.UUID) *fakeEdit {
	ev := openapi.EventItem{Id: uuid.New(), OrgId: orgID, Name: "Загруженный концерт", AgeRating: strp("16+"),
		Description: strp("Описание из Arena"), Status: "published", UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	promoter := uuid.New()
	ev.PromoterId = &promoter
	ev.PromoterName = strp("Lampyris s.r.o.")
	start := time.Date(2026, 12, 15, 20, 0, 0, 0, prague)
	s := openapi.SessionItem{Id: uuid.New(), EventId: ev.Id, VenueId: uuid.MustParse("00000000-0000-0000-0000-00000000a000"),
		StartAt: start.UTC(), EndAt: start.Add(2 * time.Hour).UTC(), Status: "scheduled", AdmissionMode: "general",
		CapacityTotal: 300, CapacityOverride: i32p(250), Currency: "CZK"}
	return &fakeEdit{event: ev, sessions: []openapi.SessionItem{s}, tiers: map[uuid.UUID][]openapi.TicketTierItem{}, windows: map[uuid.UUID][]openapi.TierPriceWindow{}}
}

func loadRun(t *testing.T) (*wizardRun, *fakeEdit) {
	t.Helper()
	refs := newFakeRefs()
	refs.venues[0].ID = "00000000-0000-0000-0000-00000000a000"
	r := newWizardRun(t, refs)
	return r, newFakeEvent(r.ws.OrgID)
}

func TestLoadEventDraft_SingleCategoryWithSchedule(t *testing.T) {
	r, f := loadRun(t)
	s := f.sessions[0]
	tier := openapi.TicketTierItem{Id: uuid.New(), SessionId: s.Id, Name: "Вход", PriceAmount: 50000, Currency: "CZK", Capacity: i32p(250)}
	f.tiers[s.Id] = []openapi.TicketTierItem{tier}
	f.windows[tier.Id] = []openapi.TierPriceWindow{
		{Id: uuid.New(), TierId: tier.Id, ValidFrom: time.Date(2026, 12, 1, 0, 0, 0, 0, prague).UTC(), PriceAmount: 70000},
		{Id: uuid.New(), TierId: tier.Id, ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, prague).UTC(), PriceAmount: 60000},
	}

	d, notes, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 || d.Mode != ModeEdit || d.Step != stSummary || !d.Publish {
		t.Fatalf("draft head = mode %s step %s publish %v notes %v", d.Mode, d.Step, d.Publish, notes)
	}
	if d.Event.EventID != f.event.Id.String() || d.Event.Name != "Загруженный концерт" || d.Event.Age != "16+" || d.Event.PromoterName != "Lampyris s.r.o." || d.Event.Description != "Описание из Arena" || d.Event.UpdatedAt != "2026-10-01T12:00:00Z" {
		t.Fatalf("event = %+v", d.Event)
	}
	if len(d.Sessions) != 1 {
		t.Fatalf("sessions = %+v", d.Sessions)
	}
	ds := d.Sessions[0]
	if ds.SessionID != s.Id.String() || ds.Date != "2026-12-15" || ds.Time != "20:00" || ds.VenueName != "Palác Akropolis" || ds.Timezone != "Europe/Prague" || ds.Capacity != 250 || ds.CountryISO2 != "CZ" || ds.Currency != "CZK" {
		t.Fatalf("session = %+v", ds)
	}
	if d.Currency != "CZK" || d.Tickets.Mode != ModeSingle || len(d.Tickets.Categories) != 1 || d.Tickets.Categories[0].TierID != tier.Id.String() || d.Tickets.Categories[0].PriceMinor != 50000 {
		t.Fatalf("tickets = %+v", d.Tickets)
	}
	if len(d.Tickets.Schedule) != 2 || d.Tickets.Schedule[0].From != "2026-11-01" || d.Tickets.Schedule[0].PriceMinor != 60000 || d.Tickets.Schedule[1].From != "2026-12-01" {
		t.Fatalf("schedule (must be sorted) = %+v", d.Tickets.Schedule)
	}

	// The summary renders the loaded event and the edit buttons.
	r.d = d
	screen, err := r.w.Render(context.Background(), r.ws, d)
	if err != nil || !strings.Contains(screen.Text, "Загруженный концерт") || screen.Buttons[0][0].Data != "publish" || !strings.Contains(screen.Buttons[0][0].Label, "Сохранить") {
		t.Fatalf("summary = %+v %v", screen, err)
	}

	// Re-pricing the single category keeps its id, whatever it is called.
	r.press("edit:tickets", stTName)
	r.text("Вход в зал", stTPrice)
	r.text("550", stTChanges)
	r.press("no", stSummary) // back to the summary, not on into the description
	c := d.Tickets.Categories[0]
	if c.TierID != tier.Id.String() || c.Name != "Вход в зал" || c.PriceMinor != 55000 || len(d.Tickets.Schedule) != 0 {
		t.Fatalf("re-entered category = %+v schedule=%v", c, d.Tickets.Schedule)
	}

	// Renaming the event returns to the summary as well.
	r.press("edit:event", stEvName)
	r.text("Новое имя", stEvAge)
	r.press("keep", stEvAge) // no remembered default: ignored
	r.press("age:18+", stEvPromoter)
	r.press("prom:org", stEvPoster)
	r.press("skip", stSummary)
	if d.Event.Name != "Новое имя" || d.Event.Age != "18+" || d.Event.PromoterID != "" {
		t.Fatalf("event after edit = %+v", d.Event)
	}

	// The bundle addresses everything by arena id and carries no key.
	req := BuildBundle(d, 0, 0, "", "would-be-ref")
	if req.ExternalRef != "" || req.Action.ArenaEventID != f.event.Id.String() || req.ActionEvent.ArenaSessionID != s.Id.String() || req.CategoryList[0].ArenaTierID != tier.Id.String() {
		t.Fatalf("bundle ids = ref %q event %q session %q tier %q", req.ExternalRef, req.Action.ArenaEventID, req.ActionEvent.ArenaSessionID, req.CategoryList[0].ArenaTierID)
	}
	if req.CategoryList[0].Availability != 250 || req.CategoryList[0].Price != 550 || len(*req.CategoryList[0].PriceSchedule) != 0 {
		t.Fatalf("bundle category = %+v", req.CategoryList[0])
	}

	// Adding a date: existing ones stay, the new one is asked from scratch
	// and the wizard comes back to the summary.
	r.press("edit:when", stSDate)
	r.text("16.12.2026", stSTime)
	r.press("default", stSSame)
	r.press("same", stSMore)
	r.press("next", stSummary) // tickets exist: no ticket questions again
	if len(d.Sessions) != 2 || d.Sessions[0].SessionID == "" || d.Sessions[1].SessionID != "" || d.Sessions[1].VenueID != ds.VenueID {
		t.Fatalf("sessions after add = %+v", d.Sessions)
	}

	// Save re-sends the existing date by id and creates the new one under
	// a key of its own; a retry after a failure re-sends only the new one.
	io := &fakeSave{failRef: map[string]error{"tg:" + r.ws.OrgID.String() + ":d9:2": errors.New("boom")}}
	out := r.w.Save(context.Background(), io, r.ws, d, "d9")
	if out.Done || out.FailedIdx != 1 || len(io.posts) != 1 || io.posts[0].ExternalRef != "" || io.posts[0].ActionEvent.ArenaSessionID != s.Id.String() {
		t.Fatalf("first save = %+v posts=%+v", out, io.posts)
	}
	io.failRef = nil
	out = r.w.Save(context.Background(), io, r.ws, d, "d9")
	if !out.Done || len(io.posts) != 2 || io.posts[1].ExternalRef != "tg:"+r.ws.OrgID.String()+":d9:2" || io.posts[1].ActionEvent.ArenaSessionID != "" || io.posts[1].Action.ArenaEventID != f.event.Id.String() {
		t.Fatalf("retry = %+v posts=%+v", out, io.posts)
	}
	if d.Sessions[1].SessionID != "session-2" {
		t.Fatalf("the added date must now carry its id: %+v", d.Sessions[1])
	}
}

func TestLoadEventDraft_SequenceAndParallelAndCopy(t *testing.T) {
	r, f := loadRun(t)
	s := f.sessions[0]
	early, regular, last := uuid.New(), uuid.New(), uuid.New()
	endEarly := time.Date(2027, 1, 1, 0, 0, 0, 0, prague).UTC()
	f.tiers[s.Id] = []openapi.TicketTierItem{
		// Deliberately out of order: the chain, not the list order, decides.
		{Id: last, Name: "Last minute", PriceAmount: 4000, SaleWindowEnd: &s.StartAt, SortOrder: 2},
		{Id: regular, Name: "Regular", PriceAmount: 3000, NextTierId: &last, SaleWindowEnd: &s.StartAt, SellLimit: i32p(0), SortOrder: 1},
		{Id: early, Name: "Early bird", PriceAmount: 2000, NextTierId: &regular, SaleWindowEnd: &endEarly, SellLimit: i32p(20), SortOrder: 0},
	}
	d, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	cats := d.Tickets.Categories
	if d.Tickets.Mode != ModeSequence || len(cats) != 3 || cats[0].Name != "Early bird" || cats[1].Name != "Regular" || cats[2].Name != "Last minute" {
		t.Fatalf("sequence = %+v", d.Tickets)
	}
	if cats[0].SellUntil != "2026-12-31" || cats[0].SellLimit != 20 || cats[1].SellUntil != "" || cats[2].SellUntil != "" || cats[0].TierID != early.String() {
		t.Fatalf("sequence rules = %+v", cats)
	}

	// Re-entering the categories carries the ids over by name.
	r.d = d
	r.press("edit:tickets", stTCatName)
	r.text("Early bird", stTCatPrice)
	r.text("25", stTCatUntil)
	r.text("31.12.2026", stTCatLimit)
	r.press("skip", stTCatMore)
	r.press("more", stTCatName)
	r.text("Door price", stTCatPrice) // a new name: no id, a new category
	r.text("45", stTCatUntil)
	r.press("skip", stTCatLimit)
	r.text("10", stTCatMore)
	r.press("done", stSummary)
	cats = d.Tickets.Categories
	if len(cats) != 2 || cats[0].TierID != early.String() || cats[0].PriceMinor != 2500 || cats[1].TierID != "" || cats[1].Name != "Door price" {
		t.Fatalf("re-entered sequence = %+v", cats)
	}
	req := BuildBundle(d, 0, 0, "", "")
	if req.CategoryList[0].ArenaTierID != early.String() || req.CategoryList[1].ArenaTierID != "" || *req.CategoryList[0].SellLimit != 0 || *req.CategoryList[0].NextCategoryIndex != 1 {
		t.Fatalf("sequence bundle = %+v", req.CategoryList)
	}

	// Parallel: places come from the category capacity; a second date with
	// other tickets is reported.
	g := newFakeEvent(r.ws.OrgID)
	s1 := g.sessions[0]
	s2 := s1
	s2.Id = uuid.New()
	s2.StartAt = s1.StartAt.Add(24 * time.Hour)
	cancelled := s1
	cancelled.Id = uuid.New()
	cancelled.Status = "cancelled"
	g.sessions = []openapi.SessionItem{s2, cancelled, s1} // unsorted, one cancelled
	p1, p2 := uuid.New(), uuid.New()
	g.tiers[s1.Id] = []openapi.TicketTierItem{{Id: p2, Name: "Балкон", PriceAmount: 800, Capacity: i32p(100), SortOrder: 1}, {Id: p1, Name: "Партер", PriceAmount: 1200, Capacity: i32p(200), SortOrder: 0}}
	g.tiers[s2.Id] = []openapi.TicketTierItem{{Id: uuid.New(), Name: "Партер", PriceAmount: 1500, Capacity: i32p(200)}}
	d2, notes, err := r.w.LoadEventDraft(context.Background(), g, r.ws, g.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Sessions) != 2 || d2.Sessions[0].SessionID != s1.Id.String() || d2.Sessions[1].Date != "2026-12-16" {
		t.Fatalf("sessions = %+v", d2.Sessions)
	}
	if d2.Tickets.Mode != ModeParallel || len(d2.Tickets.Categories) != 2 || d2.Tickets.Categories[0].Name != "Партер" || d2.Tickets.Categories[0].Places != 200 || d2.Tickets.Categories[1].Places != 100 {
		t.Fatalf("parallel = %+v", d2.Tickets)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "16.12.2026") {
		t.Fatalf("notes = %v", notes)
	}

	// The copy: no ids, no dates, the tickets kept, straight to the first
	// date and then to the summary.
	c := CopyDraft(d2)
	if c.Mode != ModeCreate || c.Step != stSDate || c.Event.EventID != "" || len(c.Sessions) != 0 || c.Tickets.Categories[0].TierID != "" || c.Tickets.Categories[0].Places != 200 || !c.Publish {
		t.Fatalf("copy = %+v", c)
	}
	if d2.Tickets.Categories[0].TierID == "" {
		t.Fatal("the copy must not strip the source's ids")
	}
	r.d = c
	r.text("10.01.2027", stSTime)
	r.text("19:00", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	r.press("venue:00000000-0000-0000-0000-00000000a000", stSCapacity)
	r.press("keep", stSMore)
	r.press("next", stSummary)
	req = BuildBundle(c, 0, 0, "", "tg:copy:1")
	if req.ExternalRef != "tg:copy:1" || req.Action.ArenaEventID != "" || req.ActionEvent.ArenaSessionID != "" || req.CategoryList[0].ArenaTierID != "" || req.ActionEvent.Day != "10.01.2027" {
		t.Fatalf("copy bundle = %+v", req)
	}
}

func TestLoadEventDraft_RefusesSeatedAndForeign(t *testing.T) {
	r, f := loadRun(t)
	plan := uuid.New()
	f.sessions[0].SeatingPlanVersionId = &plan
	if _, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("seated: %v", err)
	}
	f.sessions[0].SeatingPlanVersionId = nil
	f.event.OrgId = uuid.New()
	if _, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("foreign: %v", err)
	}
	if _, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, uuid.New()); !IsAPIError(err, 404) {
		t.Fatalf("unknown: %v", err)
	}
}

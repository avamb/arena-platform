package eventbot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func (f *fakeRefs) VenuePlans(_ context.Context, _ string, _ uuid.UUID, venueID string) ([]PlanRef, error) {
	return f.plans[venueID], nil
}

func akropolisPlan() PlanRef {
	return PlanRef{
		VersionID: "11111111-1111-1111-1111-111111111111", Name: "Main hall", Seats: 200,
		Categories: []PlanCategory{
			{Index: 1, Name: "Stalls", Places: 120},
			{Index: 2, Name: "Balcony", Places: 60},
			{Index: 3, Name: "Standing", Places: 20, GA: true},
		},
	}
}

func seatedRun(t *testing.T) *wizardRun {
	t.Helper()
	refs := newFakeRefs()
	refs.plans = map[string][]PlanRef{"akr": {akropolisPlan()}}
	return newWizardRun(t, refs)
}

// seatedToVenue walks a new event up to the venue question's answer.
func seatedToVenue(r *wizardRun) {
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
}

// A venue with a plan asks whether to sell by the plan, then the price of each
// plan category; the date carries the categories of the plan and its places,
// and a date sold by the plan needs no shared ticket questions.
func TestWizardSeated_CreateByPlan(t *testing.T) {
	r := seatedRun(t)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Palác Akropolis") || len(screen.Buttons) < 2 {
		t.Fatalf("plan question = %q %v", screen.Text, screen.Buttons)
	}
	r.press("plan:0", stSPlanPrice)
	r.text("не цена", stSPlanPrice) // refused, still on the first category
	if r.d.Scratch.SeatIdx != 0 {
		t.Fatalf("a refused price must not move on: %d", r.d.Scratch.SeatIdx)
	}
	r.text("90", stSPlanPrice)
	r.text("50", stSPlanPrice)
	r.text("15", stSPlanExtra)
	r.press("extra:done", stSMore)
	s := r.d.Sessions[0]
	if s.PlanVersionID != akropolisPlan().VersionID || s.Capacity != 200 || s.Tickets == nil || len(s.Tickets.Categories) != 3 {
		t.Fatalf("session = %+v", s)
	}
	if got := s.Tickets.Categories; got[0].PriceMinor != 9000 || got[1].PriceMinor != 5000 || got[2].PriceMinor != 1500 {
		t.Fatalf("prices = %+v", got)
	}
	r.press("next", stXDescription) // every date is sold by the plan: no shared ticket questions
	if r.d.Tickets.Mode != ModeParallel || len(r.d.Tickets.Categories) != 3 {
		t.Fatalf("summary mirror = %+v", r.d.Tickets)
	}

	req := BuildBundle(r.d, 0, 0, "", "tg:ref:1")
	if req.ActionEvent.ArenaSeatingPlanVersionID != akropolisPlan().VersionID || len(req.CategoryList) != 3 {
		t.Fatalf("bundle = %+v", req)
	}
	c := req.CategoryList
	if !c[0].Placement || c[0].Availability != 0 || c[0].PlanCategoryIndex == nil || *c[0].PlanCategoryIndex != 1 || c[0].Price != 90 || c[0].PriceSchedule != nil || c[0].NextCategoryIndex != nil {
		t.Fatalf("seated category = %+v", c[0])
	}
	if c[2].Placement || c[2].Availability != 20 || *c[2].PlanCategoryIndex != 3 {
		t.Fatalf("standing zone = %+v", c[2])
	}
}

// "Without a plan" keeps the old general-admission path.
func TestWizardSeated_NoPlanKeepsTheGeneralAdmissionPath(t *testing.T) {
	r := seatedRun(t)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	r.press("plan:none", stSCapacity)
	r.press("keep", stSMore)
	r.press("next", stTMode)
	if r.d.Sessions[0].seated() || r.d.Sessions[0].Tickets != nil {
		t.Fatalf("session = %+v", r.d.Sessions[0])
	}
}

// A venue with no plan goes straight to the places question, as before.
func TestWizardSeated_VenueWithoutPlans(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.firstDate()
	if r.d.Sessions[0].seated() {
		t.Fatal("no plan, no seated date")
	}
}

// An edited event keeps its seated date: it loads, "Prices and categories"
// asks only the prices, and the bundle names the same plan version and the
// categories by id, without a schedule or a seat quantity.
func TestWizardSeated_EditPricesOnly(t *testing.T) {
	r, f := loadRun(t)
	s := f.sessions[0]
	plan := uuid.MustParse(akropolisPlan().VersionID)
	f.sessions[0].SeatingPlanVersionId = &plan
	seated, ga := "seated", "ga"
	stalls := openapi.TicketTierItem{Id: uuid.New(), SessionId: s.Id, Name: "Stalls", PriceAmount: 9000, Currency: "CZK", Kind: &seated, SortOrder: 0}
	standing := openapi.TicketTierItem{Id: uuid.New(), SessionId: s.Id, Name: "Standing", PriceAmount: 1500, Currency: "CZK", Kind: &ga, Capacity: i32p(20), SortOrder: 1}
	f.tiers[s.Id] = []openapi.TicketTierItem{standing, stalls}
	d, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	own := d.Sessions[0].Tickets
	if !d.Sessions[0].seated() || own == nil || len(own.Categories) != 2 || own.Categories[0].Name != "Stalls" || own.Categories[0].GA || !own.Categories[1].GA {
		t.Fatalf("loaded = %+v / %+v", d.Sessions[0], own)
	}
	capacity := d.Sessions[0].Capacity

	r.press("e:tickets", stSPlanPrice)
	r.press("keep", stSPlanPrice)
	r.text("20", stEditMenu)
	if got := d.Sessions[0].Tickets.Categories; got[0].PriceMinor != 9000 || got[1].PriceMinor != 2000 || !d.Sessions[0].Dirty {
		t.Fatalf("prices = %+v dirty=%v", got, d.Sessions[0].Dirty)
	}
	if d.Sessions[0].Capacity != capacity {
		t.Fatalf("capacity moved from %d to %d", capacity, d.Sessions[0].Capacity)
	}

	req := BuildBundle(d, 0, 0, "", "")
	if req.ActionEvent.ArenaSessionID != s.Id.String() || req.ActionEvent.ArenaSeatingPlanVersionID != plan.String() || len(req.CategoryList) != 2 {
		t.Fatalf("bundle = %+v", req)
	}
	if c := req.CategoryList[0]; c.ArenaTierID != stalls.Id.String() || !c.Placement || c.Availability != 0 || c.PlanCategoryIndex != nil || c.PriceSchedule != nil || c.Price != 90 {
		t.Fatalf("seated tier = %+v", c)
	}
	if c := req.CategoryList[1]; c.ArenaTierID != standing.Id.String() || c.Placement || c.Price != 20 || c.Availability != 20 {
		t.Fatalf("standing tier = %+v", c)
	}
}

// Copying a seated event starts it afresh: the categories of a plan are not a
// set of categories to sell on the new dates.
func TestWizardSeated_CopyAsksForTickets(t *testing.T) {
	r := seatedRun(t)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	r.press("plan:0", stSPlanPrice)
	r.text("90", stSPlanPrice)
	r.text("50", stSPlanPrice)
	r.text("15", stSPlanExtra)
	r.press("extra:done", stSMore)
	r.press("next", stXDescription)
	c := CopyDraft(r.d)
	if c.Tickets.Mode != "" || c.Scratch.ReturnToSummary {
		t.Fatalf("copy = %+v scratch=%+v", c.Tickets, c.Scratch)
	}
}

// Choosing another venue after a plan was chosen drops the plan of the first.
func TestWizardSeated_AnotherVenueDropsThePlan(t *testing.T) {
	r := seatedRun(t)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	r.press("plan:0", stSPlanPrice)
	r.w.refs.(*fakeRefs).plans = nil // the venue chosen next has no plan
	r.d.Step = stSVenue
	r.press("venue:akr", stSCapacity)
	if s := r.d.Sessions[0]; s.seated() || s.Tickets != nil {
		t.Fatalf("the plan of the first choice stayed: %+v", s)
	}
}

// A date added after a seated one cannot copy its categories: they are the
// plan's. "Own categories" on a seated date asks only the prices.
func TestWizardSeated_NoCopyOfAPlanAndOwnMeansPrices(t *testing.T) {
	r, f := loadRun(t)
	s := f.sessions[0]
	plan := uuid.MustParse(akropolisPlan().VersionID)
	f.sessions[0].SeatingPlanVersionId = &plan
	seated := "seated"
	f.tiers[s.Id] = []openapi.TicketTierItem{{Id: uuid.New(), SessionId: s.Id, Name: "Stalls", PriceAmount: 9000, Currency: "CZK", Kind: &seated}}
	d, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	d.Sessions = append(d.Sessions, DraftSession{Date: "2026-12-16", Time: "20:00", VenueID: d.Sessions[0].VenueID, Timezone: d.Sessions[0].Timezone})
	d.Cur, d.Step = 1, stSTickets
	screen, err := r.w.Render(context.Background(), r.ws, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range screen.Buttons {
		for _, b := range row {
			if b.Data == "st:same" {
				t.Fatalf("a seated date must not be offered as a copy: %v", screen.Buttons)
			}
		}
	}
	if _, err := r.w.Apply(context.Background(), r.ws, d, WizInput{Data: "st:same"}); err != nil || d.Step != stSTickets {
		t.Fatalf("a forced st:same must change nothing: step %s err %v", d.Step, err)
	}

	// The seated date's own summary: "own categories" re-prices, not GA questions.
	d.Cur, d.Step = 0, stSConfirm
	d.Scratch.Edit = "dates"
	if _, err := r.w.Apply(context.Background(), r.ws, d, WizInput{Data: "sc:own"}); err != nil || d.Step != stSPlanPrice {
		t.Fatalf("sc:own on a seated date: step %s err %v", d.Step, err)
	}
}

func hasButton(s Screen, data string) bool {
	for _, row := range s.Buttons {
		for _, b := range row {
			if b.Data == data {
				return true
			}
		}
	}
	return false
}

// A plan drawn with one category ("Church": every seat one colour): after its
// price the wizard offers extra price categories — name, price, colour — and
// the bundle sends them as planExtra, without places, after the plan's own.
func TestWizardSeated_ExtraCategoriesOverAOneCategoryPlan(t *testing.T) {
	refs := newFakeRefs()
	refs.plans = map[string][]PlanRef{"akr": {{
		VersionID: "22222222-2222-2222-2222-222222222222", Name: "Church", Seats: 100,
		Categories: []PlanCategory{{Index: 1, Name: "Hall", Places: 100}},
	}}}
	r := newWizardRun(t, refs)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	r.press("plan:0", stSPlanPrice)
	r.text("30", stSPlanExtra)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "одна категория") || !strings.Contains(screen.Text, "Hall") ||
		!hasButton(screen, "extra:add") || !hasButton(screen, "extra:done") {
		t.Fatalf("extra question = %q %v", screen.Text, screen.Buttons)
	}

	r.press("extra:add", stSPlanExtraName)
	if note := r.text("  ", stSPlanExtraName); !strings.Contains(note, "VIP") {
		t.Fatalf("an empty name must be refused: %q", note)
	}
	if note := r.text("hall", stSPlanExtraName); !strings.Contains(note, "уже есть") {
		t.Fatalf("a name of the plan's category must be refused: %q", note)
	}
	r.text("VIP", stSPlanExtraPrice)
	r.text("не цена", stSPlanExtraPrice)
	r.text("90", stSPlanExtraColor)
	screen, err = r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !hasButton(screen, "color:#dc2626") || !hasButton(screen, "color:#374151") {
		t.Fatalf("colour buttons = %v", screen.Buttons)
	}
	r.press("color:#123456", stSPlanExtraColor) // not one of the offered colours
	r.press("color:#dc2626", stSPlanExtra)
	r.press("extra:add", stSPlanExtraName)
	r.text("Student", stSPlanExtraPrice)
	r.text("10", stSPlanExtraColor)
	r.press("color:#16a34a", stSPlanExtra)
	screen, err = r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen.Text, "одна категория") || !strings.Contains(screen.Text, "🟥 VIP") || !strings.Contains(screen.Text, "🟩 Student") {
		t.Fatalf("list after two extras = %q", screen.Text)
	}
	// "Back" from the list returns to the plan's prices, not into the
	// colour of a category that is already added.
	if !r.d.back() || r.d.Step != stSPlanPrice {
		t.Fatalf("back from the extra list went to %s", r.d.Step)
	}
	r.d.Step = stSPlanExtra

	note := r.press("extra:done", stSMore)
	if !strings.Contains(note, "новых 2") {
		t.Fatalf("done note = %q", note)
	}
	s := r.d.Sessions[0]
	if s.Capacity != 100 || s.Tickets == nil || len(s.Tickets.Categories) != 3 {
		t.Fatalf("session = %+v", s)
	}
	req := BuildBundle(r.d, 0, 0, "", "tg:ref:1")
	c := req.CategoryList
	if len(c) != 3 || c[0].PlanExtra || c[0].PlanCategoryIndex == nil || c[0].Price != 30 {
		t.Fatalf("plan category = %+v", c)
	}
	for i, want := range []struct {
		name  string
		price float64
		color string
	}{{"VIP", 90, "#dc2626"}, {"Student", 10, "#16a34a"}} {
		e := c[i+1]
		if !e.PlanExtra || e.PlanCategoryIndex != nil || e.Availability != 0 || e.CategoryPriceName != want.name ||
			e.Price != want.price || e.Color != want.color {
			t.Fatalf("extra %d = %+v", i, e)
		}
	}
}

// The extra categories stop at the seat layouts' fifteen.
func TestWizardSeated_ExtraCategoriesStopAtFifteen(t *testing.T) {
	r := seatedRun(t)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	r.press("plan:0", stSPlanPrice)
	r.text("90", stSPlanPrice)
	r.text("50", stSPlanPrice)
	r.text("15", stSPlanExtra)
	for i := 0; i < maxSeatCategories-3; i++ {
		r.press("extra:add", stSPlanExtraName)
		r.text("C"+itoa(i), stSPlanExtraPrice)
		r.text("5", stSPlanExtraColor)
		r.press("color:#2563eb", stSPlanExtra)
	}
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if hasButton(screen, "extra:add") {
		t.Fatal("a sixteenth category is offered")
	}
	r.press("extra:add", stSPlanExtra)
}

// An existing venue without a seating plan does not silently sell without
// seats: the places question says so and offers another venue.
func TestWizardSeated_VenueWithoutPlanSaysSo(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	r.press("venue:akr", stSCapacity)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "нет загруженной схемы") || !hasButton(screen, "venue:other") {
		t.Fatalf("places question = %q %v", screen.Text, screen.Buttons)
	}
	r.press("venue:other", stSVenue)
	// A venue with a plan, chosen next, asks about the plan as usual.
	r.w.refs.(*fakeRefs).plans = map[string][]PlanRef{"akr": {akropolisPlan()}}
	r.press("venue:akr", stSPlan)
	if r.d.Scratch.NoPlanVenueID != "" {
		t.Fatalf("the no-plan mark stayed: %q", r.d.Scratch.NoPlanVenueID)
	}
}

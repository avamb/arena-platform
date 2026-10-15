package eventbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// twoDates is the live case of 2026-10-15: an event made one date per import,
// whose two dates sell DIFFERENT categories ("4–6 years" on one day, "from 6
// years" on the next), both "at once" (parallel).
type twoDates struct {
	r            *wizardRun
	d            *Draft
	f            *fakeEdit
	s1, s2       openapi.SessionItem
	kid1, extra1 uuid.UUID // date 1's categories
	kid2, extra2 uuid.UUID // date 2's categories
}

func loadTwoDates(t *testing.T) *twoDates {
	t.Helper()
	r, f := loadRun(t)
	s1 := f.sessions[0]
	s2 := s1
	s2.Id = uuid.New()
	s2.StartAt = s1.StartAt.Add(24 * time.Hour)
	s2.EndAt = s1.EndAt.Add(24 * time.Hour)
	f.sessions = []openapi.SessionItem{s1, s2}
	x := &twoDates{r: r, f: f, s1: s1, s2: s2, kid1: uuid.New(), extra1: uuid.New(), kid2: uuid.New(), extra2: uuid.New()}
	f.tiers[s1.Id] = []openapi.TicketTierItem{
		{Id: x.kid1, Name: "Ребёнок 4–6 лет + взрослый", PriceAmount: 5000, Capacity: i32p(11), SortOrder: 0},
		{Id: x.extra1, Name: "Дополнительный детский", PriceAmount: 3500, Capacity: i32p(3), SortOrder: 1},
	}
	f.tiers[s2.Id] = []openapi.TicketTierItem{
		{Id: x.kid2, Name: "Ребёнок от 6 лет + взрослый", PriceAmount: 5000, Capacity: i32p(11), SortOrder: 0},
		{Id: x.extra2, Name: "Дополнительный детский", PriceAmount: 3500, Capacity: i32p(3), SortOrder: 1},
	}
	d, notes, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "разные категории") {
		t.Fatalf("notes = %v", notes)
	}
	r.d = d
	x.d = d
	return x
}

func tierIDs(req []string) string { return strings.Join(req, ",") }

func bundleTierIDs(d *Draft, idx int) []string {
	req := BuildBundle(d, idx, 0, "", "ref")
	out := []string{}
	for _, c := range req.CategoryList {
		out = append(out, c.ArenaTierID)
	}
	return out
}

// Each date of an edited event is read with its own categories and sent with
// its own tier ids — never the first date's (which the import refuses for
// the second date with 422 import.invalid_category).
func TestDateTickets_LoadKeepsEachDatesOwnCategories(t *testing.T) {
	x := loadTwoDates(t)
	d := x.d
	if len(d.Sessions) != 2 || d.Sessions[0].Tickets == nil || d.Sessions[1].Tickets == nil {
		t.Fatalf("sessions = %+v", d.Sessions)
	}
	if got := d.Sessions[1].Tickets.Categories[0].Name; got != "Ребёнок от 6 лет + взрослый" {
		t.Fatalf("second date's first category = %q", got)
	}
	if got, want := tierIDs(bundleTierIDs(d, 0)), x.kid1.String()+","+x.extra1.String(); got != want {
		t.Fatalf("date 1 bundle ids = %s want %s", got, want)
	}
	if got, want := tierIDs(bundleTierIDs(d, 1)), x.kid2.String()+","+x.extra2.String(); got != want {
		t.Fatalf("date 2 bundle ids = %s want %s", got, want)
	}
	req := BuildBundle(d, 1, 0, "", "ref")
	if req.CategoryList[0].CategoryPriceName != "Ребёнок от 6 лет + взрослый" || req.CategoryList[0].Availability != 11 || req.ActionEvent.ArenaSessionID != x.s2.Id.String() {
		t.Fatalf("date 2 bundle = %+v", req)
	}

	// The card shows the categories date by date.
	screen, err := x.r.w.Render(context.Background(), x.r.ws, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"У сеансов разные категории", "15.12.2026 20:00: Одновременно: Ребёнок 4–6 лет", "16.12.2026 20:00: Одновременно: Ребёнок от 6 лет"} {
		if !strings.Contains(screen.Text, want) {
			t.Fatalf("card lacks %q:\n%s", want, screen.Text)
		}
	}
}

// The live case: "Another date" gets its own two categories (9 + 3 places);
// the save posts only that date, the other two are not touched.
func TestDateTickets_AnotherDateWithItsOwnCategories(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:date", stSDate)
	r.text("18.12.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	screen, _ := r.w.Render(context.Background(), r.ws, d)
	if !strings.Contains(screen.Text, "Категории сеанса 3") || !strings.Contains(screen.Text, "16.12.2026 20:00") {
		t.Fatalf("categories question = %q", screen.Text)
	}
	r.press("st:own", stTCatName)
	screen, _ = r.w.Render(context.Background(), r.ws, d)
	if !strings.Contains(screen.Text, "Для сеанса 18.12.2026 18:00") {
		t.Fatalf("the ticket question does not say which date: %q", screen.Text)
	}
	r.text("Ребёнок 4–9 лет + взрослый", stTCatPrice)
	r.text("50", stTCatPlaces)
	r.text("9", stTCatMore)
	r.press("more", stTCatName)
	r.text("Дополнительный детский", stTCatPrice)
	r.text("35", stTCatPlaces)
	r.text("3", stTCatMore)
	note := r.press("done", stSMore)
	if !strings.Contains(note, "Ребёнок 4–9 лет") {
		t.Fatalf("ack = %q", note)
	}
	r.press("next", stEditMenu)

	s3 := d.Sessions[2]
	if s3.SessionID != "" || s3.Tickets == nil || len(s3.Tickets.Categories) != 2 || s3.Capacity != 12 {
		t.Fatalf("new date = %+v tickets %+v", s3, s3.Tickets)
	}
	for _, c := range s3.Tickets.Categories {
		if c.TierID != "" {
			t.Fatalf("a new date carries a tier id: %+v", c)
		}
	}
	if d.Sessions[0].Dirty || d.Sessions[1].Dirty {
		t.Fatal("existing dates marked changed")
	}
	if got := d.Sessions[0].Tickets.Categories[0].TierID; got != x.kid1.String() {
		t.Fatalf("date 1 lost its id: %s", got)
	}

	io := &fakeSave{}
	out := r.w.Save(context.Background(), io, r.ws, d, "d1")
	if !out.Done || len(io.posts) != 1 {
		t.Fatalf("save = %+v posts = %d, want only the new date", out, len(io.posts))
	}
	p := io.posts[0]
	if p.ActionEvent.ArenaSessionID != "" || p.ExternalRef == "" || p.Action.ArenaEventID != x.f.event.Id.String() || p.ActionEvent.Day != "18.12.2026" || p.ActionEvent.Time != "18:00" {
		t.Fatalf("new date bundle head = %+v", p)
	}
	if len(p.CategoryList) != 2 || p.CategoryList[0].CategoryPriceName != "Ребёнок 4–9 лет + взрослый" || p.CategoryList[0].Availability != 9 || p.CategoryList[1].Availability != 3 || p.CategoryList[0].ArenaTierID != "" || p.CategoryList[1].ArenaTierID != "" {
		t.Fatalf("new date categories = %+v", p.CategoryList)
	}
}

// "Same as the date before" copies that date's categories as new ones.
func TestDateTickets_AnotherDateSameAsThePrevious(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:date", stSDate)
	r.text("18.12.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	r.press("st:same", stSMore)
	s3 := d.Sessions[2]
	if s3.Tickets == nil || len(s3.Tickets.Categories) != 2 || s3.Tickets.Categories[0].Name != "Ребёнок от 6 лет + взрослый" || s3.Tickets.Categories[0].TierID != "" || s3.Capacity != 14 {
		t.Fatalf("copied date = %+v %+v", s3, s3.Tickets)
	}
	// The copy is a copy: changing the new date never touches date 2.
	s3.Tickets.Categories[0].Name = "x"
	if d.Sessions[1].Tickets.Categories[0].Name == "x" {
		t.Fatal("dates share a slice")
	}
}

// "Prices and categories" asks for the date first; changing one date never
// touches another, and only the changed date is posted.
func TestDateTickets_OneDatesCategoriesLeaveTheOtherAlone(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:tickets", stTDate)
	screen, _ := r.w.Render(context.Background(), r.ws, d)
	flat := strings.Join([]string{screen.Text, buttonsData(screen)}, "|")
	if !strings.Contains(flat, "td:1") || !strings.Contains(flat, "td:2") || !strings.Contains(flat, "td:all") {
		t.Fatalf("date choice = %s", flat)
	}
	r.press("td:1", stTCatName)
	r.text("Ребёнок 4–7 лет + взрослый", stTCatPrice) // renamed: a new category of date 1
	r.text("50", stTCatPlaces)
	r.text("11", stTCatMore)
	r.press("more", stTCatName)
	r.text("Дополнительный детский", stTCatPrice) // same name: keeps date 1's own id
	r.text("40", stTCatPlaces)
	r.text("3", stTCatMore)
	r.press("done", stEditMenu)

	c1 := d.Sessions[0].Tickets.Categories
	if c1[0].TierID != "" || c1[1].TierID != x.extra1.String() || c1[1].PriceMinor != 4000 || !d.Sessions[0].Dirty {
		t.Fatalf("date 1 after the change = %+v dirty=%v", c1, d.Sessions[0].Dirty)
	}
	c2 := d.Sessions[1].Tickets.Categories
	if d.Sessions[1].Dirty || c2[0].TierID != x.kid2.String() || c2[1].TierID != x.extra2.String() || c2[1].PriceMinor != 3500 {
		t.Fatalf("date 2 changed: %+v", c2)
	}
	if !contains(d.Changed, "tickets") {
		t.Fatalf("Changed = %v", d.Changed)
	}
	io := &fakeSave{}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d2"); !out.Done || len(io.posts) != 1 || io.posts[0].ActionEvent.ArenaSessionID != x.s1.Id.String() {
		t.Fatalf("save = %+v posts = %+v", out, io.posts)
	}
}

// Leaving the categories half way changes nothing on the date.
func TestDateTickets_BackToTheCardKeepsTheDate(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	before := append([]DraftCategory(nil), d.Sessions[1].Tickets.Categories...)
	r.press("e:tickets", stTDate)
	r.press("td:2", stTCatName)
	r.text("Другая", stTCatPrice)
	r.press("e:home", stEditMenu)
	if d.Sessions[1].Dirty || len(d.Sessions[1].Tickets.Categories) != 2 || d.Sessions[1].Tickets.Categories[0] != before[0] || contains(d.Changed, "tickets") {
		t.Fatalf("date 2 after leaving = %+v changed=%v", d.Sessions[1].Tickets, d.Changed)
	}
	// A half-entered new date (its categories not chosen yet) is dropped.
	r.press("e:date", stSDate)
	r.text("18.12.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	r.press("e:home", stEditMenu)
	if len(d.Sessions) != 2 || contains(d.Changed, "dates") {
		t.Fatalf("half-entered date kept: %d %v", len(d.Sessions), d.Changed)
	}
}

// "For all dates" gives every date the same categories; each date keeps its
// own id for the category of the same name.
func TestDateTickets_ForAllDatesCarriesEachDatesOwnIDs(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:tickets", stTDate)
	r.press("td:all", stTCatName)
	r.text("Взрослый", stTCatPrice)
	r.text("60", stTCatPlaces)
	r.text("10", stTCatMore)
	r.press("more", stTCatName)
	r.text("Дополнительный детский", stTCatPrice)
	r.text("35", stTCatPlaces)
	r.text("4", stTCatMore)
	r.press("done", stEditMenu)
	c1, c2 := d.Sessions[0].Tickets.Categories, d.Sessions[1].Tickets.Categories
	if c1[1].TierID != x.extra1.String() || c2[1].TierID != x.extra2.String() || c1[0].TierID != "" || c2[0].TierID != "" {
		t.Fatalf("ids: date 1 %+v date 2 %+v", c1, c2)
	}
	if d.Sessions[0].Capacity != 14 || d.Sessions[1].Capacity != 14 || !d.Sessions[0].Dirty || !d.Sessions[1].Dirty {
		t.Fatalf("dates = %+v", d.Sessions)
	}
	io := &fakeSave{}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d3"); !out.Done || len(io.posts) != 2 {
		t.Fatalf("save = %+v posts = %d", out, len(io.posts))
	}
}

// A change of the event itself rides on the first date only; a changed
// currency (stored per date) is posted with every date.
func TestDateTickets_SendPlan(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:name", stEvName)
	r.text("Новое имя", stEditMenu)
	if got := d.sendPlan(); !got[0] || got[1] {
		t.Fatalf("name change plan = %v", got)
	}
	r.press("e:currency", stXCurrency)
	r.press("cur:CZK", stEditMenu)
	if got := d.sendPlan(); !got[0] || !got[1] {
		t.Fatalf("currency change plan = %v", got)
	}
	// A new event posts every date.
	c := CopyDraft(d)
	c.Sessions = []DraftSession{{Date: "2026-12-20"}, {Date: "2026-12-21"}}
	if got := c.sendPlan(); !got[0] || !got[1] {
		t.Fatalf("new event plan = %v", got)
	}
}

func buttonsData(s Screen) string {
	out := []string{}
	for _, row := range s.Buttons {
		for _, b := range row {
			out = append(out, b.Data)
		}
	}
	return strings.Join(out, " ")
}

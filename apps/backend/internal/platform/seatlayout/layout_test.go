package seatlayout

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

func okPayload() LayoutPayload {
	return LayoutPayload{
		Slots: []LayoutSlot{
			{Key: "vip", Name: "VIP", Color: "#AA0000", PriceAmount: 5000, SortOrder: 0},
			{Key: "std", Name: "Standard", PriceAmount: 2500, SortOrder: 1},
		},
		Assignments:    map[string]string{"S|A|1": "vip", "S|A|2": "std"},
		ClosedSeatKeys: []string{"S|A|9", "S|A|2", "S|A|9"},
	}
}

func TestLayoutNormalize_StoresTheCanonicalForm(t *testing.T) {
	p := okPayload()
	p.FrozenSeatKeys = []string{"S|A|3"}
	got, err := p.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got.Slots[0].Color != "#aa0000" {
		t.Errorf("colour = %q, want lower-case", got.Slots[0].Color)
	}
	if strings.Join(got.ClosedSeatKeys, ",") != "S|A|2,S|A|9" {
		t.Errorf("closed = %v, want sorted and unique", got.ClosedSeatKeys)
	}
	if got.FrozenSeatKeys != nil {
		t.Errorf("frozen list must not be stored: %v", got.FrozenSeatKeys)
	}
}

func TestLayoutNormalize_Refusals(t *testing.T) {
	over := make([]LayoutSlot, MaxSlots+1)
	for i := range over {
		over[i] = LayoutSlot{Key: string(rune('a' + i)), Name: string(rune('A' + i))}
	}
	tooMany := LayoutPayload{Slots: okPayload().Slots, Assignments: map[string]string{}}
	for i := 0; i <= MaxChanges; i++ {
		tooMany.Assignments["k"+uuid.NewString()] = "vip"
	}
	cases := []struct {
		name   string
		mutate func(p *LayoutPayload)
		raw    *LayoutPayload
		code   string
	}{
		{name: "no slots", mutate: func(p *LayoutPayload) { p.Slots = nil }},
		{name: "16 slots", raw: &LayoutPayload{Slots: over, Assignments: map[string]string{"a": "a"}}},
		{name: "bad slot key", mutate: func(p *LayoutPayload) { p.Slots[0].Key = "no good" }},
		{name: "duplicate slot key", mutate: func(p *LayoutPayload) { p.Slots[1].Key = "vip" }},
		{name: "empty name", mutate: func(p *LayoutPayload) { p.Slots[0].Name = "  " }},
		{name: "names clash ignoring case", mutate: func(p *LayoutPayload) { p.Slots[1].Name = " vip " }},
		{name: "bad colour", mutate: func(p *LayoutPayload) { p.Slots[0].Color = "red" }},
		{name: "negative price", mutate: func(p *LayoutPayload) { p.Slots[0].PriceAmount = -1 }},
		{name: "huge price", mutate: func(p *LayoutPayload) { p.Slots[0].PriceAmount = 2_000_000_000 }},
		{name: "bad currency", mutate: func(p *LayoutPayload) { p.Slots[0].Currency = "eur" }},
		{name: "share over 1", mutate: func(p *LayoutPayload) { s := 1.5; p.Slots[0].Share = &s }},
		{name: "assignment to unknown slot", mutate: func(p *LayoutPayload) { p.Assignments["S|A|3"] = "ghost" }},
		{name: "empty seat key", mutate: func(p *LayoutPayload) { p.Assignments[""] = "vip" }},
		{name: "empty closed key", mutate: func(p *LayoutPayload) { p.ClosedSeatKeys = append(p.ClosedSeatKeys, "") }},
		{name: "no seats", mutate: func(p *LayoutPayload) { p.Assignments, p.ClosedSeatKeys = nil, nil }},
		{name: "more seats than a batch", raw: &tooMany, code: "seat_layout.too_many_seats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := okPayload()
			if tc.raw != nil {
				p = *tc.raw
			} else {
				tc.mutate(&p)
			}
			_, err := p.Normalize()
			var le *LayoutError
			if !errors.As(err, &le) {
				t.Fatalf("err = %v, want a *LayoutError", err)
			}
			want := tc.code
			if want == "" {
				want = "seat_layout.invalid_payload"
			}
			if le.Code != want {
				t.Errorf("code = %q, want %q", le.Code, want)
			}
		})
	}
}

func TestNormalizeLayoutName(t *testing.T) {
	if n, err := NormalizeLayoutName("  Wide centre "); err != nil || n != "Wide centre" {
		t.Fatalf("got %q, %v", n, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", MaxLayoutNameLen+1)} {
		if _, err := NormalizeLayoutName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NormalizeLayoutName(strings.Repeat("я", MaxLayoutNameLen)); err != nil {
		t.Errorf("a name of exactly %d characters (not bytes) must be fine: %v", MaxLayoutNameLen, err)
	}
}

func testGeometry() seating.Geometry {
	return seating.Geometry{
		Categories: []seating.Category{
			{Index: 1, Name: "Stalls", Color: "#112233", PriceHint: "4500"},
			{Index: 2, Name: "Balcony", Color: "bad"},
			{Index: 3, Name: "Zone", Kind: seating.KindGeneralAdmission},
			{Index: 4, Name: "Unused"},
		},
		Sections: []seating.Section{{Key: "A", Name: "A", Rows: []seating.Row{
			{Name: "1", Seats: []seating.Seat{
				{Key: "A|1|1", CategoryIndex: 1}, {Key: "A|1|2", CategoryIndex: 1}, {Key: "A|1|3", CategoryIndex: 2},
			}},
		}}},
	}
}

func TestFromGeometry_OneSlotPerUsedSeatedCategory(t *testing.T) {
	p, err := FromGeometry(testGeometry(), map[string]string{"2": "Gallery"})
	if err != nil {
		t.Fatalf("FromGeometry: %v", err)
	}
	if len(p.Slots) != 2 {
		t.Fatalf("slots = %+v, want the used seated categories only", p.Slots)
	}
	if p.Slots[0].Key != "c1" || p.Slots[0].Name != "Stalls" || p.Slots[0].Color != "#112233" || p.Slots[0].PriceAmount != 4500 {
		t.Errorf("slot 1 = %+v", p.Slots[0])
	}
	if p.Slots[1].Name != "Gallery" || p.Slots[1].Color != "" {
		t.Errorf("slot 2 = %+v (override applied, bad colour dropped)", p.Slots[1])
	}
	if p.Assignments["A|1|1"] != "c1" || p.Assignments["A|1|3"] != "c2" || len(p.Assignments) != 3 {
		t.Errorf("assignments = %v", p.Assignments)
	}
}

func TestFromGeometry_OverridesThatCollideFold(t *testing.T) {
	p, err := FromGeometry(testGeometry(), map[string]string{"2": "stalls"})
	if err != nil {
		t.Fatalf("FromGeometry: %v", err)
	}
	if len(p.Slots) != 1 || p.Assignments["A|1|3"] != "c1" {
		t.Fatalf("slots %+v assignments %v: two categories with one name must become one slot", p.Slots, p.Assignments)
	}
}

func TestFromSession_PicksUsedCategoriesAndClosedSeats(t *testing.T) {
	t1, t2, t3 := uuid.New(), uuid.New(), uuid.New()
	tiers := []gen.TicketTierRow{
		{ID: t1, Name: "Parter", PriceAmount: 1000, Currency: "EUR", SortOrder: 1},
		{ID: t2, Name: "Balcony", PriceAmount: 2000, Currency: "EUR", SortOrder: 0},
		{ID: t3, Name: "Unused", PriceAmount: 1, Currency: "EUR", SortOrder: 2},
	}
	seats := []gen.SessionSeatAssignmentRow{
		{SeatKey: "S|1", TierID: &t1, Status: "available"},
		{SeatKey: "S|2", TierID: &t2, Status: "unavailable"},
		{SeatKey: "S|3", TierID: &t2, Status: "sold"},
		{SeatKey: "S|4", Status: "available"},
	}
	p, err := FromSession(tiers, map[uuid.UUID]string{t2: "#00ff00"}, seats)
	if err != nil {
		t.Fatalf("FromSession: %v", err)
	}
	if len(p.Slots) != 2 || p.Slots[0].Name != "Balcony" || p.Slots[0].Color != "#00ff00" || p.Slots[1].Name != "Parter" {
		t.Fatalf("slots = %+v (sorted by sort_order, unused category left out)", p.Slots)
	}
	if len(p.Assignments) != 3 || p.Assignments["S|4"] != "" {
		t.Errorf("assignments = %v (a seat with no category is not assigned)", p.Assignments)
	}
	if len(p.ClosedSeatKeys) != 1 || p.ClosedSeatKeys[0] != "S|2" {
		t.Errorf("closed = %v", p.ClosedSeatKeys)
	}
}

func TestFromSession_EqualNamesStayDistinct(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	tiers := []gen.TicketTierRow{{ID: a, Name: "Same", SortOrder: 0}, {ID: b, Name: "same", SortOrder: 1}}
	seats := []gen.SessionSeatAssignmentRow{{SeatKey: "1", TierID: &a, Status: "available"}, {SeatKey: "2", TierID: &b, Status: "available"}}
	if _, err := FromSession(tiers, nil, seats); err != nil {
		t.Fatalf("two categories that differ only by case must not make an invalid layout: %v", err)
	}
}

func migration0171(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "migrations", "sql", "0171_seat_layouts.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return string(raw)
}

// TestLayoutSources_MatchMigrationCheckConstraint keeps the Go sources and
// the CHECK of seat_layouts.source (migration 0171) in step.
func TestLayoutSources_MatchMigrationCheckConstraint(t *testing.T) {
	m := regexp.MustCompile(`CHECK \(source IN \(([^)]*)\)\)`).FindStringSubmatch(migration0171(t))
	if m == nil {
		t.Fatal("source CHECK not found in migration 0171")
	}
	var sqlSources []string
	for _, part := range strings.Split(m[1], ",") {
		sqlSources = append(sqlSources, strings.Trim(strings.TrimSpace(part), "'"))
	}
	if strings.Join(sqlSources, ",") != strings.Join(LayoutSources, ",") {
		t.Fatalf("migration sources %v, Go sources %v", sqlSources, LayoutSources)
	}
}

// TestLayoutLimits_MatchMigration keeps the name length in step with its CHECK.
func TestLayoutLimits_MatchMigration(t *testing.T) {
	if !strings.Contains(migration0171(t), "BETWEEN 1 AND 120") || MaxLayoutNameLen != 120 {
		t.Fatalf("layout name length: Go %d, migration CHECK must say BETWEEN 1 AND 120", MaxLayoutNameLen)
	}
}

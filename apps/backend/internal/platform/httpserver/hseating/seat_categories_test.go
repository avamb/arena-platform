// seat_categories_test.go - S1a (execution spec 40, plan 26 section 1.1).
// The per-seat category truth and the three buyer renders that consume it:
// the /schema overlay, the BSS layout.svg and the Bil24 sbt/1.0 image must
// show ONE price for a seat after PATCH .../seats/category repainted it.
package hseating

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// s1aFixture: two plan categories (1 VIP with seats a1..a3, 2 Standard with
// b1..b2) and three tiers - VIP, Standard and Balcony, the last one the home
// tier of no plan category.
type s1aFixture struct {
	geom              seating.Geometry
	vip, std, balcony gen.TicketTierRow
}

func newS1aFixture() s1aFixture {
	seat := func(n string, cat int) seating.Seat {
		return seating.Seat{Key: "s|1|" + n, Number: n, X: 10, Y: 10, Radius: 5, CategoryIndex: cat}
	}
	return s1aFixture{
		geom: seating.Geometry{
			SchemaVersion: seating.SchemaVersion,
			Canvas:        seating.Canvas{Width: 400, Height: 200},
			Categories: []seating.Category{
				{Index: 1, Name: "VIP", Color: "#ff0000"},
				{Index: 2, Name: "Standard", Color: "#00aa00"},
			},
			Sections: []seating.Section{{
				Key: "s", Name: "S",
				Rows: []seating.Row{{
					Key: "1", Name: "1",
					Seats: []seating.Seat{
						seat("a1", 1), seat("a2", 1), seat("a3", 1), seat("a4", 1),
						seat("b1", 2), seat("b2", 2),
					},
				}},
			}},
		},
		vip:     gen.TicketTierRow{ID: uuid.New(), Name: "VIP", PricingMode: "fixed", PriceAmount: 5000, Currency: "EUR"},
		std:     gen.TicketTierRow{ID: uuid.New(), Name: "Standard", PricingMode: "fixed", PriceAmount: 2000, Currency: "EUR"},
		balcony: gen.TicketTierRow{ID: uuid.New(), Name: "Balcony", PricingMode: "fixed", PriceAmount: 1200, Currency: "EUR"},
	}
}

func (f s1aFixture) tiers() []gen.TicketTierRow {
	return []gen.TicketTierRow{f.vip, f.std, f.balcony}
}

// seats builds the session_seats rows; tierOf overrides the default tier of
// a seat key (a1..a4 -> VIP, b1..b2 -> Standard).
func (f s1aFixture) seats(tierOf map[string]*gen.TicketTierRow) []gen.SessionSeatRow {
	def := map[string]gen.TicketTierRow{"a1": f.vip, "a2": f.vip, "a3": f.vip, "a4": f.vip, "b1": f.std, "b2": f.std}
	var out []gen.SessionSeatRow
	for i, n := range []string{"a1", "a2", "a3", "a4", "b1", "b2"} {
		tier := def[n]
		tid := tier.ID
		tp := &tid
		if o, ok := tierOf[n]; ok {
			if o == nil {
				tp = nil
			} else {
				oid := o.ID
				tp = &oid
			}
		}
		out = append(out, gen.SessionSeatRow{
			ID: uuid.New(), SeatKey: "s|1|" + n, SeatNumber: n, SectorName: "S", RowName: "1",
			Status: "available", TierID: tp, SystemSeatID: int64(100 + i),
		})
	}
	return out
}

func TestResolveSeatCategories_NoRepaintChangesNothing(t *testing.T) {
	f := newS1aFixture()
	res := resolveSeatCategories(f.geom, f.seats(nil), f.tiers(), seatKeyIndex(f.geom))
	if len(res.SeatIndex) != 0 || len(res.Extra) != 0 {
		t.Fatalf("untouched session: overrides=%v extra=%v, want none", res.SeatIndex, res.Extra)
	}
	if res.Home[1].ID != f.vip.ID || res.Home[2].ID != f.std.ID {
		t.Fatalf("home tiers = %v", res.Home)
	}
}

// The old rule anchored a category on its FIRST seat: repainting a1 turned the
// whole VIP category into Standard. The home tier is the tier of most seats.
func TestResolveSeatCategories_RepaintingFirstSeatKeepsCategoryHome(t *testing.T) {
	f := newS1aFixture()
	seats := f.seats(map[string]*gen.TicketTierRow{"a1": &f.std})
	res := resolveSeatCategories(f.geom, seats, f.tiers(), seatKeyIndex(f.geom))
	if res.Home[1].ID != f.vip.ID {
		t.Fatalf("category 1 home = %q, want VIP", res.Home[1].Name)
	}
	if got := res.SeatIndex["s|1|a1"]; got != 2 {
		t.Fatalf("a1 effective category = %d, want 2 (the category home to Standard)", got)
	}
	if res.EffectiveIndex("s|1|a2", 1) != 1 {
		t.Fatal("untouched seat a2 left its plan category")
	}
	if len(res.Extra) != 0 {
		t.Fatalf("Standard is home to category 2, no synthetic category expected: %v", res.Extra)
	}
}

func TestResolveSeatCategories_TierWithoutHomeGetsSyntheticCategory(t *testing.T) {
	f := newS1aFixture()
	seats := f.seats(map[string]*gen.TicketTierRow{"a2": &f.balcony, "b2": &f.balcony})
	res := resolveSeatCategories(f.geom, seats, f.tiers(), seatKeyIndex(f.geom))
	if len(res.Extra) != 1 || res.Extra[0].Index != 3 || res.Extra[0].Tier.ID != f.balcony.ID {
		t.Fatalf("extra = %+v, want one synthetic category 3 for Balcony", res.Extra)
	}
	if res.SeatIndex["s|1|a2"] != 3 || res.SeatIndex["s|1|b2"] != 3 {
		t.Fatalf("overrides = %v, want a2 and b2 -> 3", res.SeatIndex)
	}
	cats := res.Categories(f.geom)
	if len(cats) != 3 || cats[2].Index != 3 || cats[2].Name != "Balcony" || cats[2].Color == "" {
		t.Fatalf("categories = %+v", cats)
	}
	if got := res.CategoryTiers()[3].PriceAmount; got != 1200 {
		t.Fatalf("synthetic category price = %d, want 1200", got)
	}
}

func TestResolveSeatCategories_TieGoesToFirstSeenAndSeatsWithoutTierStay(t *testing.T) {
	f := newS1aFixture()
	// Category 2 has b1=Standard, b2=VIP: a 1-1 tie, first seen (b1) wins.
	seats := f.seats(map[string]*gen.TicketTierRow{"b2": &f.vip})
	res := resolveSeatCategories(f.geom, seats, f.tiers(), seatKeyIndex(f.geom))
	if res.Home[2].ID != f.std.ID {
		t.Fatalf("tie: home of category 2 = %q, want Standard", res.Home[2].Name)
	}
	if res.SeatIndex["s|1|b2"] != 1 {
		t.Fatalf("b2 (VIP) -> %d, want 1", res.SeatIndex["s|1|b2"])
	}
	// A seat with no tier keeps its plan category and never anchors one.
	seats = f.seats(map[string]*gen.TicketTierRow{"b1": nil, "b2": nil})
	res = resolveSeatCategories(f.geom, seats, f.tiers(), seatKeyIndex(f.geom))
	if _, ok := res.Home[2]; ok || len(res.SeatIndex) != 0 {
		t.Fatalf("tier-less category: home=%v overrides=%v", res.Home, res.SeatIndex)
	}
}

// ── Three renders, one price ────────────────────────────────────────────────

var (
	reBSSSwatch = regexp.MustCompile(`<circle sbt:index="(\d+)" [^>]*sbt:price="(\d+)" sbt:currency="([^"]*)"`)
	reBSSSeat   = regexp.MustCompile(`<circle sbt:seat="([^"]*)" [^>]*sbt:cat="(\d+)"`)
	reSBT10Cat  = regexp.MustCompile(`<sbt:category sbt:id="(\d+)" sbt:index="(\d+)" [^>]*sbt:price="([^"]*)"`)
	reSBT10Seat = regexp.MustCompile(`<circle sbt:id="(\d+)" sbt:state="\d" sbt:cat="(\d+)" sbt:seat="([^"]*)"`)
)

// TestS1a_PatchedSeatShowsOnePriceInAllThreeRenders is the unit-level twin of
// the integration test: seat a1 is repainted VIP -> Standard and a2 VIP ->
// Balcony (a tier anchoring no plan category). The schema overlay, the BSS
// layout.svg and the sbt/1.0 image must all give each seat the price of ITS
// tier - the number GET_SEAT_LIST reads from session_seats.tier_id.
func TestS1a_PatchedSeatShowsOnePriceInAllThreeRenders(t *testing.T) {
	f := newS1aFixture()
	seats := f.seats(map[string]*gen.TicketTierRow{"a1": &f.std, "a2": &f.balcony})
	tiers := f.tiers()
	res := resolveSeatCategories(f.geom, seats, tiers, seatKeyIndex(f.geom))

	want := map[string]int64{ // seat number -> its own tier's price (minor units)
		"a1": f.std.PriceAmount, "a2": f.balcony.PriceAmount, "a3": f.vip.PriceAmount, "a4": f.vip.PriceAmount,
		"b1": f.std.PriceAmount, "b2": f.std.PriceAmount,
	}

	// 1. /schema overlay: seat_category_index -> category_prices/extra_categories.
	cats, extra := buildCategoryOverlay(f.geom, res, map[uuid.UUID]priceresolve.Effective{})
	priceByIndex := map[int]int64{}
	for _, e := range append(append([]map[string]any{}, cats...), extra...) {
		if p, ok := e["price_amount"].(int64); ok {
			priceByIndex[e["index"].(int)] = p
		}
	}
	idx := seatKeyIndex(f.geom)
	for key, seat := range idx {
		n := seat.Number
		got := priceByIndex[res.EffectiveIndex(key, seat.CategoryIndex)]
		if got != want[n] {
			t.Errorf("schema: seat %s price = %d, want %d", n, got, want[n])
		}
	}
	if len(extra) != 1 || extra[0]["index"] != 3 {
		t.Fatalf("schema extra_categories = %v", extra)
	}

	// 2. BSS layout.svg.
	bss := string(RenderBSSLayoutSVG(f.geom, seats, tiers, 5))
	bssPrice := map[int]int64{}
	for _, m := range reBSSSwatch.FindAllStringSubmatch(bss, -1) {
		i, _ := strconv.Atoi(m[1])
		p, _ := strconv.ParseInt(m[2], 10, 64)
		bssPrice[i] = p
	}
	for _, m := range reBSSSeat.FindAllStringSubmatch(bss, -1) {
		i, _ := strconv.Atoi(m[2])
		if bssPrice[i] != want[m[1]] {
			t.Errorf("layout.svg: seat %s (sbt:cat %d) price = %d, want %d\n%s", m[1], i, bssPrice[i], want[m[1]], bss)
		}
	}
	if len(reBSSSeat.FindAllStringSubmatch(bss, -1)) != 6 {
		t.Fatalf("layout.svg did not render six seats\n%s", bss)
	}

	// 3. Bil24 sbt/1.0 image. Category prices there are major units.
	catIDs := map[int]int64{1: 1000000001, 2: 1000000002, 3: 1000000003}
	img := string(RenderSBT10SVG(f.geom, seats, tiers, catIDs, 5))
	imgPrice := map[int]string{}
	imgID := map[int]string{}
	for _, m := range reSBT10Cat.FindAllStringSubmatch(img, -1) {
		i, _ := strconv.Atoi(m[2])
		imgPrice[i] = m[3]
		imgID[i] = m[1]
	}
	seatNum := map[string]bool{}
	for _, m := range reSBT10Seat.FindAllStringSubmatch(img, -1) {
		i, _ := strconv.Atoi(m[2])
		seatNum[m[3]] = true
		if wantMajor := formatMajor(want[m[3]]); imgPrice[i] != wantMajor {
			t.Errorf("image: seat %s (sbt:cat %d) price = %s, want %s\n%s", m[3], i, imgPrice[i], wantMajor, img)
		}
	}
	if len(seatNum) != 6 {
		t.Fatalf("image rendered %d seats, want 6\n%s", len(seatNum), img)
	}
	// The wire id of the synthetic category is the one the gateway minted for it.
	if imgID[3] != "1000000003" {
		t.Errorf("image: synthetic category id = %q, want 1000000003", imgID[3])
	}
	// Seat colour follows the effective category, not the plan one.
	if !strings.Contains(img, `sbt:seat="a2" cx="10" cy="10" r="5" fill="`+res.Extra[0].Color+`"`) {
		t.Errorf("image: seat a2 is not filled with the synthetic category colour\n%s", img)
	}
}

// TestS1a_SBT10CategoryTierIDsIncludesSyntheticCategories keeps sbt:id and
// sbt:price derived from one source: the exported map covers every category
// the encoder emits.
func TestS1a_SBT10CategoryTierIDsIncludesSyntheticCategories(t *testing.T) {
	f := newS1aFixture()
	seats := f.seats(map[string]*gen.TicketTierRow{"a2": &f.balcony})
	got := SBT10CategoryTierIDs(f.geom, seats, f.tiers())
	if got[1] != f.vip.ID || got[2] != f.std.ID || got[3] != f.balcony.ID || len(got) != 3 {
		t.Fatalf("SBT10CategoryTierIDs = %v", got)
	}
}

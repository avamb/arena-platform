package himports

// import_arena_seated_test.go — the pure parts of the seated branch of the
// arena event bundle (task S1b): how categoryList is matched onto a seating
// plan's categories, and how the read names a tier's plan category.

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// seatedTestGeometry: 1 Parter (two seats), 2 Balcony (one seat, display
// name override "Galerie"), 3 Standing (a GA zone of 50), 4 Legend (a
// seated legend entry no seat uses — it owns no place).
func seatedTestGeometry() seating.Geometry {
	return seating.Canonicalize(seating.Geometry{
		Canvas: seating.Canvas{Width: 100, Height: 100},
		Categories: []seating.Category{
			{Index: 1, Name: "Parter", Color: "#111111"},
			{Index: 2, Name: "Balcony", Color: "#222222"},
			{Index: 3, Name: "Standing", Color: "#333333", Kind: seating.KindGeneralAdmission, Capacity: 50},
			{Index: 4, Name: "Legend", Color: "#444444"},
		},
		Sections: []seating.Section{{Key: "s", Name: "Hall", Rows: []seating.Row{
			{Key: "a", Name: "A", Seats: []seating.Seat{
				{Key: "s|a|1", Number: "1", X: 10, Y: 10, Radius: 2, CategoryIndex: 1},
				{Key: "s|a|2", Number: "2", X: 20, Y: 10, Radius: 2, CategoryIndex: 1},
			}},
			{Key: "b", Name: "B", Seats: []seating.Seat{
				{Key: "s|b|1", Number: "1", X: 10, Y: 30, Radius: 2, CategoryIndex: 2},
			}},
		}}},
	})
}

func cat(name string, index *int) bil24compat.ImportSessionCategory {
	return bil24compat.ImportSessionCategory{CategoryPriceName: name, Price: 10, PlanCategoryIndex: index}
}

func importErrOf(t *testing.T, err error) *importError {
	t.Helper()
	var ie *importError
	if !errors.As(err, &ie) {
		t.Fatalf("error %v is not an *importError", err)
	}
	return ie
}

func TestPlanCategoryPairs_MatchesByNameOverrideAndIndex(t *testing.T) {
	g := seatedTestGeometry()
	names := map[int]string{2: "Galerie"}

	// By name, case- and space-insensitive; the display-name override and the
	// geometry's own name both count; an explicit index wins over the name.
	list := []bil24compat.ImportSessionCategory{
		cat("  parter ", nil),
		cat("Galerie", nil),
		cat("Fans on the floor", ptr(3)),
	}
	pairs, errs := planCategoryPairs(list, g, names)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if pairs[0] != 1 || pairs[1] != 2 || pairs[2] != 3 {
		t.Fatalf("pairs = %v, want [1 2 3]", pairs)
	}
	if _, errs := planCategoryPairs([]bil24compat.ImportSessionCategory{cat("Parter", nil), cat("balcony", nil), cat("Standing", nil)}, g, names); len(errs) != 0 {
		t.Fatalf("the geometry name still matches next to an override: %v", errs)
	}
}

func TestPlanCategoryPairs_RefusesWithTheFieldPath(t *testing.T) {
	g := seatedTestGeometry()
	cases := []struct {
		name  string
		list  []bil24compat.ImportSessionCategory
		code  string
		field string
	}{
		{"unknown name", []bil24compat.ImportSessionCategory{cat("Parter", nil), cat("Balcony", nil), cat("VIP", nil)},
			"import.seating_category_unmatched", "/categoryList/2/categoryPriceName"},
		{"index of a category without places", []bil24compat.ImportSessionCategory{cat("x", ptr(4))},
			"import.seating_category_unmatched", "/categoryList/0/planCategoryIndex"},
		{"index outside the plan", []bil24compat.ImportSessionCategory{cat("x", ptr(9))},
			"import.seating_category_unmatched", "/categoryList/0/planCategoryIndex"},
		{"empty name without an index", []bil24compat.ImportSessionCategory{cat(" ", nil)},
			"import.seating_category_unmatched", "/categoryList/0/categoryPriceName"},
		{"two entries for one category", []bil24compat.ImportSessionCategory{cat("Parter", nil), cat("Stalls", ptr(1))},
			"import.seating_category_duplicate", "/categoryList/1/planCategoryIndex"},
		{"a plan category nobody prices", []bil24compat.ImportSessionCategory{cat("Parter", nil), cat("Standing", nil)},
			"import.seating_category_missing", "/categoryList"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := planCategoryPairs(tc.list, g, nil)
			if len(errs) == 0 {
				t.Fatal("no error")
			}
			ie := importErrOf(t, errs[0])
			if ie.code != tc.code || ie.field != tc.field || ie.status != 422 {
				t.Fatalf("got %s %s %d (%s), want %s %s 422", ie.code, ie.field, ie.status, ie.message, tc.code, tc.field)
			}
		})
	}
}

func TestPlanCategoryPairs_ReportsEveryEntryAndMissingOnlyAlone(t *testing.T) {
	g := seatedTestGeometry()
	_, errs := planCategoryPairs([]bil24compat.ImportSessionCategory{cat("A", nil), cat("B", nil)}, g, nil)
	if len(errs) != 2 {
		t.Fatalf("errors = %v, want one per entry", errs)
	}
	for _, err := range errs {
		if importErrOf(t, err).code == "import.seating_category_missing" {
			t.Fatal("the missing-category error is only reported once the entries themselves are right")
		}
	}
}

func TestPlanCategoryPairs_AmbiguousName(t *testing.T) {
	g := seatedTestGeometry()
	names := map[int]string{2: "Parter"}
	_, errs := planCategoryPairs([]bil24compat.ImportSessionCategory{cat("Parter", nil)}, g, names)
	if len(errs) == 0 || importErrOf(t, errs[0]).field != "/categoryList/0/categoryPriceName" {
		t.Fatalf("a name shared by two plan categories must be refused: %v", errs)
	}
}

func TestSeatedEntries_MarksSeatedPairsOnly(t *testing.T) {
	sp := &arenaSeatedPlan{geometry: seatedTestGeometry(), pairs: []int{1, 3, 2}}
	got := sp.seatedEntries()
	if !got[0] || got[1] || !got[2] {
		t.Fatalf("seatedEntries = %v, want seat, zone, seat", got)
	}
	var none *arenaSeatedPlan
	if none.seatedEntries() != nil {
		t.Fatal("a general-admission bundle has no seated entries")
	}
}

func TestPlanCategoryHomes_MajorityTierAndSeatedSet(t *testing.T) {
	g := seatedTestGeometry()
	parter, balcony, standing, repaint := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seats := []gen.SessionSeatRow{
		{SeatKey: "s|a|1", Kind: "seat", TierID: &parter},
		// One Parter seat repainted into another tier: Parter stays home.
		{SeatKey: "s|a|2", Kind: "seat", TierID: &repaint},
		{SeatKey: "s|b|1", Kind: "seat", TierID: &balcony},
		{SeatKey: "ga|c3|000001", Kind: "ga_unit", TierID: &standing},
		{SeatKey: "ga|c3|000002", Kind: "ga_unit", TierID: &standing},
		// A quota place of the zone's category: not a plan place.
		{SeatKey: "ga|t1|000001", Kind: "ga_unit", TierID: &repaint},
	}
	homes, seated := planCategoryHomes(&g, seats)
	if homes[parter] != 1 || homes[balcony] != 2 || homes[standing] != 3 {
		t.Fatalf("homes = %v", homes)
	}
	if _, ok := homes[repaint]; ok {
		t.Fatal("a tier that is home to no plan category gets no index (tie on Parter goes to the smaller seat key)")
	}
	if !seated[parter] || !seated[balcony] || !seated[repaint] || seated[standing] {
		t.Fatalf("seated = %v", seated)
	}
	if h, s := planCategoryHomes(nil, seats); len(h) != 0 || len(s) != 0 {
		t.Fatal("without a plan nothing is named")
	}
}

func TestZoneIndexOfKey(t *testing.T) {
	for key, want := range map[string]int{"ga|c3|000001": 3, "ga|c12|7": 12} {
		if got, ok := zoneIndexOfKey(key); !ok || got != want {
			t.Errorf("zoneIndexOfKey(%q) = %d %v, want %d", key, got, ok, want)
		}
	}
	for _, key := range []string{"ga|t3|000001", "s|a|1", "ga|cx|1"} {
		if _, ok := zoneIndexOfKey(key); ok {
			t.Errorf("zoneIndexOfKey(%q) must not be a plan zone", key)
		}
	}
}

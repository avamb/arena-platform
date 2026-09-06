// seating_518_test.go — unit coverage for the seated half of the Bil24 session
// import (feature #518, W1-C3d; spec §13.2 step 6).
//
// Only the DB-free parts of the algorithm are asserted here: admission-mode
// derivation, the GA-category synthesis that makes a payload hybrid, and the
// mode → seating_plans.plan_type mapping. The stateful half (plan/version
// upsert, binding, materialization, the 409 on a session with sales) needs a
// live database and is covered end-to-end by scenario 8 of the contract
// harness, tests/compat/bil24/scenario08_import_test.go.
package himports

import (
	"strconv"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// sbtPlanWithSeats builds a parsed sbt plan carrying n seats in one category,
// standing in for whatever ImportSBTSVG would have returned. Going through the
// parser here would test the parser, not the import.
func sbtPlanWithSeats(t *testing.T, categoryExternalID int64, seats int) seating.SBTPlan {
	t.Helper()
	geometry := seating.Geometry{
		Categories: []seating.Category{{
			Index:      1,
			Name:       "Parter",
			Kind:       seating.KindSeated,
			ExternalID: categoryExternalID,
		}},
	}
	if seats > 0 {
		row := seating.Row{Name: "1"}
		for i := 1; i <= seats; i++ {
			row.Seats = append(row.Seats, seating.Seat{
				Number:        strconv.Itoa(i),
				CategoryIndex: 1,
				X:             float64(i * 10),
				Y:             10,
			})
		}
		geometry.Sections = []seating.Section{{Name: "Parter", Rows: []seating.Row{row}}}
	}
	return seating.SBTPlan{Geometry: geometry}
}

func importPlanWithCategories(cats ...bil24compat.ImportSessionCategory) importPlan {
	return importPlan{Request: bil24compat.ImportSessionRequest{CategoryList: cats}}
}

// TestBuildImportGeometry_AdmissionModes pins the hybrid-detection rule of spec
// §13.2 step 6: placed seats alone are assigned_seats, a placement:false quota
// alongside them makes the session hybrid, and geometry without seats is pure
// general admission.
func TestBuildImportGeometry_AdmissionModes(t *testing.T) {
	const seatedCat, gaCat = 4001, 4002
	h := &Handler{}

	seated := bil24compat.ImportSessionCategory{
		CategoryPriceID: seatedCat, CategoryPriceName: "Parter",
		Placement: true, Availability: 4,
	}
	ga := bil24compat.ImportSessionCategory{
		CategoryPriceID: gaCat, CategoryPriceName: "Standing",
		Placement: false, Availability: 30,
	}

	t.Run("placed seats only → assigned_seats", func(t *testing.T) {
		_, mode := h.buildImportGeometry(sbtPlanWithSeats(t, seatedCat, 4), importPlanWithCategories(seated))
		if mode != "assigned_seats" {
			t.Fatalf("mode = %q, want assigned_seats", mode)
		}
	})

	t.Run("placed seats plus an unplaced quota → hybrid", func(t *testing.T) {
		geometry, mode := h.buildImportGeometry(sbtPlanWithSeats(t, seatedCat, 4), importPlanWithCategories(seated, ga))
		if mode != "hybrid" {
			t.Fatalf("mode = %q, want hybrid", mode)
		}
		// The quota has no geometry in the svg, so the import must synthesize
		// a general-admission category for it — otherwise the standing
		// inventory would simply not exist.
		var gaCategory *seating.Category
		for i := range geometry.Categories {
			if geometry.Categories[i].ExternalID == gaCat {
				gaCategory = &geometry.Categories[i]
			}
		}
		if gaCategory == nil {
			t.Fatalf("no synthesized GA category for %d: %+v", gaCat, geometry.Categories)
		}
		if gaCategory.Kind != seating.KindGeneralAdmission {
			t.Errorf("synthesized category kind = %q, want %q", gaCategory.Kind, seating.KindGeneralAdmission)
		}
		if gaCategory.Capacity != 30 {
			t.Errorf("synthesized category capacity = %d, want 30 (the payload availability)", gaCategory.Capacity)
		}
	})

	t.Run("no seats → general_admission", func(t *testing.T) {
		_, mode := h.buildImportGeometry(sbtPlanWithSeats(t, seatedCat, 0), importPlanWithCategories(ga))
		if mode != "general_admission" {
			t.Fatalf("mode = %q, want general_admission", mode)
		}
	})
}

// TestBuildImportGeometry_PlacedCategoryIsNotDoubleCounted guards the subtle
// case where a category is drawn in the svg AND declared with placement:false.
// Its inventory is the seats; adding a GA block on top would sell the same
// capacity twice.
func TestBuildImportGeometry_PlacedCategoryIsNotDoubleCounted(t *testing.T) {
	const seatedCat = 4101
	h := &Handler{}
	contradictory := bil24compat.ImportSessionCategory{
		CategoryPriceID: seatedCat, CategoryPriceName: "Parter",
		Placement: false, Availability: 99,
	}

	geometry, mode := h.buildImportGeometry(sbtPlanWithSeats(t, seatedCat, 4), importPlanWithCategories(contradictory))
	if mode != "assigned_seats" {
		t.Errorf("mode = %q, want assigned_seats — the category is drawn in the svg", mode)
	}
	for _, c := range geometry.Categories {
		if c.ExternalID == seatedCat && c.Kind == seating.KindGeneralAdmission {
			t.Fatalf("category %d was synthesized as a GA block despite being seated in the svg: %+v", seatedCat, c)
		}
	}
}

// TestImportPlanTypeForMode pins the mode → seating_plans.plan_type mapping.
// The names differ on purpose: the wire/admission vocabulary says "hybrid",
// the seating_plans CHECK constraint says "mixed".
func TestImportPlanTypeForMode(t *testing.T) {
	want := map[string]string{
		"assigned_seats":    "assigned_seats",
		"hybrid":            "mixed",
		"general_admission": "general_admission",
	}
	if len(importPlanTypeForMode) != len(want) {
		t.Fatalf("importPlanTypeForMode has %d entries, want %d: %v",
			len(importPlanTypeForMode), len(want), importPlanTypeForMode)
	}
	for mode, planType := range want {
		if got := importPlanTypeForMode[mode]; got != planType {
			t.Errorf("importPlanTypeForMode[%q] = %q, want %q", mode, got, planType)
		}
	}
}

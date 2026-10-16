package seatmint

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

func geometry(seats bool, zone bool) seating.Geometry {
	g := seating.Geometry{Categories: []seating.Category{{Index: 1, Name: "Parter"}, {Index: 2, Name: "Legend"}}}
	if seats {
		g.Sections = []seating.Section{{Key: "s", Rows: []seating.Row{{Key: "a", Seats: []seating.Seat{
			{Key: "s|a|1", CategoryIndex: 1}, {Key: "s|a|2", CategoryIndex: 1},
		}}}}}
	}
	if zone {
		g.Categories = append(g.Categories, seating.Category{Index: 3, Name: "Floor", Kind: seating.KindGeneralAdmission, Capacity: 40})
	}
	return g
}

func TestAdmissionMode(t *testing.T) {
	cases := map[string]seating.Geometry{
		ModeHybrid:           geometry(true, true),
		ModeAssignedSeats:    geometry(true, false),
		ModeGeneralAdmission: geometry(false, true),
	}
	for want, g := range cases {
		if got := AdmissionMode(g); got != want {
			t.Errorf("AdmissionMode = %s, want %s", got, want)
		}
	}
}

func TestPlanCapacity(t *testing.T) {
	v := gen.SeatingPlanVersionRow{CapacitySeated: 120, CapacityStanding: 30}
	if PlanCapacity(ModeHybrid, v) != 150 || PlanCapacity(ModeAssignedSeats, v) != 120 || PlanCapacity(ModeGeneralAdmission, v) != 30 {
		t.Fatal("plan capacity per mode")
	}
}

func TestPlacedCategories_SkipsALegendEntryWithoutSeats(t *testing.T) {
	got := PlacedCategories(geometry(true, true))
	if !got[1] || got[2] || !got[3] || len(got) != 2 {
		t.Fatalf("placed = %v, want {1, 3}", got)
	}
}

func TestZoneKeyPrefix(t *testing.T) {
	if ZoneKeyPrefix(7) != "ga|c7" {
		t.Fatal(ZoneKeyPrefix(7))
	}
}

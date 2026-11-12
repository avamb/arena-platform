package layoutgen

import (
	"math"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSellThroughRates(t *testing.T) {
	cases := []struct {
		name   string
		prices []int64
		want   []float64
	}{
		{"empty", nil, []float64{}},
		{"one level", []int64{5000}, []float64{0.75}},
		{"one level repeated", []int64{5000, 5000, 5000}, []float64{0.75, 0.75, 0.75}},
		{"two levels", []int64{1000, 3000}, []float64{0.60, 0.90}},
		{"three levels", []int64{3000, 2000, 1000}, []float64{0.90, 0.75, 0.60}},
		// Rank among DISTINCT levels, not by price distance: 3000/2900/100
		// is still 0.90/0.75/0.60, and a repeated price shares its rate.
		{"rank not distance", []int64{3000, 2900, 2900, 100}, []float64{0.90, 0.75, 0.75, 0.60}},
		{"four levels", []int64{4, 3, 2, 1}, []float64{0.90, 0.80, 0.70, 0.60}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SellThroughRates(tc.prices)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if !approx(got[i], tc.want[i]) {
					t.Fatalf("rate[%d] = %v, want %v (all %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
	if SellThroughDearest != 0.90 || SellThroughCheapest != 0.60 {
		t.Fatalf("constants moved: %v / %v", SellThroughDearest, SellThroughCheapest)
	}
}

func TestEvaluate_RealisticRevenue(t *testing.T) {
	seat := func(k string) seating.Seat { return seating.Seat{Key: k} }
	g := seating.Geometry{Sections: []seating.Section{{Key: "S", Rows: []seating.Row{{Key: "A", Seats: []seating.Seat{
		seat("a1"), seat("a2"), seat("a3"), seat("a4"), seat("a5"), seat("a6"), seat("a7"),
	}}}}}}
	l := Layout{
		Slots: []Slot{{Key: "c1", PriceAmount: 1001}, {Key: "c2", PriceAmount: 333}},
		Assignments: map[string]string{
			"a1": "c1", "a2": "c1", // open, dearest: 2 x 1001 x 0.90 = 1801.8
			"a3": "c2", // open, cheapest: 333 x 0.60 = 199.8
			"a4": "c2", // closed: nothing
		},
	}
	st := SessionState{
		Currency: "EUR",
		SoldHeld: map[string]FrozenSeat{
			"a5": {Status: StatusSold, PriceMinor: 777}, // in full
			"a6": {Status: StatusHeld, PriceMinor: 900}, // nearest level 1001: 900 x 0.90 = 810
		},
		Closed: map[string]bool{"a4": true},
	}
	m := Evaluate(g, l, st)
	// 777 + 1801.8 + 199.8 + 810 = 3588.6 -> 3589 (a7 has no slot).
	if m.RealisticRevenueMinor != 3589 {
		t.Fatalf("realistic = %d, want 3589 (%+v)", m.RealisticRevenueMinor, m)
	}
	if m.RevenueEstimateMinor != 777+900+2*1001+333 {
		t.Fatalf("estimate = %d", m.RevenueEstimateMinor)
	}
	if m.RealisticRevenueMinor > m.RevenueEstimateMinor {
		t.Fatal("the realistic estimate cannot exceed the full one")
	}

	// Half up: one level (0.75) x 2 seats x 1 = 1.5 -> 2.
	one := Layout{Slots: []Slot{{Key: "c1", PriceAmount: 1}}, Assignments: map[string]string{"a1": "c1", "a2": "c1"}}
	if m := Evaluate(g, one, SessionState{}); m.RealisticRevenueMinor != 2 {
		t.Fatalf("half up: %d, want 2", m.RealisticRevenueMinor)
	}
	// Only sold seats: the realistic figure is exactly the sold revenue.
	sold := SessionState{SoldHeld: map[string]FrozenSeat{"a1": {Status: StatusSold, PriceMinor: 1234}}}
	if m := Evaluate(g, Layout{Slots: []Slot{{Key: "c1", PriceAmount: 50}}, Assignments: map[string]string{}}, sold); m.RealisticRevenueMinor != 1234 {
		t.Fatalf("sold only: %d, want 1234", m.RealisticRevenueMinor)
	}
}

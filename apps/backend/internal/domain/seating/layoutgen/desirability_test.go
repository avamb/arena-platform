package layoutgen

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// akropolisStage is the stage of the Palác Akropolis fixture: the parterre
// starts at y≈49 at the top of the canvas and the balconies sit at the
// bottom, so the stage is above the parterre, centred.
var akropolisStage = &seating.Point{X: 152, Y: 20}

func loadAkropolis(t testing.TB) seating.Geometry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "Palac_Akropolis.svg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	g, _, errs := seating.ImportSVG(raw)
	if len(errs) != 0 {
		t.Fatalf("import: %v", errs)
	}
	return seating.Canonicalize(g)
}

// values validates raw JSON parameters against the desirability schema.
func values(t testing.TB, raw string) Values {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad test params %s: %v", raw, err)
	}
	v, err := Desirability{}.Params().Validate(m)
	if err != nil {
		t.Fatalf("validate %s: %v", raw, err)
	}
	return v
}

func runAkropolis(t testing.TB, g seating.Geometry, raw string, st SessionState) (Layout, Metrics) {
	t.Helper()
	if st.Currency == "" {
		st.Currency = "CZK"
	}
	l, m, err := Run(Desirability{}, g, Annotations{FocalPoint: akropolisStage}, st, values(t, raw))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return l, m
}

func slotIndex(l Layout) map[string]int {
	out := map[string]int{}
	for i, s := range l.Slots {
		out[s.Key] = i
	}
	return out
}

func TestDesirability_Akropolis_DeterministicAndComplete(t *testing.T) {
	g := loadAkropolis(t)
	const params = `{"categories":4,"price_max":175000,"price_min":80000}`
	l1, m1 := runAkropolis(t, g, params, SessionState{})
	l2, m2 := runAkropolis(t, g, params, SessionState{})
	b1, _ := json.Marshal(struct {
		L Layout
		M Metrics
	}{l1, m1})
	b2, _ := json.Marshal(struct {
		L Layout
		M Metrics
	}{l2, m2})
	if string(b1) != string(b2) {
		t.Fatal("two runs on the same input differ")
	}
	if len(l1.Assignments) != g.SeatCount() || m1.TotalSeats != g.SeatCount() {
		t.Fatalf("assigned %d of %d seats", len(l1.Assignments), g.SeatCount())
	}
	if len(l1.Slots) != 4 || l1.Slots[0].PriceAmount != 175000 || l1.Slots[3].PriceAmount != 80000 {
		t.Fatalf("slots = %+v", l1.Slots)
	}
	for i := 1; i < len(l1.Slots); i++ {
		if l1.Slots[i].PriceAmount > l1.Slots[i-1].PriceAmount {
			t.Fatalf("prices not descending: %+v", l1.Slots)
		}
	}
	if m1.RevenueSoldMinor != 0 || m1.SoldFrozen != 0 || m1.RevenueEstimateMinor != m1.RevenueRemainingMinor {
		t.Fatalf("a session without sales must estimate only the remaining seats: %+v", m1)
	}

	// The parterre is nearer the stage than the centre balcony, so on
	// average it must be dearer (a lower slot index).
	idx := slotIndex(l1)
	mean := func(section string) float64 {
		var sum, n float64
		for _, sec := range g.Sections {
			if sec.Key != section {
				continue
			}
			for _, r := range sec.Rows {
				for _, s := range r.Seats {
					sum += float64(idx[l1.Assignments[s.Key]])
					n++
				}
			}
		}
		return sum / n
	}
	if p, b := mean("parter"), mean("balcony-center"); p >= b {
		t.Fatalf("parterre mean slot %.2f is not dearer than the centre balcony %.2f", p, b)
	}
	if got := l1.Assignments[g.Sections[3].Rows[0].Seats[0].Key]; got == "" {
		t.Fatal("first parterre seat has no slot")
	}
}

// Whole rows (the default) keep every seat of a row in one slot.
func TestDesirability_WholeRowsHaveOneSlot(t *testing.T) {
	g := loadAkropolis(t)
	l, _ := runAkropolis(t, g, `{"categories":5,"price_max":175000,"price_min":60000,"share_template":"equal"}`, SessionState{})
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			first := l.Assignments[r.Seats[0].Key]
			for _, s := range r.Seats {
				if l.Assignments[s.Key] != first {
					t.Fatalf("row %s/%s is split: %s vs %s", sec.Key, r.Key, first, l.Assignments[s.Key])
				}
			}
		}
	}
}

// The share templates are honoured up to the granularity of a row.
func TestDesirability_SharesFollowTheTemplate(t *testing.T) {
	g := loadAkropolis(t)
	maxRow := 0
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			maxRow = max(maxRow, len(r.Seats))
		}
	}
	tolerance := float64(maxRow) / float64(g.SeatCount())
	for _, tmpl := range []string{SharePyramid, ShareEqual, ShareWideMiddle} {
		l, m := runAkropolis(t, g, `{"categories":4,"price_max":175000,"price_min":80000,"share_template":"`+tmpl+`"}`, SessionState{})
		want := ShareTemplate(tmpl, 4)
		for i, s := range l.Slots {
			if math.Abs(s.Share-want[i]) > 1e-9 {
				t.Fatalf("%s: slot %d target share %.3f, want %.3f", tmpl, i, s.Share, want[i])
			}
			if got := m.BySlot[i].Share; math.Abs(got-want[i]) > tolerance+1e-9 {
				t.Fatalf("%s: slot %d actual share %.3f, target %.3f, tolerance %.3f", tmpl, i, got, want[i], tolerance)
			}
		}
	}
}

func TestDesirability_ExplicitShares(t *testing.T) {
	g := loadAkropolis(t)
	l, m := runAkropolis(t, g, `{"categories":2,"prices":[150000,90000],"shares":[50,50]}`, SessionState{})
	if l.Slots[0].PriceAmount != 150000 || l.Slots[1].PriceAmount != 90000 {
		t.Fatalf("explicit prices not used: %+v", l.Slots)
	}
	if math.Abs(m.BySlot[0].Share-0.5) > 0.1 {
		t.Fatalf("explicit 50/50 shares gave %.3f", m.BySlot[0].Share)
	}
}

func TestDesirability_OneCategory(t *testing.T) {
	g := loadAkropolis(t)
	l, m := runAkropolis(t, g, `{"categories":1,"price_max":99000,"price_min":10000}`, SessionState{})
	if len(l.Slots) != 1 || l.Slots[0].PriceAmount != 99000 || l.Slots[0].Share != 1 {
		t.Fatalf("one category = %+v", l.Slots)
	}
	for k, v := range l.Assignments {
		if v != "c1" {
			t.Fatalf("seat %s in %s", k, v)
		}
	}
	if m.RevenueEstimateMinor != int64(g.SeatCount())*99000 {
		t.Fatalf("revenue %d", m.RevenueEstimateMinor)
	}
}

func TestDesirability_FifteenCategories(t *testing.T) {
	g := loadAkropolis(t)
	l, m := runAkropolis(t, g, `{"categories":15,"price_max":300000,"price_min":30000,"share_template":"equal"}`, SessionState{})
	if len(l.Slots) != 15 || len(m.BySlot) != 15 {
		t.Fatalf("got %d slots", len(l.Slots))
	}
	var shares float64
	used := map[string]bool{}
	for i, s := range l.Slots {
		shares += s.Share
		if s.Key != SlotKey(i) || s.Color == "" {
			t.Fatalf("slot %d = %+v", i, s)
		}
		if i > 0 && s.PriceAmount > l.Slots[i-1].PriceAmount {
			t.Fatalf("prices not descending at %d", i)
		}
	}
	for _, v := range l.Assignments {
		used[v] = true
	}
	if math.Abs(shares-1) > 1e-9 || len(l.Assignments) != g.SeatCount() {
		t.Fatalf("shares sum %.6f, assigned %d", shares, len(l.Assignments))
	}
	if len(used) < 10 {
		t.Fatalf("only %d of 15 slots used on a 22-row hall", len(used))
	}

	// 16 is beyond the schema.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"categories":16,"price_max":1,"price_min":1}`), &raw)
	if _, err := (Desirability{}).Params().Validate(raw); err == nil {
		t.Fatal("16 categories accepted")
	}
}

// The edge option cuts long rows so an edge is never dearer than its own
// middle, and on Akropolis some edge does end up cheaper.
func TestDesirability_CheaperRowEdges(t *testing.T) {
	g := loadAkropolis(t)
	l, _ := runAkropolis(t, g, `{"categories":6,"price_max":175000,"price_min":50000,"share_template":"equal","cheaper_row_edges":true}`, SessionState{})
	idx := slotIndex(l)
	cheaperSomewhere := false
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			if len(r.Seats) < edgeMinSeats {
				continue
			}
			seats := Seats(seating.Geometry{Sections: []seating.Section{{Key: sec.Key, Rows: []seating.Row{r}}}})
			ix := make([]int, len(seats))
			for i := range ix {
				ix[i] = i
			}
			left, mid, right := splitRow(seats, ix)
			if len(mid) == 0 {
				t.Fatalf("row %s/%s has no middle", sec.Key, r.Key)
			}
			m := idx[l.Assignments[seats[mid[0]].Key]]
			for _, part := range [][]int{left, right} {
				for _, i := range part {
					e := idx[l.Assignments[seats[i].Key]]
					if e < m {
						t.Fatalf("edge seat %s (slot %d) dearer than its middle (slot %d)", seats[i].Key, e, m)
					}
					if e > m {
						cheaperSomewhere = true
					}
				}
			}
		}
	}
	if !cheaperSomewhere {
		t.Fatal("no row edge came out cheaper than its middle")
	}
}

// Sold and held seats are never assigned, count at their own price, and the
// free seats get exactly the slots a fresh proposal would give them.
func TestDesirability_FrozenSeatsStayAndCountByFact(t *testing.T) {
	g := loadAkropolis(t)
	const params = `{"categories":4,"price_max":175000,"price_min":80000}`
	fresh, _ := runAkropolis(t, g, params, SessionState{})

	seats := Seats(g)
	st := SessionState{SoldHeld: map[string]FrozenSeat{}, Closed: map[string]bool{}}
	var soldSum, heldSum int64
	for i, s := range seats {
		switch {
		case i%7 == 0:
			st.SoldHeld[s.Key] = FrozenSeat{Status: StatusSold, PriceMinor: 123400}
			soldSum += 123400
		case i%11 == 0:
			st.SoldHeld[s.Key] = FrozenSeat{Status: StatusHeld, PriceMinor: 50000}
			heldSum += 50000
		case i%13 == 0:
			st.Closed[s.Key] = true
		}
	}
	l, m := runAkropolis(t, g, params, st)

	for k := range st.SoldHeld {
		if _, ok := l.Assignments[k]; ok {
			t.Fatalf("frozen seat %s was assigned", k)
		}
	}
	if len(l.FrozenSeatKeys) != len(st.SoldHeld) || !sort.StringsAreSorted(l.FrozenSeatKeys) {
		t.Fatalf("frozen keys %d, want %d sorted", len(l.FrozenSeatKeys), len(st.SoldHeld))
	}
	for k, v := range l.Assignments {
		if fresh.Assignments[k] != v {
			t.Fatalf("free seat %s moved from %s to %s because of the sales", k, fresh.Assignments[k], v)
		}
	}
	if m.RevenueSoldMinor != soldSum || m.RevenueHeldMinor != heldSum {
		t.Fatalf("sold %d held %d, want %d %d", m.RevenueSoldMinor, m.RevenueHeldMinor, soldSum, heldSum)
	}
	var remaining int64
	price := map[string]int64{}
	for _, s := range l.Slots {
		price[s.Key] = s.PriceAmount
	}
	for k, v := range l.Assignments {
		if !st.Closed[k] {
			remaining += price[v]
		}
	}
	if m.RevenueRemainingMinor != remaining {
		t.Fatalf("remaining %d, want %d (closed seats excluded)", m.RevenueRemainingMinor, remaining)
	}
	if m.RevenueEstimateMinor != soldSum+heldSum+remaining {
		t.Fatalf("estimate %d is not sold + held + remaining", m.RevenueEstimateMinor)
	}
	if m.SoldFrozen != len(st.SoldHeld) || m.ClosedSeats != len(st.Closed) {
		t.Fatalf("counts %+v", m)
	}
}

func TestDesirability_NeedsTheStage(t *testing.T) {
	g := loadAkropolis(t)
	_, _, err := Run(Desirability{}, g, Annotations{}, SessionState{Currency: "CZK"},
		values(t, `{"price_max":1000,"price_min":500}`))
	var req *AnnotationRequiredError
	if !errors.As(err, &req) || req.Annotation != AnnotationFocalPoint {
		t.Fatalf("err = %v, want focal_point required", err)
	}
}

func TestDesirability_SectionWeightAndObstruction(t *testing.T) {
	g := loadAkropolis(t)
	const params = `{"categories":3,"price_max":150000,"price_min":50000,"share_template":"equal"}`
	v := values(t, params)
	base, _, err := Run(Desirability{}, g, Annotations{FocalPoint: akropolisStage}, SessionState{Currency: "CZK"}, v)
	if err != nil {
		t.Fatal(err)
	}
	front := g.Sections[3].Rows[0] // parterre, nearest row group
	keys := []string{}
	for _, s := range front.Seats {
		keys = append(keys, s.Key)
	}
	weighted, _, err := Run(Desirability{}, g, Annotations{
		FocalPoint:         akropolisStage,
		SectionWeights:     map[string]float64{"parter": 0},
		ObstructedSeatKeys: keys,
	}, SessionState{Currency: "CZK"}, v)
	if err != nil {
		t.Fatal(err)
	}
	bi, wi := slotIndex(base), slotIndex(weighted)
	if wi[weighted.Assignments[keys[0]]] <= bi[base.Assignments[keys[0]]] {
		t.Fatalf("weight 0 and obstruction did not make %s cheaper", keys[0])
	}

	if _, _, err := Run(Desirability{}, g, Annotations{
		FocalPoint: akropolisStage, SectionWeights: map[string]float64{"parter": 2},
	}, SessionState{}, v); err == nil {
		t.Fatal("weight 2 accepted")
	}
}

// syntheticHall builds a hall of rows x perRow seats facing a stage at the top.
func syntheticHall(rows, perRow int) seating.Geometry {
	g := seating.Geometry{Canvas: seating.Canvas{Width: 2000, Height: 2000}}
	sections := []string{"left", "centre", "right"}
	for si, name := range sections {
		sec := seating.Section{Key: name, Name: name}
		for r := 0; r < rows; r++ {
			row := seating.Row{Key: rowKey(r), Name: rowKey(r)}
			for n := 0; n < perRow; n++ {
				x := float64(si*600+n*12) + 50
				y := float64(r*20) + 200
				num := rowKey(n)
				row.Seats = append(row.Seats, seating.Seat{
					Key: seating.SeatKey(name, row.Key, num), Number: num, X: x, Y: y, Radius: 5, CategoryIndex: 1,
				})
			}
			sec.Rows = append(sec.Rows, row)
		}
		g.Sections = append(g.Sections, sec)
	}
	return g
}

func rowKey(i int) string {
	const digits = "0123456789"
	return string([]byte{digits[i/100%10], digits[i/10%10], digits[i%10]})
}

// < 100 ms on 3 000 seats (spec 26 §4.1). Best of three runs, so a busy CI
// runner does not turn a scheduling hiccup into a failure.
func TestDesirability_ThreeThousandSeatsUnder100ms(t *testing.T) {
	g := syntheticHall(25, 40) // 3 sections x 25 rows x 40 = 3 000
	if g.SeatCount() != 3000 {
		t.Fatalf("hall has %d seats", g.SeatCount())
	}
	v := values(t, `{"categories":15,"price_max":300000,"price_min":30000,"cheaper_row_edges":true}`)
	a := Annotations{FocalPoint: &seating.Point{X: 1000, Y: 50}}
	best := time.Hour
	for i := 0; i < 3; i++ {
		start := time.Now()
		if _, _, err := Run(Desirability{}, g, a, SessionState{Currency: "EUR"}, v); err != nil {
			t.Fatal(err)
		}
		best = min(best, time.Since(start))
	}
	if best >= 100*time.Millisecond {
		t.Fatalf("3 000 seats took %v, want < 100ms", best)
	}
}

func BenchmarkDesirability3000(b *testing.B) {
	g := syntheticHall(25, 40)
	v := values(b, `{"categories":6,"price_max":300000,"price_min":30000,"cheaper_row_edges":true}`)
	a := Annotations{FocalPoint: &seating.Point{X: 1000, Y: 50}}
	for i := 0; i < b.N; i++ {
		if _, _, err := Run(Desirability{}, g, a, SessionState{Currency: "EUR"}, v); err != nil {
			b.Fatal(err)
		}
	}
}

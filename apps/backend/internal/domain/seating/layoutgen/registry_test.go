package layoutgen

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// flat is the trivial second algorithm of the acceptance criteria: one file,
// one registration, every free seat in a single category at price_max.
type flat struct{ id string }

func (f flat) ID() string { return f.id }

func (flat) Params() ParamSchema {
	return ParamSchema{
		LabelKey:            "layoutgen.algorithm.test_flat",
		RequiredAnnotations: []string{},
		Params: []Param{{
			Name: ParamPriceMax, Type: TypeInteger, Required: true,
			Min: i64(0), Max: i64(maxPriceMinor), LabelKey: "layoutgen.param.price_max",
		}},
	}
}

func (flat) Suggest(g seating.Geometry, _ Annotations, st SessionState, p Values) (Layout, Metrics, error) {
	price, _ := p.Int(ParamPriceMax)
	l := Layout{Slots: []Slot{{Key: "all", PriceAmount: price, Share: 1}}, Assignments: map[string]string{}}
	for _, s := range Seats(g) {
		if _, frozen := st.SoldHeld[s.Key]; !frozen {
			l.Assignments[s.Key] = "all"
		}
	}
	return l, Evaluate(g, l, st), nil
}

func TestRegistry_SecondAlgorithmRunsThroughTheSamePath(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Desirability{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(flat{id: "test_flat"}); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, a := range r.List() {
		ids = append(ids, a.ID())
	}
	if !reflect.DeepEqual(ids, []string{"desirability", "test_flat"}) {
		t.Fatalf("list = %v", ids)
	}
	alg, ok := r.Lookup("test_flat")
	if !ok {
		t.Fatal("lookup failed")
	}
	v, err := alg.Params().Validate(map[string]json.RawMessage{"price_max": json.RawMessage(`500`)})
	if err != nil {
		t.Fatal(err)
	}
	g := loadAkropolis(t)
	l, m, err := Run(alg, g, Annotations{}, SessionState{Currency: "CZK"}, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Assignments) != g.SeatCount() || m.RevenueEstimateMinor != int64(g.SeatCount())*500 {
		t.Fatalf("assigned %d, revenue %d", len(l.Assignments), m.RevenueEstimateMinor)
	}
}

func TestRegistry_RefusesBadAlgorithms(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(flat{id: "Bad-ID"}); err == nil {
		t.Fatal("invalid id accepted")
	}
	if err := r.Register(flat{id: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(flat{id: "ok"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.Register(badDefault{}); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("bad default accepted: %v", err)
	}
	if _, ok := Lookup(DesirabilityID); !ok {
		t.Fatal("desirability is not in the process-wide registry")
	}
}

type badDefault struct{ flat }

func (badDefault) ID() string { return "bad_default" }
func (badDefault) Params() ParamSchema {
	return ParamSchema{LabelKey: "x", Params: []Param{{
		Name: "n", Type: TypeInteger, Default: 4, LabelKey: "x", // int, not int64
	}}}
}

// brokenAlg assigns a frozen seat; Run must refuse its output.
type brokenAlg struct{ flat }

func (brokenAlg) ID() string { return "broken" }
func (brokenAlg) Suggest(g seating.Geometry, _ Annotations, _ SessionState, _ Values) (Layout, Metrics, error) {
	l := Layout{Slots: []Slot{{Key: "all"}}, Assignments: map[string]string{}}
	for _, s := range Seats(g) {
		l.Assignments[s.Key] = "all"
	}
	return l, Metrics{}, nil
}

func TestRun_RefusesALayoutThatTouchesAFrozenSeat(t *testing.T) {
	g := loadAkropolis(t)
	key := Seats(g)[0].Key
	_, _, err := Run(brokenAlg{}, g, Annotations{}, SessionState{
		SoldHeld: map[string]FrozenSeat{key: {Status: StatusSold, PriceMinor: 1}},
	}, Values{ParamPriceMax: int64(1)})
	var ce *ContractError
	if !errors.As(err, &ce) || !strings.Contains(ce.Reason, "frozen") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := Run(Desirability{}, seating.Geometry{}, Annotations{FocalPoint: akropolisStage}, SessionState{}, Values{}); !errors.Is(err, ErrNoSeats) {
		t.Fatalf("empty plan: %v", err)
	}
}

func TestParamSchema_Validate(t *testing.T) {
	s := Desirability{}.Params()
	cases := []struct {
		raw   string
		param string
	}{
		{`{"bogus":1}`, "bogus"},
		{`{"categories":0}`, ParamCategories},
		{`{"categories":16}`, ParamCategories},
		{`{"categories":2.5}`, ParamCategories},
		{`{"categories":"4"}`, ParamCategories},
		{`{"share_template":"triangle"}`, ParamShareTemplate},
		{`{"cheaper_row_edges":"yes"}`, ParamCheaperRowEdges},
		{`{"shares":[101]}`, ParamShares},
		{`{"prices":[]}`, ParamPrices},
		{`{"price_max":-1}`, ParamPriceMax},
	}
	for _, c := range cases {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.raw), &raw); err != nil {
			t.Fatal(err)
		}
		_, err := s.Validate(raw)
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Param != c.param {
			t.Fatalf("%s: err = %v, want a ParamError on %s", c.raw, err, c.param)
		}
	}

	v, err := s.Validate(map[string]json.RawMessage{"categories": json.RawMessage(`null`)})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := v.Int(ParamCategories); n != 4 {
		t.Fatalf("null did not fall back to the default: %v", v)
	}
	if tmpl, _ := v.String(ParamShareTemplate); tmpl != SharePyramid || !v.Bool(ParamRoundPrices) || v.Bool(ParamCheaperRowEdges) {
		t.Fatalf("defaults = %v", v)
	}
}

func TestPlanSlots_CrossFieldRules(t *testing.T) {
	cases := []struct {
		raw   string
		param string
	}{
		{`{"categories":3}`, ParamPriceMax},
		{`{"categories":3,"price_max":100}`, ParamPriceMin},
		{`{"categories":3,"price_max":100,"price_min":200}`, ParamPriceMin},
		{`{"categories":3,"prices":[3,2]}`, ParamPrices},
		{`{"categories":3,"prices":[300,200,250]}`, ParamPrices}, // must not rise
		{`{"categories":2,"prices":[3,2],"shares":[50,40]}`, ParamShares},
		{`{"categories":2,"prices":[3,2],"shares":[100]}`, ParamShares},
	}
	for _, c := range cases {
		v := values(t, c.raw)
		_, err := PlanSlots(v, "EUR")
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Param != c.param {
			t.Fatalf("%s: err = %v, want a ParamError on %s", c.raw, err, c.param)
		}
	}
}

func TestPlanSlots_EqualExplicitPricesAreAllowed(t *testing.T) {
	sp, err := PlanSlots(values(t, `{"categories":3,"prices":[500,500,300]}`), "EUR")
	if err != nil || !reflect.DeepEqual(sp.Prices, []int64{500, 500, 300}) {
		t.Fatalf("plan = %+v, err = %v", sp, err)
	}
}

func TestRun_RefusesAnnotationsThePlanDoesNotHave(t *testing.T) {
	g := loadAkropolis(t)
	v := values(t, `{"price_max":1000,"price_min":500}`)
	cases := []struct {
		a     Annotations
		field string
	}{
		{Annotations{FocalPoint: akropolisStage, SectionWeights: map[string]float64{"parter": 1, "stalls": 0.5}}, "section_weights"},
		{Annotations{FocalPoint: akropolisStage, ObstructedSeatKeys: []string{"nope|1|1"}}, "obstructed_seat_keys"},
	}
	for _, c := range cases {
		_, _, err := Run(Desirability{}, g, c.a, SessionState{Currency: "CZK"}, v)
		var ae *AnnotationError
		if !errors.As(err, &ae) || ae.Annotation != c.field {
			t.Fatalf("%s: err = %v", c.field, err)
		}
	}
	key := Seats(g)[0].Key
	if _, _, err := Run(Desirability{}, g, Annotations{
		FocalPoint: akropolisStage, SectionWeights: map[string]float64{"parter": 0.5}, ObstructedSeatKeys: []string{key},
	}, SessionState{Currency: "CZK"}, v); err != nil {
		t.Fatalf("known keys refused: %v", err)
	}
}

func TestShareTemplate(t *testing.T) {
	want := map[string][]float64{
		SharePyramid:    {0.1, 0.2, 0.3, 0.4},
		ShareEqual:      {0.25, 0.25, 0.25, 0.25},
		ShareWideMiddle: {1.0 / 6, 2.0 / 6, 2.0 / 6, 1.0 / 6},
	}
	for tmpl, w := range want {
		got := ShareTemplate(tmpl, 4)
		for i := range w {
			if math.Abs(got[i]-w[i]) > 1e-9 {
				t.Fatalf("%s = %v, want %v", tmpl, got, w)
			}
		}
	}
	if got := ShareTemplate(SharePyramid, 1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("one category = %v", got)
	}
}

func TestPriceLadder(t *testing.T) {
	// The Symphonians example of spec 26 §4.1: 1 750 / ~1 550 / 1 375 CZK.
	got := PriceLadder(175000, 137500, 3, LadderGeometric, true, "CZK")
	if !reflect.DeepEqual(got, []int64{175000, 155000, 137500}) {
		t.Fatalf("geometric = %v", got)
	}
	got = PriceLadder(100000, 40000, 4, LadderLinear, false, "EUR")
	if !reflect.DeepEqual(got, []int64{100000, 80000, 60000, 40000}) {
		t.Fatalf("linear = %v", got)
	}
	got = PriceLadder(5000, 0, 3, LadderGeometric, false, "EUR")
	if !reflect.DeepEqual(got, []int64{5000, 2500, 0}) {
		t.Fatalf("geometric with zero falls back to linear: %v", got)
	}
	if got := PriceLadder(7000, 1000, 1, LadderLinear, true, "EUR"); !reflect.DeepEqual(got, []int64{7000}) {
		t.Fatalf("one category = %v", got)
	}
}

func TestRoundNice(t *testing.T) {
	cases := []struct {
		in   int64
		exp  int
		want int64
	}{
		{155123, 2, 155000},   // 1 551.23 -> 1 550 (step 25)
		{156300, 2, 157500},   // 1 563 -> 1 575
		{1234500, 2, 1225000}, // 12 345 -> 12 250 (step 250)
		{44730, 2, 44500},     // 447.30 -> 445 (step 5)
		{4370, 2, 4400},       // 43.70 -> 44
		{730, 2, 750},         // 7.30 -> 7.50
		{1551, 0, 1550},       // JPY-like, no minor units
		{0, 2, 0},
	}
	for _, c := range cases {
		if got := RoundNice(c.in, c.exp); got != c.want {
			t.Fatalf("RoundNice(%d, %d) = %d, want %d", c.in, c.exp, got, c.want)
		}
	}
	if MinorExponent("jpy") != 0 || MinorExponent("CZK") != 2 {
		t.Fatal("minor exponents")
	}
}

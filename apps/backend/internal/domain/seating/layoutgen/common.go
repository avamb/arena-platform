// common.go holds what every algorithm shares: the common parameters
// (number of categories, prices, shares), the share templates, the price
// ladder, the revenue estimate (Evaluate) and Run, the one entry point that
// checks an algorithm's output contract.
package layoutgen

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// Common parameter names.
const (
	ParamCategories    = "categories"
	ParamPriceMax      = "price_max"
	ParamPriceMin      = "price_min"
	ParamPrices        = "prices"
	ParamPriceLadder   = "price_ladder"
	ParamRoundPrices   = "round_prices"
	ParamShareTemplate = "share_template"
	ParamShares        = "shares"
)

// Share templates (spec 26 §4.1 step 2).
const (
	SharePyramid    = "pyramid"     // few dear seats, many cheap: 10/20/30/40
	ShareEqual      = "equal"       // the same share for every category
	ShareWideMiddle = "wide_middle" // middle categories larger than the ends
)

// Price ladders (spec 26 §4.1 step 4).
const (
	LadderGeometric = "geometric"
	LadderLinear    = "linear"
)

// maxPriceMinor bounds a price parameter: one million in major units.
const maxPriceMinor = 100_000_000

// CommonParams returns the parameters every category-cutting algorithm
// takes, so the clients draw the same controls for each. An algorithm may
// use a subset or append its own options.
func CommonParams() []Param {
	return []Param{
		{
			Name: ParamCategories, Type: TypeInteger, Required: true,
			Min: i64(1), Max: i64(MaxSlots), Default: int64(4), Unit: UnitCount,
			LabelKey: "layoutgen.param.categories",
		},
		{
			Name: ParamPriceMax, Type: TypeInteger,
			Min: i64(0), Max: i64(maxPriceMinor), Unit: UnitMinorCurrency,
			LabelKey: "layoutgen.param.price_max", HintKey: "layoutgen.param.price_max.hint",
		},
		{
			Name: ParamPriceMin, Type: TypeInteger,
			Min: i64(0), Max: i64(maxPriceMinor), Unit: UnitMinorCurrency,
			LabelKey: "layoutgen.param.price_min", HintKey: "layoutgen.param.price_min.hint",
		},
		{
			Name: ParamPrices, Type: TypeIntegerList,
			Min: i64(0), Max: i64(maxPriceMinor), MinItems: 1, MaxItems: MaxSlots,
			Unit:     UnitMinorCurrency,
			LabelKey: "layoutgen.param.prices", HintKey: "layoutgen.param.prices.hint",
		},
		{
			Name: ParamPriceLadder, Type: TypeEnum, Default: LadderGeometric,
			Options: []Option{
				{Value: LadderGeometric, LabelKey: "layoutgen.option.price_ladder.geometric"},
				{Value: LadderLinear, LabelKey: "layoutgen.option.price_ladder.linear"},
			},
			LabelKey: "layoutgen.param.price_ladder",
		},
		{
			Name: ParamRoundPrices, Type: TypeBoolean, Default: true,
			LabelKey: "layoutgen.param.round_prices", HintKey: "layoutgen.param.round_prices.hint",
		},
		{
			Name: ParamShareTemplate, Type: TypeEnum, Default: SharePyramid,
			Options: []Option{
				{Value: SharePyramid, LabelKey: "layoutgen.option.share_template.pyramid"},
				{Value: ShareEqual, LabelKey: "layoutgen.option.share_template.equal"},
				{Value: ShareWideMiddle, LabelKey: "layoutgen.option.share_template.wide_middle"},
			},
			LabelKey: "layoutgen.param.share_template",
		},
		{
			Name: ParamShares, Type: TypeIntegerList,
			Min: i64(0), Max: i64(100), MinItems: 1, MaxItems: MaxSlots,
			Unit:     UnitPercent,
			LabelKey: "layoutgen.param.shares", HintKey: "layoutgen.param.shares.hint",
		},
	}
}

// SlotPlan is what the common parameters decide: one price and one target
// share per category, the dearest first.
type SlotPlan struct {
	Prices []int64
	Shares []float64
}

// PlanSlots turns the common parameters into prices and shares. It checks
// the rules a schema cannot express: an explicit list must have one entry
// per category, shares must add up to 100, and without an explicit price
// list both price_max and price_min are needed with min <= max.
func PlanSlots(p Values, currency string) (SlotPlan, error) {
	n64, ok := p.Int(ParamCategories)
	if !ok || n64 < 1 || n64 > MaxSlots {
		return SlotPlan{}, &ParamError{Param: ParamCategories, Reason: fmt.Sprintf("must be 1 to %d", MaxSlots)}
	}
	n := int(n64)

	var plan SlotPlan
	if prices, ok := p.Ints(ParamPrices); ok {
		if len(prices) != n {
			return SlotPlan{}, &ParamError{Param: ParamPrices, Reason: fmt.Sprintf("must have one price per category (%d)", n)}
		}
		// Slot 1 is the most wanted part of the hall, so the list goes from
		// the dearest category down: a price may repeat but never rise.
		for i := 1; i < n; i++ {
			if prices[i] > prices[i-1] {
				return SlotPlan{}, &ParamError{Param: ParamPrices, Reason: "must not increase: list the dearest category first"}
			}
		}
		plan.Prices = append([]int64(nil), prices...)
	} else {
		hi, okHi := p.Int(ParamPriceMax)
		lo, okLo := p.Int(ParamPriceMin)
		if !okHi {
			return SlotPlan{}, &ParamError{Param: ParamPriceMax, Reason: "is required when prices is not given"}
		}
		if !okLo {
			return SlotPlan{}, &ParamError{Param: ParamPriceMin, Reason: "is required when prices is not given"}
		}
		if lo > hi {
			return SlotPlan{}, &ParamError{Param: ParamPriceMin, Reason: "must not exceed price_max"}
		}
		ladder, _ := p.String(ParamPriceLadder)
		plan.Prices = PriceLadder(hi, lo, n, ladder, p.Bool(ParamRoundPrices), currency)
	}

	if shares, ok := p.Ints(ParamShares); ok {
		if len(shares) != n {
			return SlotPlan{}, &ParamError{Param: ParamShares, Reason: fmt.Sprintf("must have one share per category (%d)", n)}
		}
		var sum int64
		for _, s := range shares {
			sum += s
		}
		if sum != 100 {
			return SlotPlan{}, &ParamError{Param: ParamShares, Reason: "must add up to 100"}
		}
		plan.Shares = make([]float64, n)
		for i, s := range shares {
			plan.Shares[i] = float64(s) / 100
		}
	} else {
		tmpl, _ := p.String(ParamShareTemplate)
		plan.Shares = ShareTemplate(tmpl, n)
	}
	return plan, nil
}

// ShareTemplate returns n target shares adding up to 1, the dearest
// category first. pyramid weighs category i (1-based) by i, so four
// categories give 10/20/30/40; wide_middle weighs it by 1 + its distance
// from the nearer end (1/2/2/1 for four); equal gives 1/n each. An unknown
// template is treated as equal.
func ShareTemplate(template string, n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		switch template {
		case SharePyramid:
			w[i] = float64(i + 1)
		case ShareWideMiddle:
			w[i] = float64(1 + min(i, n-1-i))
		default:
			w[i] = 1
		}
	}
	var sum float64
	for _, x := range w {
		sum += x
	}
	for i := range w {
		w[i] /= sum
	}
	return w
}

// PriceLadder returns n prices from hi down to lo. The two ends are kept as
// given; the prices between them are rounded to "nice" values of the
// currency when round is true (1 750 / 1 550 / 1 375). A geometric ladder
// with lo = 0 falls back to linear.
func PriceLadder(hi, lo int64, n int, ladder string, round bool, currency string) []int64 {
	if n == 1 {
		return []int64{hi}
	}
	out := make([]int64, n)
	geometric := ladder == LadderGeometric && lo > 0
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n-1)
		var v float64
		if geometric {
			v = float64(hi) * math.Pow(float64(lo)/float64(hi), f)
		} else {
			v = float64(hi) - float64(hi-lo)*f
		}
		out[i] = int64(math.Round(v))
	}
	out[0], out[n-1] = hi, lo
	if round {
		exp := MinorExponent(currency)
		for i := 1; i < n-1; i++ {
			r := RoundNice(out[i], exp)
			// Rounding must not cross an end of the range.
			out[i] = max(lo, min(hi, r))
		}
	}
	return out
}

// MinorExponent is the number of minor-unit digits of a currency: 2 for
// almost every currency arena sells in, 0 for the few without minor units.
func MinorExponent(currency string) int {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "JPY", "KRW", "ISK", "CLP", "VND", "PYG", "UGX", "XAF", "XOF":
		return 0
	}
	return 2
}

// RoundNice rounds a price (minor units) to a step a box office would
// print: 25 in the thousands of major units (1 375), 250 in the tens of
// thousands, 5 in the hundreds, 1 in the tens and a half below ten.
func RoundNice(v int64, exp int) int64 {
	if v <= 0 {
		return v
	}
	unit := int64(1)
	for i := 0; i < exp; i++ {
		unit *= 10
	}
	major := float64(v) / float64(unit)
	var step int64 // in minor units
	switch {
	case major >= 1000:
		digits := int(math.Floor(math.Log10(major))) + 1
		step = 25 * unit
		for i := 4; i < digits; i++ {
			step *= 10
		}
	case major >= 100:
		step = 5 * unit
	case major >= 10:
		step = unit
	default:
		step = unit / 2
		if step == 0 {
			step = 1
		}
	}
	return (v + step/2) / step * step
}

// palette colours the proposed categories, the dearest first, so a preview
// is readable before the operator picks colours of their own.
var palette = [MaxSlots]string{
	"#c0392b", "#e67e22", "#f1c40f", "#27ae60", "#16a085",
	"#2980b9", "#8e44ad", "#d35400", "#7f8c8d", "#2c3e50",
	"#e84393", "#00b894", "#6c5ce7", "#fdcb6e", "#636e72",
}

// SlotKey is the key of the i-th category (0-based), "c1" for the dearest.
func SlotKey(i int) string { return fmt.Sprintf("c%d", i+1) }

// Slots builds the slot list of a plan with the standard keys and colours.
func (sp SlotPlan) Slots() []Slot {
	out := make([]Slot, len(sp.Prices))
	for i := range sp.Prices {
		out[i] = Slot{Key: SlotKey(i), Color: palette[i%len(palette)], PriceAmount: sp.Prices[i], Share: sp.Shares[i]}
	}
	return out
}

// ─── seats ──────────────────────────────────────────────────────────────────

// PlacedSeat is a seat of the geometry with its place in the plan.
type PlacedSeat struct {
	seating.Seat
	Section int // index into Geometry.Sections
	Row     int // index into Section.Rows
}

// Seats flattens the geometry's seats in canonical order.
func Seats(g seating.Geometry) []PlacedSeat {
	var out []PlacedSeat
	for si, sec := range g.Sections {
		for ri, row := range sec.Rows {
			for _, s := range row.Seats {
				out = append(out, PlacedSeat{Seat: s, Section: si, Row: ri})
			}
		}
	}
	return out
}

// ─── evaluation ─────────────────────────────────────────────────────────────

// Evaluate computes the revenue estimate of a layout over the geometry's
// seats: frozen seats at their own price, open assigned seats at the price
// of their slot, closed seats nowhere. Every algorithm should end with it so
// the figures mean the same thing whichever algorithm made them.
func Evaluate(g seating.Geometry, l Layout, st SessionState) Metrics {
	m := Metrics{Currency: st.Currency}
	idx := make(map[string]int, len(l.Slots))
	m.BySlot = make([]SlotMetrics, len(l.Slots))
	for i, s := range l.Slots {
		idx[s.Key] = i
		m.BySlot[i] = SlotMetrics{SlotKey: s.Key, PriceAmount: s.PriceAmount}
	}
	for _, s := range Seats(g) {
		m.TotalSeats++
		if f, ok := st.SoldHeld[s.Key]; ok {
			m.SoldFrozen++
			if f.Status == StatusHeld {
				m.HeldSeats++
				m.RevenueHeldMinor += f.PriceMinor
			} else {
				m.SoldSeats++
				m.RevenueSoldMinor += f.PriceMinor
			}
			continue
		}
		closed := st.Closed[s.Key]
		if closed {
			m.ClosedSeats++
		}
		i, ok := idx[l.Assignments[s.Key]]
		if !ok {
			continue
		}
		bs := &m.BySlot[i]
		bs.Seats++
		if !closed {
			bs.OpenSeats++
			bs.RevenueMinor += bs.PriceAmount
		}
	}
	for i := range m.BySlot {
		m.RevenueRemainingMinor += m.BySlot[i].RevenueMinor
		if m.TotalSeats > 0 {
			m.BySlot[i].Share = float64(m.BySlot[i].Seats) / float64(m.TotalSeats)
		}
	}
	m.RevenueEstimateMinor = m.RevenueSoldMinor + m.RevenueHeldMinor + m.RevenueRemainingMinor
	return m
}

// ─── run ────────────────────────────────────────────────────────────────────

// Validate checks the annotation values. Section weights must lie in
// [0, 1] and the focal point must be a finite point.
func (a Annotations) Validate() error {
	if fp := a.FocalPoint; fp != nil {
		if math.IsNaN(fp.X) || math.IsInf(fp.X, 0) || math.IsNaN(fp.Y) || math.IsInf(fp.Y, 0) {
			return &AnnotationError{Annotation: AnnotationFocalPoint, Reason: "must be a finite point"}
		}
	}
	for k, w := range a.SectionWeights {
		if math.IsNaN(w) || w < 0 || w > 1 {
			return &AnnotationError{Annotation: "section_weights", Reason: fmt.Sprintf("weight of %q must be between 0 and 1", k)}
		}
	}
	return nil
}

// matchGeometry refuses a section weight or an obstructed seat that the
// plan does not have: a typo would otherwise be ignored silently and the
// proposal would not be the one the operator asked for. Keys are compared
// in sorted order so the first error does not depend on map order.
func (a Annotations) matchGeometry(g seating.Geometry) error {
	sections := make(map[string]bool, len(g.Sections))
	for _, s := range g.Sections {
		sections[s.Key] = true
	}
	keys := make([]string, 0, len(a.SectionWeights))
	for k := range a.SectionWeights {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !sections[k] {
			return &AnnotationError{Annotation: "section_weights", Reason: fmt.Sprintf("the plan has no section %q", k)}
		}
	}
	if len(a.ObstructedSeatKeys) > 0 {
		seats := make(map[string]bool, g.SeatCount())
		for _, s := range Seats(g) {
			seats[s.Key] = true
		}
		for _, k := range a.ObstructedSeatKeys {
			if !seats[k] {
				return &AnnotationError{Annotation: "obstructed_seat_keys", Reason: fmt.Sprintf("the plan has no seat %q", k)}
			}
		}
	}
	return nil
}

func (a Annotations) has(name string) bool {
	switch name {
	case AnnotationFocalPoint:
		return a.FocalPoint != nil
	}
	return false
}

// Run is the one way to call an algorithm: it canonicalises the geometry,
// checks the annotations the algorithm requires, calls Suggest and then
// checks the output contract — every free seat of the geometry assigned to
// a declared slot, no frozen seat assigned, 1 to MaxSlots slots with unique
// keys. The frozen list and the metrics are recomputed here from the
// geometry, so no algorithm can report revenue differently.
func Run(alg Algorithm, g seating.Geometry, a Annotations, st SessionState, p Values) (Layout, Metrics, error) {
	if err := a.Validate(); err != nil {
		return Layout{}, Metrics{}, err
	}
	g = seating.Canonicalize(g)
	if g.SeatCount() == 0 {
		return Layout{}, Metrics{}, ErrNoSeats
	}
	if err := a.matchGeometry(g); err != nil {
		return Layout{}, Metrics{}, err
	}
	for _, name := range alg.Params().RequiredAnnotations {
		if !a.has(name) {
			return Layout{}, Metrics{}, &AnnotationRequiredError{Annotation: name}
		}
	}
	l, _, err := alg.Suggest(g, a, st, p)
	if err != nil {
		return Layout{}, Metrics{}, err
	}

	fail := func(format string, args ...any) (Layout, Metrics, error) {
		return Layout{}, Metrics{}, &ContractError{Algorithm: alg.ID(), Reason: fmt.Sprintf(format, args...)}
	}
	if len(l.Slots) < 1 || len(l.Slots) > MaxSlots {
		return fail("%d slots, want 1 to %d", len(l.Slots), MaxSlots)
	}
	keys := make(map[string]bool, len(l.Slots))
	for _, s := range l.Slots {
		if s.Key == "" || keys[s.Key] {
			return fail("slot key %q is empty or repeated", s.Key)
		}
		if s.PriceAmount < 0 {
			return fail("slot %q has a negative price", s.Key)
		}
		keys[s.Key] = true
	}
	seats := Seats(g)
	inGeometry := make(map[string]bool, len(seats))
	var frozen []string
	for _, s := range seats {
		inGeometry[s.Key] = true
		slot, assigned := l.Assignments[s.Key]
		if _, isFrozen := st.SoldHeld[s.Key]; isFrozen {
			if assigned {
				return fail("frozen seat %q was assigned", s.Key)
			}
			frozen = append(frozen, s.Key)
			continue
		}
		if !assigned {
			return fail("free seat %q has no slot", s.Key)
		}
		if !keys[slot] {
			return fail("seat %q is assigned to unknown slot %q", s.Key, slot)
		}
	}
	for k := range l.Assignments {
		if !inGeometry[k] {
			return fail("seat %q is not in the plan", k)
		}
	}
	sort.Strings(frozen)
	if frozen == nil {
		frozen = []string{}
	}
	l.FrozenSeatKeys = frozen
	return l, Evaluate(g, l, st), nil
}

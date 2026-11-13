package eventbot

// seat_layout.go — the pure part of the "Seat layout" screens (task S3c of
// spec 40, spec 26 §4.1): which categories a layout is cut into, which
// parameters the person is asked, how a proposal becomes a seat batch and how
// a preview is told apart from the next one. Nothing here calls arena-api.
//
// The allocator is a replaceable module (S1e): the bot reads the list of
// algorithms and the schema of their parameters from the API and never names
// one. What it does know is the COMMON parameters every category-cutting
// algorithm takes (the number of categories and their prices), because the
// bot cuts the hall into the categories the date ALREADY has - their names and
// prices are the organizer's, the allocator only decides which seats go to
// which. With two or more categories a proposal is applied only when it has
// exactly one slot per category, the dearest slot going to the dearest
// category. With ONE category the person says into how many categories to cut
// and the top and bottom price; the slots then become NEW categories, created
// by the same batch (new_tiers), except a slot that costs what the old
// category costs, which goes to it.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The common parameters of the allocator (layoutgen.CommonParams). The bot
// fills these from the date's categories and does not ask about them.
var loOwnedParams = map[string]bool{
	"categories": true, "prices": true, "price_max": true, "price_min": true,
	"price_ladder": true, "round_prices": true, "shares": true,
}

// loMaxAsked is how many options the person is asked about at most.
const loMaxAsked = 2

// loMaxChanges is the most seats one batch carries (seatlayout.MaxChanges).
const loMaxChanges = 10_000

// loMaxBody keeps the batch under the API's 1 MiB body limit.
const loMaxBody = 1<<20 - 4096

// loTarget is one category the hall is cut into.
type loTarget struct {
	ID    uuid.UUID
	Name  string
	Price int64
}

// loTargets picks the categories of the date a layout fills: the open ones
// that own seats from the plan, the dearest first. A category that is closed
// is left out (a seat moved into it could not be sold) and counted; a
// general-admission category has no seats to place.
func loTargets(tiers []openapi.TicketTierItem) (targets []loTarget, closed int) {
	type keyed struct {
		t    loTarget
		sort int32
	}
	var list []keyed
	for _, t := range tiers {
		if t.Kind == nil || *t.Kind != "seated" {
			continue
		}
		if !categoryOpen(t) {
			closed++
			continue
		}
		price := int64(0)
		if string(t.PricingMode) == "fixed" {
			price = categoryPriceNow(t)
		}
		list = append(list, keyed{t: loTarget{ID: t.Id, Name: t.Name, Price: price}, sort: int32(t.SortOrder)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case a.t.Price != b.t.Price:
			return a.t.Price > b.t.Price
		case a.sort != b.sort:
			return a.sort < b.sort
		case a.t.Name != b.t.Name:
			return a.t.Name < b.t.Name
		}
		return a.t.ID.String() < b.t.ID.String()
	})
	for _, k := range list {
		targets = append(targets, k.t)
	}
	return targets, closed
}

// loEmptyOpen picks the EMPTY open categories of the date: no seat of the plan
// and no general-admission unit (the server reports such a category as kind
// "ga", so loTargets drops it), open, with a fixed price. A split may put a
// slot of the same price into one of them instead of creating a new category.
// A real standing category owns GA units and is never returned. The dearest
// first, as loTargets orders them.
func loEmptyOpen(tiers []openapi.TicketTierItem) []loTarget {
	var picked []openapi.TicketTierItem
	for _, t := range tiers {
		if t.SeatCount == nil || *t.SeatCount != 0 || t.GaUnitCount == nil || *t.GaUnitCount != 0 {
			continue
		}
		if !categoryOpen(t) || string(t.PricingMode) != "fixed" || (t.IsAddon != nil && *t.IsAddon) {
			continue
		}
		picked = append(picked, t)
	}
	sort.SliceStable(picked, func(i, j int) bool {
		a, b := picked[i], picked[j]
		pa, pb := categoryPriceNow(a), categoryPriceNow(b)
		switch {
		case pa != pb:
			return pa > pb
		case a.SortOrder != b.SortOrder:
			return a.SortOrder < b.SortOrder
		case a.Name != b.Name:
			return a.Name < b.Name
		}
		return a.Id.String() < b.Id.String()
	})
	out := make([]loTarget, 0, len(picked))
	for _, t := range picked {
		out = append(out, loTarget{ID: t.Id, Name: t.Name, Price: categoryPriceNow(t)})
	}
	return out
}

// loAsked returns the parameters of the algorithm the person is asked about:
// the enumerations and the switches that are not the common price/category
// parameters, in the schema's order, at most loMaxAsked of them.
func loAsked(alg openapi.SeatLayoutAlgorithm) []openapi.SeatLayoutParam {
	var out []openapi.SeatLayoutParam
	for _, p := range alg.Params {
		if loOwnedParams[p.Name] || (p.Type != "enum" && p.Type != "boolean") {
			continue
		}
		out = append(out, p)
		if len(out) == loMaxAsked {
			break
		}
	}
	return out
}

// loChoiceValue is the value of an asked parameter: the person's choice, else
// the schema's default. It is a string for an enum and a bool for a switch;
// nil when neither exists.
func loChoiceValue(p openapi.SeatLayoutParam, choice map[string]any) any {
	if v, ok := choice[p.Name]; ok {
		switch p.Type {
		case "boolean":
			if b, ok := v.(bool); ok {
				return b
			}
		case "enum":
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	switch d := p.Default.(type) {
	case bool:
		if p.Type == "boolean" {
			return d
		}
	case string:
		if p.Type == "enum" {
			return d
		}
	}
	return nil
}

// loRequestParams builds the params of a suggest call: the asked parameters
// with the person's choice, and the common ones from the categories.
func loRequestParams(alg openapi.SeatLayoutAlgorithm, targets []loTarget, choice map[string]any) map[string]any {
	params := map[string]any{}
	have := map[string]bool{}
	for _, p := range alg.Params {
		have[p.Name] = true
	}
	for _, p := range loAsked(alg) {
		if v := loChoiceValue(p, choice); v != nil {
			params[p.Name] = v
		}
	}
	if have["categories"] {
		params["categories"] = len(targets)
	}
	if have["prices"] {
		prices := make([]int64, len(targets))
		for i, t := range targets {
			prices[i] = t.Price
		}
		params["prices"] = prices
	}
	return params
}

// loStageSides are the four sides of the plan the stage can be on.
var loStageSides = []string{"top", "bottom", "left", "right"}

// loStagePoint is the stage as a point outside the seats' box, on the named
// side of the plan as it is drawn (y grows downward). The margin keeps the
// point clear of the nearest row.
func loStagePoint(b PlanBounds, side string) (x, y float64, ok bool) {
	cx, cy := (b.MinX+b.MaxX)/2, (b.MinY+b.MaxY)/2
	margin := max(b.MaxX-b.MinX, b.MaxY-b.MinY) * 0.15
	if margin < 1 {
		margin = 1
	}
	switch side {
	case "top":
		return cx, b.MinY - margin, true
	case "bottom":
		return cx, b.MaxY + margin, true
	case "left":
		return b.MinX - margin, cy, true
	case "right":
		return b.MaxX + margin, cy, true
	}
	return 0, 0, false
}

// loNeedsStage reports whether the algorithm cannot work without the stage.
func loNeedsStage(alg openapi.SeatLayoutAlgorithm) bool {
	for _, a := range alg.RequiredAnnotations {
		if a == "focal_point" {
			return true
		}
	}
	return false
}

// loPlan is a proposal tied to the categories it is applied to. The slices are
// in the proposal's slot order.
type loPlan struct {
	// Tier is the existing category of each slot (uuid.Nil for a new one).
	Tier []uuid.UUID
	// Count is how many seats each slot gets.
	Count []int
	// Name and Price are what the slot is called and costs, in minor units.
	Name  []string
	Price []int64
	// NewKey is the new_tier_key of a slot that becomes a NEW category, else "".
	NewKey []string
	// NewTiers are the categories the batch creates: only slots that get a seat.
	NewTiers []openapi.SeatBatchNewTier
	// Changes are the seat changes of the batch, sorted by seat key.
	Changes []openapi.SeatBatchChangeRequest
}

// IsNew reports whether slot i becomes a new category.
func (p loPlan) IsNew(i int) bool { return p.NewKey[i] != "" }

// loPlanKeeps reports whether the plan puts a seat into the existing category.
func loPlanKeeps(p loPlan, tier uuid.UUID) bool {
	for i, t := range p.Tier {
		if t == tier && !p.IsNew(i) && p.Count[i] > 0 {
			return true
		}
	}
	return false
}

// batchRequest is the seat batch the plan applies.
func (p loPlan) batchRequest() openapi.SeatBatchRequest {
	req := openapi.SeatBatchRequest{Kind: "apply_layout", Changes: p.Changes}
	if len(p.NewTiers) > 0 {
		nt := p.NewTiers
		req.NewTiers = &nt
	}
	return req
}

// loBuildPlan ties the proposal to the categories: slot i (the dearest first)
// goes to category i. It refuses a proposal whose slot count differs from the
// number of categories, because a seat cannot be given to a category that
// does not exist.
func loBuildPlan(res openapi.SeatLayoutSuggestResponse, targets []loTarget) (loPlan, error) {
	slots := res.Layout.Slots
	if len(slots) != len(targets) {
		return loPlan{}, fmt.Errorf("the proposal has %d categories, the date has %d", len(slots), len(targets))
	}
	p := newLoPlan(len(slots))
	for i := range slots {
		p.Tier[i], p.Name[i], p.Price[i] = targets[i].ID, targets[i].Name, targets[i].Price
	}
	return p, p.assign(res)
}

func newLoPlan(n int) loPlan {
	return loPlan{
		Tier: make([]uuid.UUID, n), Count: make([]int, n), Name: make([]string, n),
		Price: make([]int64, n), NewKey: make([]string, n),
	}
}

// assign writes the seat changes of the proposal into the plan, whose slots
// are already tied to categories (existing or new).
func (p *loPlan) assign(res openapi.SeatLayoutSuggestResponse) error {
	index := make(map[string]int, len(res.Layout.Slots))
	for i, s := range res.Layout.Slots {
		index[s.Key] = i
	}
	keys := make([]string, 0, len(res.Layout.Assignments))
	for k := range res.Layout.Assignments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p.Changes = make([]openapi.SeatBatchChangeRequest, 0, len(keys))
	for _, k := range keys {
		slot := res.Layout.Assignments[k]
		i, ok := index[slot]
		if !ok {
			return fmt.Errorf("seat %q is assigned to an unknown category %q", k, slot)
		}
		p.Count[i]++
		ch := openapi.SeatBatchChangeRequest{SeatKey: k}
		if p.IsNew(i) {
			key := p.NewKey[i]
			ch.NewTierKey = &key
		} else {
			tier := p.Tier[i]
			ch.TierId = &tier
		}
		p.Changes = append(p.Changes, ch)
	}
	return nil
}

// loNewCatLimit mirrors the server: live plus new categories may not exceed 15.
const loNewCatLimit = 15

// errLoTooManyTiers: the new categories would not fit under loNewCatLimit.
var errLoTooManyTiers = errors.New("too many categories on the date")

// loBuildPlanNew ties a proposal to the date's existing categories AND new
// ones: a slot whose price equals the price of an existing category goes to it
// (the dearest of them, targets are dearest first), every other slot becomes a
// new category with the slot's price and colour, named by the slot or, when it
// has no name, by catName(n) with the first n that no category of the date
// (taken, every live category's name) or earlier new category uses, ignoring
// case. A new category is sent only when a seat goes into it. A slot that no
// target matches next looks for an EMPTY open category of the same price
// (empties, each used once) before a new one is made.
func loBuildPlanNew(res openapi.SeatLayoutSuggestResponse, targets, empties []loTarget, taken []string, catName func(n int) string) (loPlan, error) {
	slots := res.Layout.Slots
	if len(slots) == 0 {
		return loPlan{}, fmt.Errorf("the proposal has no categories")
	}
	used := make(map[string]bool, len(taken)+len(slots))
	for _, n := range taken {
		used[strings.ToLower(strings.TrimSpace(n))] = true
	}
	free := func(name string) bool { return name != "" && !used[strings.ToLower(name)] }
	p := newLoPlan(len(slots))
	// Count the seats first: a new category nobody sits in is never created.
	probe := newLoPlan(len(slots))
	for i := range slots {
		probe.NewKey[i] = "x"
	}
	if err := probe.assign(res); err != nil {
		return loPlan{}, err
	}
	next := 1
	emptyUsed := make([]bool, len(empties))
	for i, s := range slots {
		p.Price[i] = s.PriceAmount
		var match *loTarget
		for j := range targets {
			if targets[j].Price == s.PriceAmount {
				match = &targets[j]
				break
			}
		}
		if match != nil {
			p.Tier[i], p.Name[i] = match.ID, match.Name
			continue
		}
		for j := range empties {
			if !emptyUsed[j] && empties[j].Price == s.PriceAmount {
				emptyUsed[j] = true
				match = &empties[j]
				break
			}
		}
		if match != nil {
			p.Tier[i], p.Name[i] = match.ID, match.Name
			continue
		}
		name := ""
		if s.Name != nil {
			name = strings.TrimSpace(*s.Name)
		}
		if !free(name) {
			for name = catName(next); !free(name); name = catName(next) {
				next++
			}
			next++
		}
		used[strings.ToLower(name)] = true
		p.Name[i], p.NewKey[i] = name, fmt.Sprintf("n%d", i+1)
		if probe.Count[i] == 0 {
			continue
		}
		nt := openapi.SeatBatchNewTier{Key: p.NewKey[i], Name: name, PriceAmount: s.PriceAmount}
		if s.Color != nil && *s.Color != "" {
			c := *s.Color
			nt.Color = &c
		}
		p.NewTiers = append(p.NewTiers, nt)
	}
	if len(taken)+len(p.NewTiers) > loNewCatLimit {
		return loPlan{}, errLoTooManyTiers
	}
	return p, p.assign(res)
}

// loNew is what a person is asked when the date has ONE category: into how
// many categories the seats are cut, the top and the bottom price (minor).
type loNew struct {
	Cats        int
	Top, Bottom int64
}

// Bounds of the questions.
const (
	loMinCats = 2
	loMaxCats = 6
	// loMaxPriceMinor is layoutgen's bound of a price parameter (one million in
	// major units).
	loMaxPriceMinor = 100_000_000
)

// loParsePrice reads a typed price in major units of the date's currency with
// the bot's money parser, refusing zero, a negative or an absurd one.
func loParsePrice(raw string) (int64, bool) {
	digits := 0
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	if digits == 0 || digits > 12 { // more digits cannot be a price and could overflow
		return 0, false
	}
	v, ok := ParsePriceMinor(raw)
	if !ok || v <= 0 || v > loMaxPriceMinor {
		return 0, false
	}
	return v, true
}

// loSupportsNew reports whether the algorithm takes the three parameters a
// split of ONE category needs (categories and the price range).
func loSupportsNew(alg openapi.SeatLayoutAlgorithm) bool {
	have := map[string]bool{}
	for _, p := range alg.Params {
		have[p.Name] = true
	}
	return have["categories"] && have["price_max"] && have["price_min"]
}

// loRequestParamsNew builds the params of a suggest call for a date with one
// category: the asked parameters, the number of categories and the price range.
func loRequestParamsNew(alg openapi.SeatLayoutAlgorithm, nw loNew, choice map[string]any) map[string]any {
	params := loRequestParams(alg, nil, choice)
	delete(params, "categories")
	delete(params, "prices")
	params["categories"] = nw.Cats
	params["price_max"] = nw.Top
	params["price_min"] = nw.Bottom
	return params
}

// loFingerprint identifies what a preview showed: the categories, the seat
// each one gets and the seats left as they are. Two proposals with the same
// fingerprint apply the same batch. The seat status version is NOT part of
// it - a seat held elsewhere that the layout does not touch must not make a
// good preview stale.
func loFingerprint(alg string, plan loPlan, frozen []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", alg)
	for i, t := range plan.Tier {
		fmt.Fprintf(h, "slot %d %s %d %q %d %q\n", i, t, plan.Count[i], plan.Name[i], plan.Price[i], plan.NewKey[i])
	}
	for _, nt := range plan.NewTiers {
		color := ""
		if nt.Color != nil {
			color = *nt.Color
		}
		fmt.Fprintf(h, "new %s %q %d %s\n", nt.Key, nt.Name, nt.PriceAmount, color)
	}
	for _, c := range plan.Changes {
		if c.NewTierKey != nil {
			fmt.Fprintf(h, "%s=new:%s\n", c.SeatKey, *c.NewTierKey)
			continue
		}
		fmt.Fprintf(h, "%s=%s\n", c.SeatKey, c.TierId)
	}
	fz := append([]string(nil), frozen...)
	sort.Strings(fz)
	fmt.Fprintf(h, "frozen %s\n", strings.Join(fz, "\x00"))
	return hex.EncodeToString(h.Sum(nil))
}

// loFingerprintShort is the part of a fingerprint a button carries.
func loFingerprintShort(fp string) string {
	if len(fp) > 10 {
		return fp[:10]
	}
	return fp
}

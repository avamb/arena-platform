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
// which. A batch cannot create a category yet (new_tiers is refused), so a
// proposal is applied only when it has exactly one slot per category, the
// dearest slot going to the dearest category.

import (
	"crypto/sha256"
	"encoding/hex"
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

// loPlan is a proposal tied to the categories it is applied to.
type loPlan struct {
	// Tier is the category of each slot, in the proposal's slot order.
	Tier []uuid.UUID
	// Count is how many seats each slot gets.
	Count []int
	// Changes are the seat changes of the batch, sorted by seat key.
	Changes []openapi.SeatBatchChangeRequest
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
	tierOf := make(map[string]uuid.UUID, len(slots))
	index := make(map[string]int, len(slots))
	p := loPlan{Tier: make([]uuid.UUID, len(slots)), Count: make([]int, len(slots))}
	for i, s := range slots {
		tierOf[s.Key] = targets[i].ID
		index[s.Key] = i
		p.Tier[i] = targets[i].ID
	}
	keys := make([]string, 0, len(res.Layout.Assignments))
	for k := range res.Layout.Assignments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p.Changes = make([]openapi.SeatBatchChangeRequest, 0, len(keys))
	for _, k := range keys {
		slot := res.Layout.Assignments[k]
		tier, ok := tierOf[slot]
		if !ok {
			return loPlan{}, fmt.Errorf("seat %q is assigned to an unknown category %q", k, slot)
		}
		p.Count[index[slot]]++
		p.Changes = append(p.Changes, openapi.SeatBatchChangeRequest{SeatKey: k, TierId: &tier})
	}
	return p, nil
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
		fmt.Fprintf(h, "slot %d %s %d\n", i, t, plan.Count[i])
	}
	for _, c := range plan.Changes {
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

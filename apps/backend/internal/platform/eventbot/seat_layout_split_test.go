package eventbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func slotWith(key string, price int64, color string) openapi.SeatLayoutSlot {
	s := openapi.SeatLayoutSlot{Key: key, PriceAmount: price}
	if color != "" {
		s.Color = &color
	}
	return s
}

func splitSuggest(slots []openapi.SeatLayoutSlot, assign map[string]string) openapi.SeatLayoutSuggestResponse {
	res := openapi.SeatLayoutSuggestResponse{Algorithm: "desirability"}
	res.Layout.Slots = slots
	res.Layout.Assignments = assign
	return res
}

func catName(n int) string { return "Category " + string(rune('0'+n)) }

func TestLoBuildPlanNew_EqualPriceGoesToTheOldCategoryAndTheRestAreNew(t *testing.T) {
	t.Parallel()
	old := loTarget{ID: uuid.New(), Name: "Stalls", Price: 9000}
	res := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 9000, "#c0392b"), slotWith("c2", 6000, "#e67e22"), slotWith("c3", 3000, "#f1c40f")},
		map[string]string{"s|1": "c1", "s|2": "c2", "s|3": "c3", "s|4": "c3"})
	plan, err := loBuildPlanNew(res, []loTarget{old}, []string{"Stalls"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNew(0) || plan.Tier[0] != old.ID || plan.Name[0] != "Stalls" {
		t.Errorf("the slot at the old price must go to the old category: %+v", plan)
	}
	if !plan.IsNew(1) || !plan.IsNew(2) || plan.Name[1] != "Category 1" || plan.Name[2] != "Category 2" {
		t.Errorf("other slots become new categories: %+v", plan.Name)
	}
	if len(plan.NewTiers) != 2 || plan.NewTiers[0].PriceAmount != 6000 || *plan.NewTiers[0].Color != "#e67e22" ||
		plan.NewTiers[1].Key != "n3" {
		t.Errorf("new tiers = %+v", plan.NewTiers)
	}
	var seen []string
	for _, c := range plan.Changes {
		switch {
		case c.NewTierKey != nil && c.TierId == nil:
			seen = append(seen, c.SeatKey+"="+*c.NewTierKey)
		case c.TierId != nil && c.NewTierKey == nil && *c.TierId == old.ID:
			seen = append(seen, c.SeatKey+"=old")
		default:
			t.Errorf("a change names both or none: %+v", c)
		}
	}
	if got := strings.Join(seen, ","); got != "s|1=old,s|2=n2,s|3=n3,s|4=n3" {
		t.Errorf("changes = %s", got)
	}
	// The request carries the new tiers as the API wants them.
	raw, _ := json.Marshal(plan.batchRequest())
	if !strings.Contains(string(raw), `"new_tiers":[{`) || !strings.Contains(string(raw), `"new_tier_key":"n2"`) {
		t.Errorf("request = %s", raw)
	}
}

func TestLoBuildPlanNew_NamesAvoidTakenOnesAndEmptySlotsAreNotCreated(t *testing.T) {
	t.Parallel()
	old := loTarget{ID: uuid.New(), Name: "Category 1", Price: 5000}
	named := slotWith("c2", 3000, "")
	nm := " Gold "
	named.Name = &nm
	res := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 9000, ""), named, slotWith("c3", 1000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2"}) // c3 gets no seat
	plan, err := loBuildPlanNew(res, []loTarget{old}, []string{"Category 1", "gold"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	// "Gold" is taken (ignoring case), "Category 1" too: the first free number is 2.
	if plan.Name[0] != "Category 2" || plan.Name[1] != "Category 3" {
		t.Errorf("names = %v", plan.Name)
	}
	if len(plan.NewTiers) != 2 {
		t.Errorf("a slot with no seat must not create a category: %+v", plan.NewTiers)
	}
	for _, nt := range plan.NewTiers {
		if nt.Key == "n3" {
			t.Errorf("the empty slot was created: %+v", nt)
		}
	}
	// A free slot name is kept, trimmed.
	res2 := splitSuggest([]openapi.SeatLayoutSlot{named}, map[string]string{"s|1": "c2"})
	p2, err := loBuildPlanNew(res2, []loTarget{old}, []string{"Category 1"}, catName)
	if err != nil || p2.Name[0] != "Gold" {
		t.Errorf("name = %v err %v", p2.Name, err)
	}
}

func TestLoBuildPlanNew_RefusesMoreThanFifteenCategories(t *testing.T) {
	t.Parallel()
	taken := make([]string, 14)
	for i := range taken {
		taken[i] = "T" + string(rune('a'+i))
	}
	res := splitSuggest([]openapi.SeatLayoutSlot{slotWith("c1", 9, ""), slotWith("c2", 8, "")},
		map[string]string{"s|1": "c1", "s|2": "c2"})
	if _, err := loBuildPlanNew(res, nil, taken, catName); err != errLoTooManyTiers {
		t.Errorf("err = %v, want errLoTooManyTiers", err)
	}
	if _, err := loBuildPlanNew(res, nil, taken[:13], catName); err != nil {
		t.Errorf("15 categories are allowed: %v", err)
	}
}

func TestLoFingerprint_CoversNewCategoriesNamesAndPrices(t *testing.T) {
	t.Parallel()
	old := loTarget{ID: uuid.New(), Name: "Stalls", Price: 9000}
	build := func(price int64, name string) string {
		nm := name
		s2 := slotWith("c2", price, "#112233")
		s2.Name = &nm
		res := splitSuggest([]openapi.SeatLayoutSlot{slotWith("c1", 9000, ""), s2}, map[string]string{"s|1": "c1", "s|2": "c2"})
		p, err := loBuildPlanNew(res, []loTarget{old}, []string{"Stalls"}, catName)
		if err != nil {
			t.Fatal(err)
		}
		return loFingerprint("desirability", p, nil)
	}
	base := build(5000, "Gold")
	if build(5000, "Gold") != base {
		t.Error("the same proposal must have the same fingerprint")
	}
	if build(4000, "Gold") == base {
		t.Error("another price of a new category must make the preview stale")
	}
	if build(5000, "Silver") == base {
		t.Error("another name of a new category must make the preview stale")
	}
}

func TestLoRequestParamsNew_AsksCategoriesAndThePriceRangeNotPrices(t *testing.T) {
	t.Parallel()
	alg := openapi.SeatLayoutAlgorithm{Id: "x", Params: []openapi.SeatLayoutParam{
		{Name: "categories", Type: "integer"}, {Name: "price_max", Type: "integer"}, {Name: "price_min", Type: "integer"},
		{Name: "prices", Type: "integer_list"},
		{Name: "share_template", Type: "enum", Default: "pyramid", Options: &[]openapi.SeatLayoutParamOption{{Value: "pyramid"}}},
	}}
	if !loSupportsNew(alg) {
		t.Fatal("the algorithm takes the three parameters")
	}
	raw, _ := json.Marshal(loRequestParamsNew(alg, loNew{Cats: 3, Top: 12000, Bottom: 4000}, nil))
	if want := `{"categories":3,"price_max":12000,"price_min":4000,"share_template":"pyramid"}`; string(raw) != want {
		t.Errorf("params = %s, want %s", raw, want)
	}
	if loSupportsNew(loAlgorithm()) {
		t.Error("an algorithm without price_max cannot split one category")
	}
}

func TestLoParsePrice_Limits(t *testing.T) {
	t.Parallel()
	good := map[string]int64{"120": 12000, "49,90": 4990, "1 200": 120000, " 5.5 ": 550, "100€": 10000, "1000000": 100_000_000}
	for in, want := range good {
		if got, ok := loParsePrice(in); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0", "0,00", "-5", "abc", "1000001", "99999999999999999999", "1,234,567,890,123"} {
		if got, ok := loParsePrice(in); ok {
			t.Errorf("%q must be refused, got %d", in, got)
		}
	}
}

func TestSplitSeatsByPriceButton_OpensTheLayoutScreenThroughTheRouter(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	event, session := uuid.New(), uuid.New()
	btn := b.splitSeatsByPriceButton("en", event, session)
	if btn.Label == "" || btn.URL != "" {
		t.Fatalf("button = %+v", btn)
	}
	// loKeyboard puts the "sl:" prefix on, as the router expects.
	kb := loKeyboard([][]Button{{btn}})
	data := kb.InlineKeyboard[0][0].CallbackData
	if len(data) > 64 {
		t.Errorf("a callback is limited to 64 bytes, got %d", len(data))
	}
	if !strings.HasPrefix(data, "sl:") {
		t.Fatalf("data = %q", data)
	}
	// The bot routes "sl:" to seatLayoutCallback with the prefix cut off, which
	// splits it into a kind and an argument; kind "o" opens the screen of the
	// date named by the argument, the same seatLayoutOpen the date card calls.
	kind, arg, _ := strings.Cut(strings.TrimPrefix(data, "sl:"), ":")
	got, err := uuid.Parse(arg)
	if kind != "o" || err != nil || got != session {
		t.Errorf("kind %q arg %q parse %v", kind, arg, err)
	}
	// The dialog owns the press, so it does not end another screen's dialog by mistake.
	if !dialogOwners[seatLayoutDialogKind].owns(data) {
		t.Error("the seat layout dialog must own its own presses")
	}
}

func TestLoPreviewScreen_ShowsRevenuePerCategoryNewMarkAndRealisticEstimate(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	old := loTarget{ID: uuid.New(), Name: "Stalls", Price: 9000}
	res := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 9000, ""), slotWith("c2", 6000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2", "s|3": "c2"})
	res.Metrics = openapi.SeatLayoutMetrics{
		Currency: "EUR", TotalSeats: 3, RevenueEstimateMinor: 21000, RealisticRevenueMinor: 16000,
		BySlot: []openapi.SeatLayoutSlotMetrics{
			{SlotKey: "c1", Seats: 1, OpenSeats: 1, PriceAmount: 9000, RevenueMinor: 9000, Share: 0.33},
			{SlotKey: "c2", Seats: 2, OpenSeats: 2, PriceAmount: 6000, RevenueMinor: 12000, Share: 0.67},
		},
	}
	plan, err := loBuildPlanNew(res, []loTarget{old}, []string{"Stalls"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	tiers := []openapi.TicketTierItem{loTier("Stalls", 9000, "seated", true, 1)}
	p := loProposal{Res: res, Targets: []loTarget{old}, Tiers: tiers, Plan: plan, Split: true, Alg: loAlgorithm()}
	st := seatLayoutDialog{EventName: "Show", When: "soon", Cats: 2, Top: 9000, Bottom: 6000, Cur: "EUR"}
	for _, loc := range []string{"en", "ru", "es"} {
		text, rows := b.loPreviewScreen(loc, st, p, "")
		for _, want := range []string{"Stalls", "90 EUR", "120 EUR", "210 EUR", "160 EUR", "60 EUR"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: the preview lacks %q:\n%s", loc, want, text)
			}
		}
		if strings.Contains(text, "<no value>") {
			t.Errorf("%s: unrendered value:\n%s", loc, text)
		}
		var apply, change bool
		for _, r := range rows {
			for _, bt := range r {
				apply = apply || strings.HasPrefix(bt.Data, "ap:")
				change = change || bt.Data == "nq"
			}
		}
		if !apply || !change {
			t.Errorf("%s: buttons apply=%v change=%v", loc, apply, change)
		}
	}
	text, _ := b.loPreviewScreen("en", st, p, "")
	if !strings.Contains(text, "Category 1 (new)") || strings.Contains(text, "Stalls (new)") {
		t.Errorf("only the new category is marked new:\n%s", text)
	}
	if !strings.Contains(text, "Realistic estimate: about 160 EUR") || !strings.Contains(text, "90%") || !strings.Contains(text, "60%") {
		t.Errorf("realistic line missing:\n%s", text)
	}
}

func TestLoAppliedScreen_ListsCreatedCategoriesAndSaysWhenTheOldOneIsEmpty(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	old := loTarget{ID: uuid.New(), Name: "Stalls", Price: 9000}
	// No slot at the old price: the old category gets no seat.
	res := splitSuggest([]openapi.SeatLayoutSlot{slotWith("c1", 8000, ""), slotWith("c2", 4000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2"})
	plan, err := loBuildPlanNew(res, []loTarget{old}, []string{"Stalls"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	p := loProposal{Res: res, Targets: []loTarget{old}, Tiers: []openapi.TicketTierItem{loTier("Stalls", 9000, "seated", true, 1)}, Plan: plan, Split: true}
	created := []openapi.SeatBatchCreatedTier{
		{Key: "n1", Name: "Category 1", PriceAmount: 8000, TierId: uuid.New()},
		{Key: "n2", Name: "Category 2", PriceAmount: 4000, TierId: uuid.New()},
	}
	batch := openapi.SeatBatch{Id: uuid.New(), Changes: make([]openapi.SeatBatchChange, 2), CreatedTiers: &created, Skipped: []openapi.SeatBatchSkipped{}}
	text, rows, made := b.loAppliedScreen("en", seatLayoutDialog{EventName: "Show", When: "soon"}, p, batch)
	for _, want := range []string{"New categories:", "Category 1", "80 EUR", "Category 2", "«Stalls» has no seats now", "I did not close it", "new categories stay"} {
		if !strings.Contains(text, want) {
			t.Errorf("the applied message lacks %q:\n%s", want, text)
		}
	}
	if !made || len(rows) == 0 || !strings.HasPrefix(rows[0][0].Data, "rv:") {
		t.Errorf("undo button missing: made=%v rows=%v", made, rows)
	}
	// Sold seats keep the old category non-empty: no such line.
	p.Res.Metrics.SoldFrozen = 1
	text, _, _ = b.loAppliedScreen("en", seatLayoutDialog{}, p, batch)
	if strings.Contains(text, "has no seats now") {
		t.Errorf("the old category still holds a sold seat:\n%s", text)
	}
}

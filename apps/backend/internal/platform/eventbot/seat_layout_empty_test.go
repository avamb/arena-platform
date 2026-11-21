package eventbot

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func withCounts(t openapi.TicketTierItem, seats, ga int64) openapi.TicketTierItem {
	t.SeatCount, t.GaUnitCount = &seats, &ga
	return t
}

// The stand scenario: one category of 25 seats plus an empty VIP at 600.
func TestLoEmptyOpen_PicksOnlyEmptyOpenFixedPriceCategories(t *testing.T) {
	t.Parallel()
	seated := withCounts(loTier("Stalls", 30000, "seated", true, 1), 25, 0)
	vip := withCounts(loTier("VIP", 60000, "ga", true, 2), 0, 0)
	standing := withCounts(loTier("Standing", 60000, "ga", true, 3), 0, 40)
	closed := withCounts(loTier("Shut", 60000, "ga", false, 4), 0, 0)
	free := withCounts(loTier("Press", 0, "ga", true, 5), 0, 0)
	free.PricingMode = openapi.TicketTierItemPricingModeFree
	unknown := loTier("Unknown", 60000, "ga", true, 6) // no counts at all

	got := loEmptyOpen([]openapi.TicketTierItem{seated, vip, standing, closed, free, unknown})
	if len(got) != 1 || got[0].ID != vip.Id || got[0].Price != 60000 {
		t.Fatalf("only the empty open fixed-price category is offered: %+v", got)
	}
	targets, _ := loTargets([]openapi.TicketTierItem{seated, vip, standing})
	if len(targets) != 1 || targets[0].ID != seated.Id {
		t.Fatalf("loTargets keeps its meaning: %+v", targets)
	}
}

func TestLoBuildPlanNew_EmptyCategoryOfTheSamePriceIsTaken(t *testing.T) {
	t.Parallel()
	old := loTarget{ID: loTier("Stalls", 30000, "seated", true, 1).Id, Name: "Stalls", Price: 30000}
	vip := loTarget{ID: loTier("VIP", 60000, "ga", true, 2).Id, Name: "VIP", Price: 60000}
	res := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 60000, "#c0392b"), slotWith("c2", 15000, "#e67e22")},
		map[string]string{"s|1": "c1", "s|2": "c2"})
	plan, err := loBuildPlanNew(res, []loTarget{old}, []loTarget{vip}, []string{"Stalls", "VIP"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNew(0) || plan.Tier[0] != vip.ID || plan.Name[0] != "VIP" {
		t.Errorf("slot 600 goes to the empty VIP: %+v", plan)
	}
	if len(plan.NewTiers) != 1 || plan.NewTiers[0].PriceAmount != 15000 {
		t.Errorf("only the 150 category is new: %+v", plan.NewTiers)
	}
	for _, n := range plan.Name {
		if n == "Category 1" && plan.IsNew(0) {
			t.Errorf("no Category 1 for the VIP slot")
		}
	}

	// The 300 slot equals the old category's price: no new category at all.
	res2 := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 60000, ""), slotWith("c2", 30000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2"})
	p2, err := loBuildPlanNew(res2, []loTarget{old}, []loTarget{vip}, []string{"Stalls", "VIP"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.NewTiers) != 0 || p2.Tier[0] != vip.ID || p2.Tier[1] != old.ID {
		t.Errorf("no new categories expected: %+v", p2)
	}
}

func TestLoBuildPlanNew_EmptyCategoryOfAnotherPriceIsLeftAlone(t *testing.T) {
	t.Parallel()
	old := loTarget{ID: loTier("Stalls", 30000, "seated", true, 1).Id, Name: "Stalls", Price: 30000}
	vip := loTarget{ID: loTier("VIP", 70000, "ga", true, 2).Id, Name: "VIP", Price: 70000}
	res := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 60000, ""), slotWith("c2", 15000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2"})
	plan, err := loBuildPlanNew(res, []loTarget{old}, []loTarget{vip}, []string{"Stalls", "VIP"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tier[0] == vip.ID || !plan.IsNew(0) || len(plan.NewTiers) != 2 {
		t.Errorf("a different price must not take the empty category: %+v", plan)
	}
}

func TestLoBuildPlanNew_TwoEmptyCategoriesOfOnePriceTakeOneOnce(t *testing.T) {
	t.Parallel()
	old := loTarget{ID: loTier("Stalls", 30000, "seated", true, 1).Id, Name: "Stalls", Price: 30000}
	a := loTarget{ID: loTier("VIP A", 60000, "ga", true, 2).Id, Name: "VIP A", Price: 60000}
	b := loTarget{ID: loTier("VIP B", 60000, "ga", true, 3).Id, Name: "VIP B", Price: 60000}
	// Two slots at the same price: each empty category is used once.
	res := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 60000, ""), slotWith("c2", 60000, ""), slotWith("c3", 15000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2", "s|3": "c3"})
	plan, err := loBuildPlanNew(res, []loTarget{old}, []loTarget{a, b}, []string{"Stalls", "VIP A", "VIP B"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tier[0] != a.ID || plan.Tier[1] != b.ID {
		t.Errorf("each empty category once: %+v", plan.Tier)
	}
	// One slot only: the first empty category is taken, the second stays.
	res1 := splitSuggest(
		[]openapi.SeatLayoutSlot{slotWith("c1", 60000, ""), slotWith("c2", 15000, "")},
		map[string]string{"s|1": "c1", "s|2": "c2"})
	p1, err := loBuildPlanNew(res1, []loTarget{old}, []loTarget{a, b}, []string{"Stalls", "VIP A", "VIP B"}, catName)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Tier[0] != a.ID || p1.IsNew(0) {
		t.Errorf("one empty category is taken: %+v", p1)
	}
}

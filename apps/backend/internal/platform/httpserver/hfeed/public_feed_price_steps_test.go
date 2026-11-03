package hfeed

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

func chainFixture() ([]gen.TicketTierRow, []gen.TierChainRow, [3]uuid.UUID) {
	ids := [3]uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	tiers := []gen.TicketTierRow{
		{ID: ids[0], Name: "Parter", PriceAmount: 2000},
		{ID: ids[1], Name: "Parter · 2", PriceAmount: 2200},
		{ID: ids[2], Name: "Parter · 3", PriceAmount: 2400},
		{ID: uuid.New(), Name: "Balcony", PriceAmount: 1000},
	}
	handed := time.Now()
	links := []gen.TierChainRow{
		{TierID: ids[0], NextTierID: ids[1], HandedOverAt: &handed},
		{TierID: ids[1], NextTierID: ids[2]},
	}
	return tiers, links, ids
}

func TestBuildChainView_SmartChainShowsOneCardUnderTheFirstName(t *testing.T) {
	tiers, links, ids := chainFixture()
	v := buildChainView(tiers, links, map[uuid.UUID]bool{ids[0]: true})
	if !v.hide[ids[0]] || v.hide[ids[1]] || !v.hide[ids[2]] {
		t.Fatalf("hide = %v: only the selling step (2) may show", v.hide)
	}
	if v.name[ids[1]] != "Parter" {
		t.Errorf("the selling step is named %q, want Parter", v.name[ids[1]])
	}
	left := 7
	tr := publicFeedTierResponse{CurrentPrice: 2200, Available: &left}
	ps := v.priceStep(ids[1], tr, map[uuid.UUID]priceresolve.Effective{}, tiers)
	if ps == nil || ps.NextPrice != 2400 || *ps.RemainingAtPrice != 7 {
		t.Fatalf("price step = %+v", ps)
	}
	if v.priceStep(ids[2], publicFeedTierResponse{CurrentPrice: 2400}, nil, tiers) != nil {
		t.Error("the last step has no next price")
	}
}

func TestBuildChainView_HandMadeChainKeepsPastStepsAndNames(t *testing.T) {
	tiers, links, ids := chainFixture()
	v := buildChainView(tiers, links, nil)
	if v.hide[ids[0]] || v.hide[ids[1]] || !v.hide[ids[2]] {
		t.Fatalf("hide = %v: the past step stays (honest sold out), the future one goes", v.hide)
	}
	if len(v.name) != 0 {
		t.Errorf("a hand-made chain keeps its own names: %v", v.name)
	}
}

func TestBuildChainView_StoppedPresetShowsNoRise(t *testing.T) {
	tiers, links, ids := chainFixture()
	tiers[2].PriceAmount = 2200 // stop froze the later steps at the current price
	v := buildChainView(tiers, links, map[uuid.UUID]bool{ids[0]: true})
	if ps := v.priceStep(ids[1], publicFeedTierResponse{CurrentPrice: 2200}, nil, tiers); ps != nil {
		t.Errorf("an equal next step must not announce a rise: %+v", ps)
	}
}

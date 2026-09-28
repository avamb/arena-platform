package hbil24

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// A selling step names its next category and the waiting category reports
// the places it sells in the same order once the step runs out, while its
// own availability stays 0 so an older site does not offer it.
func TestCatalog_ChainStepNamesNextAndWaitingPlaces(t *testing.T) {
	h := &Handler{}
	now := time.Now().UTC()
	start, next, last := uuid.New(), uuid.New(), uuid.New()
	ended := now.Add(-time.Hour)
	tiers := []gen.ActionEventTierRow{
		{Tier: gen.TicketTierRow{ID: start, Name: "Start", PriceAmount: 10000, IsOpen: true}, IsGA: true, GAUnitsTotal: 3, GAUnitsAvailable: 1},
		{Tier: gen.TicketTierRow{ID: next, Name: "Regular", PriceAmount: 15000}, IsGA: true, GAUnitsTotal: 7, GAUnitsAvailable: 7},
		{Tier: gen.TicketTierRow{ID: last, Name: "Door", PriceAmount: 20000, SaleWindowEnd: &ended}, IsGA: true, GAUnitsTotal: 2, GAUnitsAvailable: 2},
	}
	chain := catalogChain{
		next:    map[uuid.UUID]uuid.UUID{start: next, next: last},
		waiting: map[uuid.UUID]bool{next: true, last: true},
	}
	cats, _, _, _ := h.projectCategories(context.Background(), tiers, map[uuid.UUID]int64{}, 10, now, chain)
	if len(cats) != 3 {
		t.Fatalf("categories = %d, want 3", len(cats))
	}
	if cats[0]["availability"] != 1 || cats[0]["nextCategoryPriceId"] == nil {
		t.Errorf("selling step = %v, want availability 1 and a nextCategoryPriceId", cats[0])
	}
	if _, ok := cats[0]["waitingAvailability"]; ok {
		t.Errorf("the selling step is not waiting: %v", cats[0])
	}
	if cats[1]["availability"] != 0 || cats[1]["waitingAvailability"] != 7 {
		t.Errorf("waiting step = %v, want availability 0 and waitingAvailability 7", cats[1])
	}
	if _, ok := cats[2]["waitingAvailability"]; ok {
		t.Errorf("a waiting category whose own sale ended sells nothing: %v", cats[2])
	}
	if cats[1]["nextCategoryPriceId"] == nil {
		t.Errorf("a waiting step still names its own next: %v", cats[1])
	}

	plain, _, _, _ := h.projectCategories(context.Background(), tiers[:1], map[uuid.UUID]int64{}, 10, now, catalogChain{})
	if _, ok := plain[0]["nextCategoryPriceId"]; ok {
		t.Errorf("a category outside a chain carries no extension: %v", plain[0])
	}
}

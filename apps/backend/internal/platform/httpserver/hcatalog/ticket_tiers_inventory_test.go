// ticket_tiers_inventory_test.go — the per-category place counters of the
// admin category table for SEATED categories (F-58, functional run
// 2026-09-19). The GA quota counters cover only ga_unit places, so a seated
// category reported «sold 0, available —» however many seats were sold.
//
// Pure unit tests — no live PostgreSQL required.
package hcatalog

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestTierInventory_SeatedCategoryGetsItsSeatCounters(t *testing.T) {
	parter, balcony, ga := uuid.New(), uuid.New(), uuid.New()
	seatCounts, gaCounts, seatStats := tierInventory([]gen.SessionSeatTierCountRow{
		// 50 plan seats: 2 sold, 1 held, 45 free, 2 blocked ('unavailable').
		{TierID: parter, Kind: "seat", Count: 50, Held: 1, Sold: 2, Available: 45},
		{TierID: balcony, Kind: "seat", Count: 16, Available: 16},
		{TierID: ga, Kind: "ga_unit", Count: 100, Sold: 7, Available: 93},
	})

	if seatCounts[parter] != 50 || seatCounts[balcony] != 16 {
		t.Errorf("seatCounts = %v, want parter 50, balcony 16", seatCounts)
	}
	if gaCounts[ga] != 100 {
		t.Errorf("gaCounts[ga] = %d, want 100", gaCounts[ga])
	}

	st, ok := seatStats[parter]
	if !ok {
		t.Fatal("seated category parter has no counters")
	}
	if st.Sold != 2 || st.Held != 1 || st.Available != 45 {
		t.Errorf("parter sold/held/available = %d/%d/%d, want 2/1/45", st.Sold, st.Held, st.Available)
	}
	// Blocked seats are not for sale: quantity is what can be sold at all.
	if st.Quantity != 48 {
		t.Errorf("parter quantity = %d, want 48 (50 seats minus 2 blocked)", st.Quantity)
	}
	if st.TierID != parter {
		t.Errorf("parter TierID = %s, want %s", st.TierID, parter)
	}
	if b := seatStats[balcony]; b.Quantity != 16 || b.Available != 16 || b.Sold != 0 {
		t.Errorf("balcony counters = %+v, want 16 available of 16", b)
	}
	// GA places are counted by the quota mechanism, never here.
	if _, ok := seatStats[ga]; ok {
		t.Error("a GA category must not get seat counters")
	}
}

func TestTierInventory_EmptyIsSafe(t *testing.T) {
	seatCounts, gaCounts, seatStats := tierInventory(nil)
	if len(seatCounts) != 0 || len(gaCounts) != 0 || len(seatStats) != 0 {
		t.Errorf("nil input must yield empty maps, got %v %v %v", seatCounts, gaCounts, seatStats)
	}
}

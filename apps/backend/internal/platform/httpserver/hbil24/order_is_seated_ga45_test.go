// order_is_seated_ga45_test.go pins the discriminator CREATE_ORDER_EXT uses
// to decide whether a cart needs line reconciliation (plan
// 08_architecture/23 step 4).
package hbil24

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestOrderIsSeated_DecidesOnKindNotTier is the regression guard for the
// defect migration 0101 would otherwise have introduced: orderIsSeated used
// to answer "seated" for any reservation seat carrying a tier_id, which was
// only ever true of real seats because a GA place drawn from the fungible
// pool carried none. Every GA place now carries its category, so the old
// test would have called every GA cart seated and silently switched
// orderReconcile off for all of them.
func TestOrderIsSeated_DecidesOnKindNotTier(t *testing.T) {
	tierID := uuid.New()

	gaPlaces := []gen.SessionSeatRow{
		{SeatKey: "ga|t1|000001", TierID: &tierID, Kind: "ga_unit"},
		{SeatKey: "ga|t1|000002", TierID: &tierID, Kind: "ga_unit"},
	}
	if orderIsSeated(gaPlaces) {
		t.Error("orderIsSeated = true for a GA cart whose places carry their category; " +
			"orderReconcile would stop running for every GA order")
	}

	seats := []gen.SessionSeatRow{
		{SeatKey: "Parter|A|1", TierID: &tierID, Kind: "seat"},
	}
	if !orderIsSeated(seats) {
		t.Error("orderIsSeated = false for a cart holding a real seat")
	}

	// A hybrid cart holding both is seated: its seat lines are not
	// quantities to reconcile.
	mixed := append(append([]gen.SessionSeatRow{}, gaPlaces...), seats...)
	if !orderIsSeated(mixed) {
		t.Error("orderIsSeated = false for a mixed cart that holds a real seat")
	}

	if orderIsSeated(nil) {
		t.Error("orderIsSeated = true for an empty cart")
	}

	// Legacy pre-AB-51 GA rows carried no category at all; they are still
	// not seats.
	legacy := []gen.SessionSeatRow{{SeatKey: "ga|pool|000001", Kind: "ga_unit"}}
	if orderIsSeated(legacy) {
		t.Error("orderIsSeated = true for a legacy tier-less GA place")
	}
}

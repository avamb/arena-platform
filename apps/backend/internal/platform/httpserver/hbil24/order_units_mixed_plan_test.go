// order_units_mixed_plan_test.go pins how CREATE_ORDER_EXT prices a hold.
package hbil24

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestExpandOrderUnits_GAPlacesPickedOnMixedPlan is the regression guard for
// the Lampyris defect of 2026-09-25: on a session whose plan has both seats
// and a GA zone, the site reserves a GA place through seatList, so the hold
// has reservation seats of kind ga_unit and no GA lines. Pricing read only
// the (empty) GA lines and CREATE_ORDER_EXT answered 101 "hold expired" for
// every GA-only cart.
func TestExpandOrderUnits_GAPlacesPickedOnMixedPlan(t *testing.T) {
	ga := uuid.New()
	pricing := newCartPricing()
	pricing.price[ga] = 69000

	seats := []gen.SessionSeatRow{
		{SeatKey: "ga|t1|000001", TierID: &ga, Kind: "ga_unit"},
		{SeatKey: "ga|t1|000002", TierID: &ga, Kind: "ga_unit"},
	}
	units := expandOrderUnits(nil, seats, pricing)
	if len(units) != 2 {
		t.Fatalf("units = %d, want 2 (one per held GA place)", len(units))
	}
	for _, u := range units {
		if u.tierID != ga || u.price != 69000 {
			t.Errorf("unit = %+v, want tier %s at 69000", u, ga)
		}
	}
}

// TestExpandOrderUnits_CategoryListHoldUsesLines keeps the categoryList GA
// path on its locked GA lines.
func TestExpandOrderUnits_CategoryListHoldUsesLines(t *testing.T) {
	ga := uuid.New()
	pricing := newCartPricing()
	pricing.price[ga] = 99999 // today's list price must not win over the lock

	items := []gen.ReservationGAItemRow{{TierID: ga, Quantity: 3, UnitPrice: 69000}}
	seats := []gen.SessionSeatRow{
		{SeatKey: "ga|t1|000001", TierID: &ga, Kind: "ga_unit"},
		{SeatKey: "ga|t1|000002", TierID: &ga, Kind: "ga_unit"},
		{SeatKey: "ga|t1|000003", TierID: &ga, Kind: "ga_unit"},
	}
	units := expandOrderUnits(items, seats, pricing)
	if len(units) != 3 {
		t.Fatalf("units = %d, want 3", len(units))
	}
	for _, u := range units {
		if u.price != 69000 {
			t.Errorf("price = %d, want the locked 69000", u.price)
		}
	}
}

// TestExpandOrderUnits_SeatPlusGAPlace prices a mixed cart (a real seat and
// a GA place) from its seats, as before.
func TestExpandOrderUnits_SeatPlusGAPlace(t *testing.T) {
	seat, ga := uuid.New(), uuid.New()
	pricing := newCartPricing()
	pricing.price[seat] = 189000
	pricing.price[ga] = 69000

	seats := []gen.SessionSeatRow{
		{SeatKey: "balcony|1|1", TierID: &seat, Kind: "seat"},
		{SeatKey: "ga|t1|000001", TierID: &ga, Kind: "ga_unit"},
	}
	units := expandOrderUnits(nil, seats, pricing)
	if len(units) != 2 || units[0].price+units[1].price != 189000+69000 {
		t.Fatalf("units = %+v, want seat 189000 + GA 69000", units)
	}
}

// TestExpandOrderUnits_EmptyHold still yields nothing, which CREATE_ORDER_EXT
// reports as an expired hold.
func TestExpandOrderUnits_EmptyHold(t *testing.T) {
	if units := expandOrderUnits(nil, nil, newCartPricing()); len(units) != 0 {
		t.Fatalf("units = %d, want 0", len(units))
	}
}

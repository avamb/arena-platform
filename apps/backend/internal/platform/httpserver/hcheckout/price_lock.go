// price_lock.go — AB-48 step 9: the quoted price is locked at reservation
// creation for the reservation's TTL.
//
// reservation_ga_items (0063) is the lock record for EVERY reservation
// shape since AB-48: GA multi-tier holds already wrote per-tier lines at
// hold time; seated and single-tier GA reservations now do the same via
// WriteReservationPriceLinesTx. Checkout confirmation charges the stored
// unit_price (LockedTierPrices) and only re-resolves for legacy
// reservations that carry no lines. A cart held across a price-window
// boundary therefore keeps the price it was quoted, and issued tickets
// are never repriced.
package hcheckout

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// WriteReservationPriceLinesTx resolves the effective price of every tier
// in `quantities` at `at` through the ONE resolver and snapshots one
// reservation_ga_items line per tier inside the caller's transaction.
//
// pwyw tiers are skipped (their price is the buyer's choice, supplied at
// checkout); free tiers lock at 0. Returns the locked unit price per tier.
func WriteReservationPriceLinesTx(
	ctx context.Context,
	q *gen.Queries,
	sessionID, reservationID uuid.UUID,
	quantities map[uuid.UUID]int32,
	at time.Time,
) (map[uuid.UUID]int64, error) {
	locked := make(map[uuid.UUID]int64, len(quantities))
	if len(quantities) == 0 {
		return locked, nil
	}
	tiers := make([]gen.TicketTierRow, 0, len(quantities))
	for tierID := range quantities {
		t, err := q.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			return nil, err
		}
		if t.PricingMode == "pwyw" {
			continue
		}
		tiers = append(tiers, t)
	}
	eff, err := priceresolve.ForTiers(ctx, q, tiers, at)
	if err != nil {
		return nil, err
	}
	for _, t := range tiers {
		unit := eff[t.ID].Amount
		if t.PricingMode == "free" {
			unit = 0
		}
		if err := q.InsertReservationGAItem(ctx, reservationID, t.ID, quantities[t.ID], unit); err != nil {
			return nil, err
		}
		locked[t.ID] = unit
	}
	return locked, nil
}

// LockedTierPrices returns the per-tier unit prices snapshotted for a
// reservation (empty map for legacy reservations without lines).
func LockedTierPrices(ctx context.Context, q *gen.Queries, reservationID uuid.UUID) (map[uuid.UUID]int64, error) {
	items, err := q.ListReservationGAItems(ctx, reservationID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int64, len(items))
	for _, it := range items {
		out[it.TierID] = it.UnitPrice
	}
	return out, nil
}

// EffectiveFixedPrice resolves a fixed-mode tier's current price through
// the ONE resolver; non-fixed tiers return their base amount unchanged.
func EffectiveFixedPrice(ctx context.Context, q priceresolve.WindowLister, tier gen.TicketTierRow, at time.Time) (priceresolve.Effective, error) {
	return priceresolve.ForTier(ctx, q, tier, at)
}

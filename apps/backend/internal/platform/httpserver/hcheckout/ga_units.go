// ga_units.go — AB-51 shared General Admission unit allocation.
//
// Every GA place is a session_seats row (kind='ga_unit') with the same
// status machine as an assigned seat. A GA hold therefore allocates N
// concrete units (available -> held, reservation stamped) instead of
// decrementing a counter; the buyer-facing behaviour is unchanged.
//
// Ledger accounting: GA holds reserve SESSION-LEVEL capacity (nil tier)
// — the same accounting the seated path has always used — and the unit
// rows are the per-tier truth. The former per-tier ledger rows only
// ever existed in seeded databases (no production code path created
// them), so the per-tier ReserveCapacity calls they backed are retired.
// Release/expiry mirrors this: released units return session-level
// capacity; legacy pre-AB-51 reservations keep their old release paths.
package hcheckout

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// GAUnitLine is one tier line of a GA allocation request.
type GAUnitLine struct {
	TierID   *uuid.UUID // nil only on legacy tier-less session-level holds
	Quantity int32
}

// AllocateGAUnitsTx allocates concrete ga_unit rows for every line of a
// GA hold inside the caller's transaction, and links them to the
// reservation via reservation_seats. The caller must have bumped
// seat_status_version already (pass the fresh value).
//
// Allocation pool per line:
//   - plan-bound session: units carrying exactly the line's tier
//     (materialized at bind — one pool per GA category).
//   - plan-less session: the fungible NULL-tier pool; the line tier is
//     stamped onto the allocated units so ticket issuance knows the
//     tier, and released units are reset back to NULL by
//     releaseReservationSeatsTx. A tier with ticket_tiers.capacity set
//     is additionally guarded against exceeding that capacity.
//
// A short allocation returns *CapacityError (with the line's tier) and
// the caller MUST roll back.
func AllocateGAUnitsTx(
	ctx context.Context,
	txq *gen.Queries,
	sessionID, reservationID uuid.UUID,
	statusVersion int64,
	planBound bool,
	lines []GAUnitLine,
) ([]gen.SessionSeatRow, error) {
	var all []gen.SessionSeatRow
	for _, line := range lines {
		if line.Quantity <= 0 {
			return nil, ErrHoldInvalidInput
		}
		var unitFilter *uuid.UUID
		if planBound {
			// Plan-bound pools are keyed by the category tier; a hold
			// against a tier with no unit pool (e.g. a hand-added extra
			// tier) simply finds nothing and reads as over-capacity.
			unitFilter = line.TierID
		}
		units, err := txq.AllocateGAUnitsForHold(
			ctx, sessionID, reservationID, line.TierID, statusVersion,
			unitFilter, line.Quantity,
		)
		if err != nil {
			return nil, fmt.Errorf("hcheckout: allocate GA units: %w", err)
		}
		if int32(len(units)) != line.Quantity { //nolint:gosec // len bounded by LIMIT quantity
			return nil, &CapacityError{TierID: line.TierID, Requested: line.Quantity}
		}
		// Tier-capacity guard for plan-less pools: the pool itself is
		// shared, so a capped tier must not swallow more of it than its
		// declared ticket_tiers.capacity.
		if !planBound && line.TierID != nil {
			tier, err := txq.GetTicketTierByID(ctx, *line.TierID, sessionID)
			if err != nil {
				return nil, fmt.Errorf("hcheckout: GA tier lookup: %w", err)
			}
			if tier.Capacity != nil {
				used, err := txq.CountGAUnitsHeldSoldByTier(ctx, sessionID, *line.TierID)
				if err != nil {
					return nil, fmt.Errorf("hcheckout: GA tier usage count: %w", err)
				}
				if used > int64(*tier.Capacity) {
					return nil, &CapacityError{TierID: line.TierID, Requested: line.Quantity}
				}
			}
		}
		for _, u := range units {
			if err := txq.InsertReservationSeat(ctx, reservationID, u.ID); err != nil {
				return nil, fmt.Errorf("hcheckout: link GA unit %s: %w", u.SeatKey, err)
			}
		}
		all = append(all, units...)
	}
	return all, nil
}

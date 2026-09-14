// ga_units.go — AB-51 shared General Admission place allocation.
//
// Every GA place is a session_seats row (kind='ga_unit') with the same
// status machine as an assigned seat. A GA hold therefore allocates N
// concrete places (available -> held, reservation stamped) instead of
// decrementing a counter; the buyer-facing behaviour is unchanged.
//
// Since migration 0101 (plan 08_architecture/23) a GA category OWNS its
// places: every ga_unit row carries a hard tier_id and a category's places
// are keyed 'ga|t<unit_seq>|<n>'. The pre-0101 alternative — one fungible
// NULL-tier pool per plan-less session, stamped with the line tier on hold
// and reset to NULL on release, guarded against ticket_tiers.capacity —
// is gone, and with it the planBound branch this file used to carry.
//
// Ledger accounting: GA holds reserve SESSION-LEVEL capacity (nil tier)
// — the same accounting the seated path has always used — and the place
// rows are the per-category truth. Per-category ledger rows do not exist.
package hcheckout

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// GAUnitLine is one category line of a GA allocation request. TierID is
// required: a category owns its places, so a line without one would match
// nothing.
type GAUnitLine struct {
	TierID   *uuid.UUID
	Quantity int32
}

// AllocateGAUnitsTx allocates concrete ga_unit rows for every line of a
// GA hold inside the caller's transaction, and links them to the
// reservation via reservation_seats. The caller must have bumped
// seat_status_version already (pass the fresh value).
//
// Each line draws from its own category's places and from nowhere else.
// A short allocation returns *CapacityError (with the line's tier) and
// the caller MUST roll back.
func AllocateGAUnitsTx(
	ctx context.Context,
	txq *gen.Queries,
	sessionID, reservationID uuid.UUID,
	statusVersion int64,
	lines []GAUnitLine,
) ([]gen.SessionSeatRow, error) {
	var all []gen.SessionSeatRow
	for _, line := range lines {
		if line.Quantity <= 0 || line.TierID == nil {
			return nil, ErrHoldInvalidInput
		}
		units, err := txq.AllocateGAUnitsForHold(
			ctx, sessionID, reservationID, *line.TierID, statusVersion, line.Quantity,
		)
		if err != nil {
			return nil, fmt.Errorf("hcheckout: allocate GA places: %w", err)
		}
		if int32(len(units)) != line.Quantity { //nolint:gosec // len bounded by LIMIT quantity
			return nil, &CapacityError{TierID: line.TierID, Requested: line.Quantity}
		}
		for _, u := range units {
			if err := txq.InsertReservationSeat(ctx, reservationID, u.ID); err != nil {
				return nil, fmt.Errorf("hcheckout: link GA place %s: %w", u.SeatKey, err)
			}
		}
		all = append(all, units...)
	}
	return all, nil
}

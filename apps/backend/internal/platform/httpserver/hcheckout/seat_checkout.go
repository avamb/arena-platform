// seat_checkout.go implements the seated branch of the checkout completion
// pipeline (feature #310, Wave SEAT-C2).
//
// Two responsibilities live here:
//
//  1. sellReservationSeatsTx — the held → sold state-machine transition
//     invoked when a checkout completes. Mirrors the abandon/expire path
//     (releaseReservationSeatsTx in seat_reservations.go) so both sides of the
//     seat lifecycle share the same locking / status_version discipline.
//
//  2. buildSeatedPricingLines — turns a set of reservation_seats rows plus the
//     resolved unit price per tier_id into the []PricingLineInput slice that
//     ComputePricingLines expects. Grouping is deterministic (tier_id ASC) so
//     tests and audit logs are stable.
//
// §5.2 concurrency contract:
//
//   - The caller passes a *gen.Queries already scoped to their transaction
//     (via q.WithTx(tx)). All conditional UPDATEs are stamped with a single
//     bumped seat_status_version so delta pollers observe the whole batch
//     atomically.
//   - A missing / non-held seat (pgx.ErrNoRows) surfaces as a partial-sell
//     conflict: the caller must roll back the tx to preserve the double-sell
//     guarantee. The idempotent sold-repeat path is delegated to a re-fetch
//     via ListReservationSeats (already sold seats are counted as sold and
//     skipped) so replayed webhook completions do not fail.
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// errSeatDoubleSell is returned when the SellSessionSeat conditional UPDATE
// affects zero rows and the current status is not already 'sold'. Callers
// should abort the tx so no partial-sell ever commits.
var errSeatDoubleSell = errors.New("hcheckout: seat is not in 'held' state (double-sell attempt)")

// seatSellQuerier is the narrow surface of *gen.Queries that
// sellReservationSeatsTx needs. Declaring it as an interface lets in-package
// unit tests substitute an in-memory fake state machine so the
// double-sell / expiry-race / partial-conflict-rollback branches of §5.2 can
// be exercised without a live PostgreSQL connection.
type seatSellQuerier interface {
	ListReservationSeats(ctx context.Context, reservationID uuid.UUID) ([]gen.SessionSeatRow, error)
	IncrementSessionSeatStatusVersion(ctx context.Context, sessionID uuid.UUID) (int64, error)
	SellSessionSeat(ctx context.Context, id, reservationID uuid.UUID, statusVersion int64) (gen.SessionSeatRow, error)
}

// sellReservationSeatsTx flips every session_seats row linked to the given
// reservation from 'held' to 'sold'. Called from IssueTicketsForCheckout (the
// canonical held → sold state-machine transition point per SEAT-C2).
//
// Semantics:
//
//   - Idempotent: seats already in 'sold' state (e.g. webhook replay) are
//     counted as sold and skipped without erroring. This matches the
//     idempotency guarantee IssueTicketsForCheckout provides at the ticket
//     level.
//   - Atomic: a single IncrementSessionSeatStatusVersion stamp is applied to
//     every transitioned seat so the delta feed emits one coherent batch.
//   - Fails hard on double-sell: if a seat is in any state other than 'held'
//     or 'sold' (e.g. 'available' or 'blocked'), the function returns
//     errSeatDoubleSell so the caller aborts the tx.
//   - GA reservations (no reservation_seats rows) are a cheap no-op.
//
// Returns (soldCount, alreadySoldCount, error). Callers can log both counts
// to distinguish first-time issuance from webhook replay.
func sellReservationSeatsTx(ctx context.Context, q seatSellQuerier, sessionID, reservationID uuid.UUID) (sold int, alreadySold int, err error) {
	seats, err := q.ListReservationSeats(ctx, reservationID)
	if err != nil {
		return 0, 0, fmt.Errorf("sellReservationSeatsTx: list reservation seats: %w", err)
	}
	if len(seats) == 0 {
		// GA reservation — nothing to sell.
		return 0, 0, nil
	}

	// Partition: already-sold seats are counted separately (idempotent replay).
	// Any seat in a state other than 'held' / 'sold' is a hard double-sell
	// signal and aborts the tx.
	var toSell []gen.SessionSeatRow
	for _, s := range seats {
		switch s.Status {
		case seatStatusSold:
			alreadySold++
		case seatStatusHeld:
			toSell = append(toSell, s)
		default:
			return sold, alreadySold, fmt.Errorf(
				"%w: seat_key=%s status=%s",
				errSeatDoubleSell, s.SeatKey, s.Status,
			)
		}
	}

	if len(toSell) == 0 {
		// Everything already sold — pure idempotent replay.
		return 0, alreadySold, nil
	}

	newVersion, err := q.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		return 0, alreadySold, fmt.Errorf("sellReservationSeatsTx: bump status_version: %w", err)
	}

	for _, s := range toSell {
		if _, err := q.SellSessionSeat(ctx, s.ID, reservationID, newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Concurrent transition (e.g. TTL worker released the seat
				// between ListReservationSeats and SellSessionSeat). Abort
				// the tx so no partial-sell commits.
				return sold, alreadySold, fmt.Errorf(
					"%w: seat_key=%s (concurrent transition)",
					errSeatDoubleSell, s.SeatKey,
				)
			}
			return sold, alreadySold, fmt.Errorf(
				"sellReservationSeatsTx: sell seat %s: %w", s.SeatKey, err,
			)
		}
		sold++
	}
	return sold, alreadySold, nil
}

// SellReservationSeatsTx is the exported alias of sellReservationSeatsTx for
// use by the htickets sub-package (IssueTicketsForCheckout wires this in as
// the SEAT-C2 held→sold transition point).
func SellReservationSeatsTx(ctx context.Context, q *gen.Queries, sessionID, reservationID uuid.UUID) (int, int, error) {
	return sellReservationSeatsTx(ctx, seatSellQuerier(q), sessionID, reservationID)
}

// ErrSeatDoubleSell is the exported alias of errSeatDoubleSell so callers
// outside hcheckout can errors.Is against it.
var ErrSeatDoubleSell = errSeatDoubleSell

// buildSeatedPricingLines converts a set of reservation_seats rows and a
// tier_id → unit_price lookup into the []PricingLineInput slice that
// ComputePricingLines consumes. Seats are grouped by (tier_id, unit_price)
// and the resulting slice is sorted by TierID ASC so audit logs and JSON
// responses are deterministic.
//
// Seats whose tier_id is nil are grouped under the empty string "" so
// pathological plans still price cleanly.
func buildSeatedPricingLines(seats []gen.SessionSeatRow, tierPrice map[string]int64) []PricingLineInput {
	type key struct {
		tierID    string
		unitPrice int64
	}
	agg := make(map[key]int32, len(seats))
	for _, s := range seats {
		var tid string
		if s.TierID != nil {
			tid = s.TierID.String()
		}
		unit := tierPrice[tid] // 0 for missing (free) tiers
		k := key{tierID: tid, unitPrice: unit}
		agg[k]++
	}

	out := make([]PricingLineInput, 0, len(agg))
	for k, qty := range agg {
		out = append(out, PricingLineInput{
			TierID:    k.tierID,
			Quantity:  qty,
			UnitPrice: k.unitPrice,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TierID != out[j].TierID {
			return out[i].TierID < out[j].TierID
		}
		return out[i].UnitPrice < out[j].UnitPrice
	})
	return out
}

// BuildSeatedPricingLines is the exported alias for use by the httpserver
// shim layer and tests.
func BuildSeatedPricingLines(seats []gen.SessionSeatRow, tierPrice map[string]int64) []PricingLineInput {
	return buildSeatedPricingLines(seats, tierPrice)
}

// Additional seat status constant used by sellReservationSeatsTx. The
// available / held constants live in seat_reservations.go.
const (
	seatStatusHeld = "held"
	seatStatusSold = "sold"
)

// hold_partial.go — the partial variants of CreateSeatedHold and
// ExtendHoldTx for the public hold at the click (spec 08_architecture/67,
// section 4.1).
//
// CreateSeatedHold and ExtendHoldTx are all-or-nothing: one seat that is not
// free rolls the whole transaction back with *SeatConflictsError. The public
// path holds the free seats and returns the others as conflicts, so a buyer
// who clicked a seat that just went keeps the seats that are still free.
//
// Lock order is the one of every hold mutation (hold_mutation.go):
//   - a new hold: sessions row (seat_status_version bump) → session_seats
//     rows in seat_key order → inventory_ledger row;
//   - an existing hold: the reservations row first, then the same.
//
// Neither variant moves the hold's expiry: the public path never slides the
// select deadline on a new seat (spec 67 section 3.3, TTL = 0).
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// splitFreeSeats returns the locked rows that can be held and the conflicts
// of the rest, in requested order.
func splitFreeSeats(requested []string, locked []gen.SessionSeatRow) ([]gen.SessionSeatRow, []map[string]string) {
	conflicts := seatConflicts(requested, locked)
	if len(conflicts) == 0 {
		return locked, nil
	}
	bad := make(map[string]struct{}, len(conflicts))
	for _, c := range conflicts {
		bad[c["seat_key"]] = struct{}{}
	}
	free := make([]gen.SessionSeatRow, 0, len(locked))
	for _, s := range locked {
		if _, ok := bad[s.SeatKey]; ok {
			continue
		}
		free = append(free, s)
	}
	return free, conflicts
}

// holdLockedSeatsTx flips the given locked, free seats to 'held' for the
// reservation and links them.
func holdLockedSeatsTx(ctx context.Context, txq *gen.Queries, reservationID uuid.UUID, seats []gen.SessionSeatRow, version int64) ([]gen.SessionSeatRow, error) {
	held := make([]gen.SessionSeatRow, 0, len(seats))
	for _, s := range seats {
		row, err := txq.HoldSessionSeat(ctx, s.ID, reservationID, version)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, &SeatConflictsError{Conflicts: []map[string]string{
					{"seat_key": s.SeatKey, "status": "unavailable"},
				}}
			}
			return nil, fmt.Errorf("hcheckout: hold seat %s: %w", s.SeatKey, err)
		}
		if err := txq.InsertReservationSeat(ctx, reservationID, s.ID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				return nil, &SeatConflictsError{Conflicts: []map[string]string{
					{"seat_key": s.SeatKey, "status": "unavailable"},
				}}
			}
			return nil, fmt.Errorf("hcheckout: link seat %s: %w", s.SeatKey, err)
		}
		held = append(held, row)
	}
	return held, nil
}

// CreateSeatedHoldPartialTx is CreateSeatedHold in partial mode, inside the
// caller's transaction (txq already scoped with q.WithTx). It holds the
// requested seats that are free and returns the others as conflicts
// ({"seat_key","status"}, as SeatConflictsError). When no seat is free it
// returns *SeatConflictsError and writes nothing that the caller's rollback
// would not undo.
//
// Error contract: the one of CreateSeatedHold (ErrHoldInvalidInput,
// ErrHoldSessionNotFound, ErrHoldSeatsNotSupported, *SeatConflictsError,
// *CapacityError, a category-gate refusal), plus infrastructure errors.
func CreateSeatedHoldPartialTx(ctx context.Context, txq *gen.Queries, in SeatedHoldInput) (SeatedHoldResult, []map[string]string, error) {
	if txq == nil {
		return SeatedHoldResult{}, nil, errors.New("hcheckout: CreateSeatedHoldPartialTx requires queries")
	}
	seats, _, normErr := normalizeSeatKeys(in.SeatKeys)
	if normErr != nil || len(seats) == 0 {
		return SeatedHoldResult{}, nil, ErrHoldInvalidInput
	}
	mode, err := sessionHoldMode(ctx, txq, in.SessionID)
	if err != nil {
		return SeatedHoldResult{}, nil, err
	}
	if mode.AdmissionMode == admissionGeneralAdmission {
		return SeatedHoldResult{}, nil, ErrHoldSeatsNotSupported
	}

	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, in.SessionID)
	if err != nil {
		return SeatedHoldResult{}, nil, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}
	locked, err := txq.LockSessionSeatsForHold(ctx, in.SessionID, seats)
	if err != nil {
		return SeatedHoldResult{}, nil, fmt.Errorf("hcheckout: lock seats: %w", err)
	}
	free, conflicts := splitFreeSeats(seats, locked)
	if len(free) == 0 {
		return SeatedHoldResult{}, nil, &SeatConflictsError{Conflicts: conflicts}
	}
	if err := CheckSeatCategoriesSellable(ctx, txq, in.SessionID, free, time.Now().UTC()); err != nil {
		return SeatedHoldResult{}, nil, err
	}
	quantity := int32(len(free)) //nolint:gosec // bounded by the request size
	if _, err := txq.ReserveCapacity(ctx, in.SessionID, nil, quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SeatedHoldResult{}, nil, &CapacityError{Requested: quantity}
		}
		return SeatedHoldResult{}, nil, fmt.Errorf("hcheckout: reserve seat capacity: %w", err)
	}
	res, err := txq.InsertReservation(ctx, in.OrgID, in.ChannelID, in.SessionID, nil, in.UserID, quantity, in.ExpiresAt)
	if err != nil {
		return SeatedHoldResult{}, nil, fmt.Errorf("hcheckout: insert reservation: %w", err)
	}
	held, err := holdLockedSeatsTx(ctx, txq, res.ID, free, newVersion)
	if err != nil {
		return SeatedHoldResult{}, nil, err
	}
	return SeatedHoldResult{Reservation: res, Seats: held}, conflicts, nil
}

// ExtendHoldPartialTx is ExtendHoldTx in partial mode for seats only, inside
// the caller's transaction. It adds the requested seats that are free to the
// open reservation and returns the others as conflicts. Seats the
// reservation already holds are neither conflicts nor added again. in.TTL
// and in.GATiers are ignored: the expiry does not move.
//
// When every requested seat is either already held by this reservation or
// not free, and at least one is not free, it returns *SeatConflictsError.
// When every requested seat is already held, it returns the reservation
// unchanged and no conflicts.
//
// Error contract: the one of ExtendHoldTx for seats.
func ExtendHoldPartialTx(ctx context.Context, txq *gen.Queries, in HoldMutationInput) (HoldMutationResult, []map[string]string, error) {
	if txq == nil {
		return HoldMutationResult{}, nil, errors.New("hcheckout: ExtendHoldPartialTx requires queries")
	}
	seatKeys, _, normErr := normalizeSeatKeys(in.SeatKeys)
	if normErr != nil || len(seatKeys) == 0 {
		return HoldMutationResult{}, nil, ErrHoldInvalidInput
	}
	res, err := lockOpenReservationTx(ctx, txq, in.ReservationID)
	if err != nil {
		return HoldMutationResult{}, nil, err
	}
	mode, err := sessionHoldMode(ctx, txq, res.SessionID)
	if err != nil {
		return HoldMutationResult{}, nil, err
	}
	if mode.AdmissionMode == admissionGeneralAdmission {
		return HoldMutationResult{}, nil, ErrHoldSeatsNotSupported
	}
	seatKeys, err = filterAlreadyHeldSeatKeys(ctx, txq, res.ID, seatKeys)
	if err != nil {
		return HoldMutationResult{}, nil, err
	}
	if len(seatKeys) == 0 {
		return HoldMutationResult{Reservation: res}, nil, nil
	}

	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, res.SessionID)
	if err != nil {
		return HoldMutationResult{}, nil, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}
	locked, err := txq.LockSessionSeatsForHold(ctx, res.SessionID, seatKeys)
	if err != nil {
		return HoldMutationResult{}, nil, fmt.Errorf("hcheckout: lock seats: %w", err)
	}
	free, conflicts := splitFreeSeats(seatKeys, locked)
	if len(free) == 0 {
		return HoldMutationResult{}, nil, &SeatConflictsError{Conflicts: conflicts}
	}
	if err := CheckSeatCategoriesSellable(ctx, txq, res.SessionID, free, in.now()); err != nil {
		return HoldMutationResult{}, nil, err
	}
	added := int32(len(free)) //nolint:gosec // bounded by the request size
	if _, err := txq.ReserveCapacity(ctx, res.SessionID, nil, added); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HoldMutationResult{}, nil, &CapacityError{Requested: added}
		}
		return HoldMutationResult{}, nil, fmt.Errorf("hcheckout: reserve extension capacity: %w", err)
	}
	held, err := holdLockedSeatsTx(ctx, txq, res.ID, free, newVersion)
	if err != nil {
		return HoldMutationResult{}, nil, err
	}
	updated, err := txq.UpdateReservationQuantity(ctx, res.ID, res.Quantity+added)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HoldMutationResult{}, nil, &NotMutableError{State: res.State, Reason: "reservation closed concurrently"}
		}
		return HoldMutationResult{}, nil, fmt.Errorf("hcheckout: grow reservation quantity: %w", err)
	}
	return HoldMutationResult{Reservation: updated, Seats: held}, conflicts, nil
}

// ReleaseHoldTx is ReleaseHold inside the caller's transaction. The caller
// must have row-locked the reservation first (the hold-mutation lock order);
// the release then bumps sessions.seat_status_version before it returns the
// capacity to inventory_ledger.
//
// Error contract: ErrHoldNotFound, *NotReleasableError.
func ReleaseHoldTx(ctx context.Context, txq *gen.Queries, reservationID uuid.UUID) (gen.ReservationRow, error) {
	if txq == nil {
		return gen.ReservationRow{}, errors.New("hcheckout: ReleaseHoldTx requires queries")
	}
	return releaseHoldTx(ctx, txq, reservationID)
}

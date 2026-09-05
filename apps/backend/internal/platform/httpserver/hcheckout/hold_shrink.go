// hold_shrink.go — W1-A5a (feature #483): the removing half of the
// mutable-hold primitives (ShrinkHold), plus RefreshHoldExpiry and
// ReacquireHold. See hold_mutation.go for the shared types, the locking
// discipline and the pricing rules.
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// ShrinkHold
// ─────────────────────────────────────────────────────────────────────────────

// ShrinkHold removes seats and/or GA quantity from an open reservation in
// its own transaction. See ShrinkHoldTx for semantics and error contract.
func ShrinkHold(ctx context.Context, pool TxStarter, q *gen.Queries, in HoldMutationInput) (HoldMutationResult, error) {
	return inHoldTx(ctx, pool, q, "shrink", func(txq *gen.Queries) (HoldMutationResult, error) {
		return ShrinkHoldTx(ctx, txq, in)
	})
}

// ShrinkHoldTx removes seats and/or GA quantity from an open reservation
// inside the caller's transaction.
//
// Sequence: lock the reservation → bump seat_status_version → release the
// requested seats ('held' → 'available', scoped by reservation_id, so a
// seat belonging to someone else is never touched) and the requested GA
// units → shrink the reservation_ga_items lines (dropping emptied ones) →
// return the freed session-level capacity → rewrite reservations.quantity.
//
// A reservation left holding nothing is transitioned to 'cancelled'
// (Cancelled=true in the result): a cart emptied by UN_RESERVE must not
// linger as an open hold.
//
// Requests to remove more than the reservation actually holds are clamped
// rather than rejected — UN_RESERVE is idempotent by design.
//
// Error contract: ErrHoldInvalidInput, ErrHoldNotFound, *NotMutableError.
func ShrinkHoldTx(ctx context.Context, txq *gen.Queries, in HoldMutationInput) (HoldMutationResult, error) {
	if txq == nil {
		return HoldMutationResult{}, errors.New("hcheckout: ShrinkHoldTx requires queries")
	}
	seatKeys, _, normErr := normalizeSeatKeys(in.SeatKeys)
	if normErr != nil {
		return HoldMutationResult{}, ErrHoldInvalidInput
	}
	gaLines, _, err := validateGATiers(in.GATiers)
	if err != nil {
		return HoldMutationResult{}, err
	}
	if len(seatKeys) == 0 && len(gaLines) == 0 {
		return HoldMutationResult{}, ErrHoldInvalidInput
	}

	res, err := lockOpenReservationTx(ctx, txq, in.ReservationID)
	if err != nil {
		return HoldMutationResult{}, err
	}
	mode, err := sessionHoldMode(ctx, txq, res.SessionID)
	if err != nil {
		return HoldMutationResult{}, err
	}

	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, res.SessionID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}

	var freed []gen.SessionSeatRow
	if len(seatKeys) > 0 {
		seats, err := releaseSeatKeysTx(ctx, txq, res, seatKeys, newVersion)
		if err != nil {
			return HoldMutationResult{}, err
		}
		freed = append(freed, seats...)
	}
	if len(gaLines) > 0 {
		units, err := shrinkGALinesTx(ctx, txq, res, gaLines, newVersion)
		if err != nil {
			return HoldMutationResult{}, err
		}
		freed = append(freed, units...)
	}

	// Plan-less GA pools stamp the tier onto units at hold time; released
	// units must rejoin the NULL-tier pool or it fragments across tiers.
	if len(freed) > 0 && mode.AdmissionMode != admissionAssignedSeats && mode.SeatingPlanVersionID == nil {
		if _, err := txq.ResetAvailableGAPoolTierStamps(ctx, res.SessionID); err != nil {
			return HoldMutationResult{}, fmt.Errorf("hcheckout: reset GA pool tier stamps: %w", err)
		}
	}

	removed := int32(len(freed)) //nolint:gosec // bounded by the reservation's own seat count

	// Emptiness is decided by what the cart still HOLDS, not by subtracting
	// from reservations.quantity. A cart whose quantity ever ran ahead of its
	// rows — a legacy pre-AB-51 GA hold, or one left behind by a partially
	// failed mutation — would otherwise never reach zero and would linger for
	// ever as an un-shrinkable open hold that owns nothing.
	remaining, err := remainingHoldSizeTx(ctx, txq, res.ID)
	if err != nil {
		return HoldMutationResult{}, err
	}

	// Hand back everything the cart no longer claims. Normally that is exactly
	// the rows just freed; when quantity ran ahead of the rows the surplus goes
	// back too, so cancelling cannot leak the phantom into the ledger.
	release := res.Quantity - remaining
	if release < removed {
		release = removed
	}
	if release > 0 {
		if _, err := txq.ReleaseCapacity(ctx, res.SessionID, nil, release); err != nil {
			return HoldMutationResult{}, fmt.Errorf("hcheckout: release shrunk capacity: %w", err)
		}
	}

	// A cart emptied by UN_RESERVE is cancelled outright: everything it
	// held is already back in inventory, so an open hold would be a lie.
	if remaining == 0 {
		if err := txq.DeleteReservationGAItems(ctx, res.ID); err != nil {
			return HoldMutationResult{}, fmt.Errorf("hcheckout: clear GA lines: %w", err)
		}
		cancelled, err := txq.UpdateReservationStateGuarded(ctx, res.ID, res.State, "cancelled")
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return HoldMutationResult{}, &NotMutableError{State: res.State, Reason: "reservation closed concurrently"}
			}
			return HoldMutationResult{}, fmt.Errorf("hcheckout: cancel emptied reservation: %w", err)
		}
		return HoldMutationResult{
			Reservation:  cancelled,
			Seats:        freed,
			LockedPrices: map[uuid.UUID]int64{},
			Cancelled:    true,
		}, nil
	}

	updated, err := txq.UpdateReservationQuantity(ctx, res.ID, remaining)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HoldMutationResult{}, &NotMutableError{State: res.State, Reason: "reservation closed concurrently"}
		}
		return HoldMutationResult{}, fmt.Errorf("hcheckout: shrink reservation quantity: %w", err)
	}
	updated, err = refreshExpiryTx(ctx, txq, updated, in)
	if err != nil {
		return HoldMutationResult{}, err
	}
	prices, err := LockedTierPrices(ctx, txq, res.ID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: read locked prices: %w", err)
	}
	return HoldMutationResult{Reservation: updated, Seats: freed, LockedPrices: prices}, nil
}

// remainingHoldSizeTx reports how many tickets a reservation still holds:
// the number of session_seats rows linked to it, or — for a legacy
// pre-AB-51 GA hold that owns no rows at all — the sum of its GA lines.
// Taking the larger of the two keeps mixed carts correct (there every
// ticket, seat or GA unit, has a row, so the link count dominates) while
// still seeing a row-less legacy hold as non-empty.
func remainingHoldSizeTx(ctx context.Context, txq *gen.Queries, reservationID uuid.UUID) (int32, error) {
	linked, err := txq.ListReservationSeats(ctx, reservationID)
	if err != nil {
		return 0, fmt.Errorf("hcheckout: count remaining seats: %w", err)
	}
	items, err := txq.ListReservationGAItems(ctx, reservationID)
	if err != nil {
		return 0, fmt.Errorf("hcheckout: count remaining GA lines: %w", err)
	}
	var gaTotal int32
	for _, it := range items {
		gaTotal += it.Quantity
	}
	rows := int32(len(linked)) //nolint:gosec // bounded by the reservation's seat count
	if gaTotal > rows {
		return gaTotal, nil
	}
	return rows, nil
}

// releaseSeatKeysTx flips the named seats of THIS reservation back to
// 'available' and drops their links. Seat keys the reservation does not
// hold are ignored (idempotent UN_RESERVE).
func releaseSeatKeysTx(
	ctx context.Context,
	txq *gen.Queries,
	res gen.ReservationRow,
	seatKeys []string,
	statusVersion int64,
) ([]gen.SessionSeatRow, error) {
	linked, err := txq.ListReservationSeats(ctx, res.ID)
	if err != nil {
		return nil, fmt.Errorf("hcheckout: list reservation seats: %w", err)
	}
	wanted := make(map[string]struct{}, len(seatKeys))
	for _, k := range seatKeys {
		wanted[k] = struct{}{}
	}
	freed := make([]gen.SessionSeatRow, 0, len(seatKeys))
	for _, s := range linked {
		if _, ok := wanted[s.SeatKey]; !ok {
			continue
		}
		row, err := txq.ReleaseSessionSeat(ctx, s.ID, res.ID, statusVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Already transitioned (e.g. concurrent sell) — drop the
				// stale link but do not count it as freed capacity.
				if _, dErr := txq.DeleteReservationSeat(ctx, res.ID, s.ID); dErr != nil {
					return nil, fmt.Errorf("hcheckout: unlink seat %s: %w", s.SeatKey, dErr)
				}
				continue
			}
			return nil, fmt.Errorf("hcheckout: release seat %s: %w", s.SeatKey, err)
		}
		if _, err := txq.DeleteReservationSeat(ctx, res.ID, s.ID); err != nil {
			return nil, fmt.Errorf("hcheckout: unlink seat %s: %w", s.SeatKey, err)
		}
		freed = append(freed, row)
	}
	return freed, nil
}

// shrinkGALinesTx releases up to Quantity GA units per tier line and
// decrements the matching reservation_ga_items row by the number of units
// actually released; a line the shrink consumed is deleted by the query
// itself (migration 0063 forbids quantity = 0).
func shrinkGALinesTx(
	ctx context.Context,
	txq *gen.Queries,
	res gen.ReservationRow,
	gaLines []HoldTierQuantity,
	statusVersion int64,
) ([]gen.SessionSeatRow, error) {
	var freed []gen.SessionSeatRow
	for i := range gaLines {
		tierID := gaLines[i].TierID
		units, err := txq.ReleaseGAUnitsForReservationTier(
			ctx, res.SessionID, res.ID, &tierID, statusVersion, gaLines[i].Quantity,
		)
		if err != nil {
			return nil, fmt.Errorf("hcheckout: release GA units: %w", err)
		}
		for _, u := range units {
			if _, err := txq.DeleteReservationSeat(ctx, res.ID, u.ID); err != nil {
				return nil, fmt.Errorf("hcheckout: unlink GA unit %s: %w", u.SeatKey, err)
			}
		}
		// Legacy pre-AB-51 reservations hold no units; their GA line is
		// still the source of truth, so shrink it by the request instead.
		dec := int32(len(units)) //nolint:gosec // bounded by the requested quantity
		if dec == 0 {
			dec = gaLines[i].Quantity
		}
		if _, err := txq.DecrementReservationGAItemQuantity(ctx, res.ID, tierID, dec); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("hcheckout: shrink GA line: %w", err)
		}
		freed = append(freed, units...)
	}
	return freed, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// RefreshHoldExpiry
// ─────────────────────────────────────────────────────────────────────────────

// RefreshHoldExpiry slides the TTL of the given open reservations to
// now+ttl in one transaction. Closed reservations are skipped silently and
// are simply absent from the returned slice, so a caller that needs to
// detect a swept cart compares len(result) with len(ids).
//
// This is the Bil24 "every RESERVE call refreshes cartTimeout" rule
// (spec §7.4) expressed as a primitive.
func RefreshHoldExpiry(
	ctx context.Context,
	pool TxStarter,
	q *gen.Queries,
	ids []uuid.UUID,
	ttl time.Duration,
) ([]gen.ReservationRow, error) {
	if pool == nil || q == nil {
		return nil, errors.New("hcheckout: RefreshHoldExpiry requires a pool and queries")
	}
	if len(ids) == 0 || ttl <= 0 {
		return nil, ErrHoldInvalidInput
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("hcheckout: begin refresh tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := RefreshHoldExpiryTx(ctx, q.WithTx(tx), ids, ttl, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("hcheckout: commit refresh: %w", err)
	}
	return rows, nil
}

// RefreshHoldExpiryTx is RefreshHoldExpiry inside the caller's transaction;
// `now` is the instant the TTL is measured from.
func RefreshHoldExpiryTx(
	ctx context.Context,
	txq *gen.Queries,
	ids []uuid.UUID,
	ttl time.Duration,
	now time.Time,
) ([]gen.ReservationRow, error) {
	if txq == nil {
		return nil, errors.New("hcheckout: RefreshHoldExpiryTx requires queries")
	}
	if len(ids) == 0 || ttl <= 0 {
		return nil, ErrHoldInvalidInput
	}
	rows, err := txq.RefreshReservationsExpiry(ctx, ids, now.UTC().Add(ttl))
	if err != nil {
		return nil, fmt.Errorf("hcheckout: refresh hold expiry: %w", err)
	}
	return rows, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ReacquireHold
// ─────────────────────────────────────────────────────────────────────────────

// ReacquireHold re-asserts the holds of an open reservation and refreshes
// its TTL. It exists because a cart's seats can be released underneath it
// (an operator freeing a seat, a partially-failed mutation): the gateway
// then needs to know, atomically, whether the cart is still whole.
//
// In one transaction it locks the reservation, locks every linked seat in
// seat_key order, re-holds the ones that fell back to 'available', and
// rejects the whole call with *SeatConflictsError when a seat was taken by
// somebody else. reservations.quantity is recomputed from the surviving
// links, and expires_at slides to now+TTL when in.TTL > 0.
//
// The reservation state machine has no edge out of converted / expired /
// cancelled, so a closed cart is never revived: the caller receives
// *NotMutableError and must start a new cart. A reservation holding no
// seats at all (legacy GA rows aside) is likewise not reacquirable.
func ReacquireHold(ctx context.Context, pool TxStarter, q *gen.Queries, in HoldMutationInput) (HoldMutationResult, error) {
	return inHoldTx(ctx, pool, q, "reacquire", func(txq *gen.Queries) (HoldMutationResult, error) {
		return ReacquireHoldTx(ctx, txq, in)
	})
}

// ReacquireHoldTx is ReacquireHold inside the caller's transaction.
func ReacquireHoldTx(ctx context.Context, txq *gen.Queries, in HoldMutationInput) (HoldMutationResult, error) {
	if txq == nil {
		return HoldMutationResult{}, errors.New("hcheckout: ReacquireHoldTx requires queries")
	}
	res, err := lockOpenReservationTx(ctx, txq, in.ReservationID)
	if err != nil {
		return HoldMutationResult{}, err
	}

	linked, err := txq.ListReservationSeats(ctx, res.ID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: list reservation seats: %w", err)
	}
	if len(linked) == 0 {
		// Legacy GA reservations (no unit rows) carry their quantity on
		// the reservation itself — there is nothing to re-assert, so the
		// call degrades to a TTL refresh.
		items, err := txq.ListReservationGAItems(ctx, res.ID)
		if err != nil {
			return HoldMutationResult{}, fmt.Errorf("hcheckout: list GA lines: %w", err)
		}
		if len(items) == 0 {
			return HoldMutationResult{}, &NotMutableError{State: res.State, Reason: "reservation holds nothing"}
		}
		refreshed, err := refreshExpiryTx(ctx, txq, res, in)
		if err != nil {
			return HoldMutationResult{}, err
		}
		prices, err := LockedTierPrices(ctx, txq, res.ID)
		if err != nil {
			return HoldMutationResult{}, fmt.Errorf("hcheckout: read locked prices: %w", err)
		}
		return HoldMutationResult{Reservation: refreshed, LockedPrices: prices}, nil
	}

	seatKeys := make([]string, 0, len(linked))
	for _, s := range linked {
		seatKeys = append(seatKeys, s.SeatKey)
	}
	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, res.SessionID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}
	locked, err := txq.LockSessionSeatsForHold(ctx, res.SessionID, seatKeys)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: lock seats: %w", err)
	}

	held := make([]gen.SessionSeatRow, 0, len(locked))
	var conflicts []map[string]string
	for _, s := range locked {
		switch {
		case s.Status == "held" && s.ReservationID != nil && *s.ReservationID == res.ID:
			// Still ours — nothing to do.
			held = append(held, s)
		case s.Status == seatStatusAvailable:
			row, hErr := txq.HoldSessionSeat(ctx, s.ID, res.ID, newVersion)
			if hErr != nil {
				if errors.Is(hErr, pgx.ErrNoRows) {
					conflicts = append(conflicts, map[string]string{"seat_key": s.SeatKey, "status": "unavailable"})
					continue
				}
				return HoldMutationResult{}, fmt.Errorf("hcheckout: re-hold seat %s: %w", s.SeatKey, hErr)
			}
			held = append(held, row)
		default:
			conflicts = append(conflicts, map[string]string{"seat_key": s.SeatKey, "status": s.Status})
		}
	}
	if len(conflicts) > 0 {
		return HoldMutationResult{}, &SeatConflictsError{Conflicts: conflicts}
	}
	if len(held) == 0 {
		// Every linked seat vanished from session_seats (a rebind wiped the
		// map underneath the cart). reservations.quantity is CHECKed > 0, so
		// there is no honest row to write — the cart must be restarted.
		return HoldMutationResult{}, &NotMutableError{State: res.State, Reason: "reservation holds nothing"}
	}

	updated := res
	if qty := int32(len(held)); qty != res.Quantity { //nolint:gosec // bounded by the reservation's seat count
		updated, err = txq.UpdateReservationQuantity(ctx, res.ID, qty)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return HoldMutationResult{}, &NotMutableError{State: res.State, Reason: "reservation closed concurrently"}
			}
			return HoldMutationResult{}, fmt.Errorf("hcheckout: recompute reservation quantity: %w", err)
		}
	}
	updated, err = refreshExpiryTx(ctx, txq, updated, in)
	if err != nil {
		return HoldMutationResult{}, err
	}
	prices, err := LockedTierPrices(ctx, txq, res.ID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: read locked prices: %w", err)
	}
	return HoldMutationResult{Reservation: updated, Seats: held, LockedPrices: prices}, nil
}

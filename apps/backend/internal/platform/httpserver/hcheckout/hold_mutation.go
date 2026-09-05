// hold_mutation.go — W1-A5a (feature #483): mutable hold primitives.
//
// The Bil24 compatibility gateway models a cart as ONE long-lived
// reservation per (gateway session, event session) whose contents change
// with every RESERVE / UN_RESERVE call (spec §7.4). The immutable
// create/release pair in hold_api.go cannot express that, so this file
// adds the four primitives the gateway needs:
//
//   - ExtendHold      — add seats / GA quantity to an open reservation.
//   - ShrinkHold      — remove seats / GA quantity; an emptied reservation
//     is cancelled (capacity fully returned).
//   - RefreshHoldExpiry — slide the TTL of open reservations (cartTimeout).
//   - ReacquireHold   — re-assert the holds of an open reservation whose
//     seats may have been released underneath it, and
//     refresh its TTL.
//
// Every primitive obeys the SEAT-C1 concurrency contract exactly as
// CreateSeatedHold does: one transaction, one monotonic
// sessions.seat_status_version bump, deterministic seat_key-ordered
// SELECT … FOR UPDATE, conditional status transitions. On top of that,
// each primitive first takes a row lock on the reservation itself
// (LockReservationForUpdate) so concurrent mutations of the SAME cart
// serialize; mutations of different carts still contend only at the seat
// level, where FOR UPDATE / SKIP LOCKED guarantee a seat or GA unit is
// never held twice.
//
// Pricing follows AB-48: a tier line added by ExtendHold locks the price
// effective at extension time, while an existing line keeps the price it
// was originally quoted. pwyw tiers have no server-side price and are
// rejected with ErrHoldPricingModeUnsupported.
//
// No gateway wiring lives here — hbil24 receives these through the
// existing shim/callback layer.
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// ErrHoldPricingModeUnsupported — a mutation targeted a pwyw tier, whose
// price is chosen by the buyer at checkout and therefore cannot be locked
// onto a reservation line.
var ErrHoldPricingModeUnsupported = errors.New("hcheckout: tier pricing mode not supported for hold mutation")

// NotMutableError reports that a mutation primitive was called on a
// reservation whose state forbids it (converted / expired / cancelled), or
// on one that no longer holds anything to re-acquire.
type NotMutableError struct {
	State  string
	Reason string
}

// Error implements the error interface.
func (e *NotMutableError) Error() string {
	if e.Reason != "" {
		return "hcheckout: reservation in state '" + e.State + "' cannot be mutated: " + e.Reason
	}
	return "hcheckout: reservation in state '" + e.State + "' cannot be mutated"
}

// HoldTierQuantity is one general-admission line of a mutation: how many
// tickets to add (ExtendHold) or remove (ShrinkHold) for a tier.
type HoldTierQuantity struct {
	TierID   uuid.UUID
	Quantity int32
}

// HoldMutationInput describes one mutation of an existing hold. SeatKeys
// and GATiers may be combined (hybrid sessions); at least one must be
// non-empty except for ReacquireHold, which ignores both.
//
// TTL > 0 slides the reservation's expires_at to Now+TTL in the same
// transaction — the Bil24 "every RESERVE refreshes cartTimeout" rule.
// Now defaults to time.Now().UTC() and is also the instant at which new
// tier prices are resolved.
type HoldMutationInput struct {
	ReservationID uuid.UUID
	SeatKeys      []string
	GATiers       []HoldTierQuantity
	TTL           time.Duration
	Now           time.Time
}

func (in HoldMutationInput) now() time.Time {
	if in.Now.IsZero() {
		return time.Now().UTC()
	}
	return in.Now.UTC()
}

// HoldMutationResult carries the reservation as it stands after the
// mutation, the seats/GA units that were added (ExtendHold, ReacquireHold)
// or released (ShrinkHold), and the per-tier locked unit prices of the
// reservation's GA lines. Cancelled is true when ShrinkHold emptied the
// reservation and transitioned it to 'cancelled'.
type HoldMutationResult struct {
	Reservation  gen.ReservationRow
	Seats        []gen.SessionSeatRow
	LockedPrices map[uuid.UUID]int64
	Cancelled    bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────────────

// holdStateMutable reports whether a reservation in the given state may
// still have its contents changed. It is the hcheckout-side mirror of the
// domain state machine (inventory.ValidReservationTransitions): exactly the
// non-terminal states — draft and active — are mutable, because a cart
// emptied by ShrinkHold must be able to reach 'cancelled', and the terminal
// states have no outgoing edge at all.
func holdStateMutable(state string) bool {
	return state == "draft" || state == "active"
}

// lockOpenReservationTx row-locks the reservation and asserts it is still
// open (draft / active). Returns ErrHoldNotFound / *NotMutableError.
func lockOpenReservationTx(ctx context.Context, txq *gen.Queries, reservationID uuid.UUID) (gen.ReservationRow, error) {
	res, err := txq.LockReservationForUpdate(ctx, reservationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ReservationRow{}, ErrHoldNotFound
		}
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: lock reservation: %w", err)
	}
	if !holdStateMutable(res.State) {
		return gen.ReservationRow{}, &NotMutableError{State: res.State, Reason: "reservation is closed"}
	}
	return res, nil
}

// validateGATiers rejects structurally invalid GA lines and aggregates
// duplicate tiers into a single line so the allocation and the
// reservation_ga_items write agree on the quantity.
func validateGATiers(lines []HoldTierQuantity) ([]HoldTierQuantity, int32, error) {
	if len(lines) == 0 {
		return nil, 0, nil
	}
	idx := make(map[uuid.UUID]int, len(lines))
	out := make([]HoldTierQuantity, 0, len(lines))
	var total int32
	for _, l := range lines {
		if l.Quantity <= 0 || l.TierID == uuid.Nil {
			return nil, 0, ErrHoldInvalidInput
		}
		total += l.Quantity
		if i, ok := idx[l.TierID]; ok {
			out[i].Quantity += l.Quantity
			continue
		}
		idx[l.TierID] = len(out)
		out = append(out, l)
	}
	return out, total, nil
}

// resolveTierUnitPrices prices the given tiers through the ONE resolver
// (priceresolve) at instant `at`. free tiers lock at 0; pwyw tiers are
// rejected — their price is the buyer's choice and cannot be snapshotted.
func resolveTierUnitPrices(
	ctx context.Context,
	txq *gen.Queries,
	sessionID uuid.UUID,
	tierIDs []uuid.UUID,
	at time.Time,
) (map[uuid.UUID]int64, error) {
	out := make(map[uuid.UUID]int64, len(tierIDs))
	if len(tierIDs) == 0 {
		return out, nil
	}
	tiers := make([]gen.TicketTierRow, 0, len(tierIDs))
	for _, tierID := range tierIDs {
		t, err := txq.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrHoldInvalidInput
			}
			return nil, fmt.Errorf("hcheckout: tier lookup: %w", err)
		}
		if t.PricingMode == "pwyw" {
			return nil, ErrHoldPricingModeUnsupported
		}
		tiers = append(tiers, t)
	}
	eff, err := priceresolve.ForTiers(ctx, txq, tiers, at)
	if err != nil {
		return nil, fmt.Errorf("hcheckout: resolve tier prices: %w", err)
	}
	for _, t := range tiers {
		unit := eff[t.ID].Amount
		if t.PricingMode == "free" {
			unit = 0
		}
		out[t.ID] = unit
	}
	return out, nil
}

// refreshExpiryTx slides the reservation's TTL when in.TTL > 0 and returns
// the (possibly updated) row.
func refreshExpiryTx(ctx context.Context, txq *gen.Queries, res gen.ReservationRow, in HoldMutationInput) (gen.ReservationRow, error) {
	if in.TTL <= 0 {
		return res, nil
	}
	rows, err := txq.RefreshReservationsExpiry(ctx, []uuid.UUID{res.ID}, in.now().Add(in.TTL))
	if err != nil {
		return res, fmt.Errorf("hcheckout: refresh hold expiry: %w", err)
	}
	if len(rows) == 0 {
		return res, &NotMutableError{State: res.State, Reason: "reservation closed concurrently"}
	}
	return rows[0], nil
}

// sessionHoldMode loads the session's admission mode (and whether its GA
// units are plan-bound, i.e. materialized per tier rather than drawn from
// the fungible NULL-tier pool).
func sessionHoldMode(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID) (gen.SessionAdmissionRow, error) {
	mode, err := txq.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.SessionAdmissionRow{}, ErrHoldSessionNotFound
		}
		return gen.SessionAdmissionRow{}, fmt.Errorf("hcheckout: admission_mode lookup: %w", err)
	}
	return mode, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ExtendHold
// ─────────────────────────────────────────────────────────────────────────────

// ExtendHold adds seats and/or GA quantity to an open reservation in its
// own transaction. See ExtendHoldTx for the semantics and error contract.
func ExtendHold(ctx context.Context, pool TxStarter, q *gen.Queries, in HoldMutationInput) (HoldMutationResult, error) {
	return inHoldTx(ctx, pool, q, "extend", func(txq *gen.Queries) (HoldMutationResult, error) {
		return ExtendHoldTx(ctx, txq, in)
	})
}

// ExtendHoldTx adds seats and/or GA quantity to an open reservation inside
// the caller's transaction (txq must already be scoped via q.WithTx).
//
// Sequence: lock the reservation → bump seat_status_version → reserve the
// added session-level capacity → hold the new seats (deterministic
// seat_key order) and allocate the new GA units → write/extend the
// reservation_ga_items price lock → grow reservations.quantity → refresh
// the TTL. Seat keys already linked to THIS reservation are ignored, which
// makes a retried RESERVE idempotent.
//
// Error contract:
//
//   - ErrHoldInvalidInput             — nothing to add / malformed input
//   - ErrHoldNotFound                 — reservation does not exist
//   - *NotMutableError                — reservation is closed
//   - ErrHoldSeatsNotSupported        — seats on a general_admission session
//   - ErrHoldQuantityNotSupported     — GA lines on an assigned_seats session
//   - ErrHoldPricingModeUnsupported   — pwyw tier
//   - *SeatConflictsError             — a requested seat is not available
//   - *CapacityError                  — inventory / GA pool over-capacity
func ExtendHoldTx(ctx context.Context, txq *gen.Queries, in HoldMutationInput) (HoldMutationResult, error) {
	if txq == nil {
		return HoldMutationResult{}, errors.New("hcheckout: ExtendHoldTx requires queries")
	}
	seatKeys, _, normErr := normalizeSeatKeys(in.SeatKeys)
	if normErr != nil {
		return HoldMutationResult{}, ErrHoldInvalidInput
	}
	gaLines, gaTotal, err := validateGATiers(in.GATiers)
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
	if len(seatKeys) > 0 && mode.AdmissionMode == admissionGeneralAdmission {
		return HoldMutationResult{}, ErrHoldSeatsNotSupported
	}
	if len(gaLines) > 0 && mode.AdmissionMode == admissionAssignedSeats {
		return HoldMutationResult{}, ErrHoldQuantityNotSupported
	}
	planBound := mode.SeatingPlanVersionID != nil

	// Drop seat keys this reservation already holds so a retried RESERVE
	// neither conflicts with itself nor double-counts quantity.
	if len(seatKeys) > 0 {
		seatKeys, err = filterAlreadyHeldSeatKeys(ctx, txq, res.ID, seatKeys)
		if err != nil {
			return HoldMutationResult{}, err
		}
	}
	added := int32(len(seatKeys)) + gaTotal //nolint:gosec // seat count bounded by request size
	if added == 0 {
		// Nothing new to hold — still honour the TTL refresh.
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

	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, res.SessionID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}

	// Capacity first — mirrors CreateSeatedHold / CreateGAHold, where an
	// over-capacity reservation must never touch seat rows.
	if _, err := txq.ReserveCapacity(ctx, res.SessionID, nil, added); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HoldMutationResult{}, &CapacityError{Requested: added}
		}
		return HoldMutationResult{}, fmt.Errorf("hcheckout: reserve extension capacity: %w", err)
	}

	var touched []gen.SessionSeatRow
	if len(seatKeys) > 0 {
		seats, err := holdSeatKeysTx(ctx, txq, res, seatKeys, newVersion)
		if err != nil {
			return HoldMutationResult{}, err
		}
		touched = append(touched, seats...)
	}
	if len(gaLines) > 0 {
		units, err := extendGALinesTx(ctx, txq, res, gaLines, planBound, newVersion, in.now())
		if err != nil {
			return HoldMutationResult{}, err
		}
		touched = append(touched, units...)
	}

	updated, err := txq.UpdateReservationQuantity(ctx, res.ID, res.Quantity+added)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HoldMutationResult{}, &NotMutableError{State: res.State, Reason: "reservation closed concurrently"}
		}
		return HoldMutationResult{}, fmt.Errorf("hcheckout: grow reservation quantity: %w", err)
	}
	updated, err = refreshExpiryTx(ctx, txq, updated, in)
	if err != nil {
		return HoldMutationResult{}, err
	}
	prices, err := LockedTierPrices(ctx, txq, res.ID)
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: read locked prices: %w", err)
	}
	return HoldMutationResult{Reservation: updated, Seats: touched, LockedPrices: prices}, nil
}

// extendGALinesTx allocates the GA units for every line and writes the
// AB-48 price lock: a NEW tier line locks the price effective at `at`, an
// EXISTING line keeps its originally quoted unit_price and only grows.
func extendGALinesTx(
	ctx context.Context,
	txq *gen.Queries,
	res gen.ReservationRow,
	gaLines []HoldTierQuantity,
	planBound bool,
	statusVersion int64,
	at time.Time,
) ([]gen.SessionSeatRow, error) {
	tierIDs := make([]uuid.UUID, 0, len(gaLines))
	for _, l := range gaLines {
		tierIDs = append(tierIDs, l.TierID)
	}
	prices, err := resolveTierUnitPrices(ctx, txq, res.SessionID, tierIDs, at)
	if err != nil {
		return nil, err
	}
	lines := make([]GAUnitLine, 0, len(gaLines))
	for i := range gaLines {
		tid := gaLines[i].TierID
		lines = append(lines, GAUnitLine{TierID: &tid, Quantity: gaLines[i].Quantity})
	}
	units, err := AllocateGAUnitsTx(ctx, txq, res.SessionID, res.ID, statusVersion, planBound, lines)
	if err != nil {
		return nil, err
	}
	for _, l := range gaLines {
		if err := txq.UpsertReservationGAItemQuantity(ctx, res.ID, l.TierID, l.Quantity, prices[l.TierID]); err != nil {
			return nil, fmt.Errorf("hcheckout: extend GA line: %w", err)
		}
	}
	return units, nil
}

// holdSeatKeysTx locks the requested seats in deterministic seat_key order
// and flips them 'available' → 'held' for the reservation, linking each.
func holdSeatKeysTx(
	ctx context.Context,
	txq *gen.Queries,
	res gen.ReservationRow,
	seatKeys []string,
	statusVersion int64,
) ([]gen.SessionSeatRow, error) {
	locked, err := txq.LockSessionSeatsForHold(ctx, res.SessionID, seatKeys)
	if err != nil {
		return nil, fmt.Errorf("hcheckout: lock seats: %w", err)
	}
	if conflicts := seatConflicts(seatKeys, locked); len(conflicts) > 0 {
		return nil, &SeatConflictsError{Conflicts: conflicts}
	}
	held := make([]gen.SessionSeatRow, 0, len(locked))
	for _, s := range locked {
		row, err := txq.HoldSessionSeat(ctx, s.ID, res.ID, statusVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, &SeatConflictsError{Conflicts: []map[string]string{
					{"seat_key": s.SeatKey, "status": "unavailable"},
				}}
			}
			return nil, fmt.Errorf("hcheckout: hold seat %s: %w", s.SeatKey, err)
		}
		if err := txq.InsertReservationSeat(ctx, res.ID, s.ID); err != nil {
			return nil, fmt.Errorf("hcheckout: link seat %s: %w", s.SeatKey, err)
		}
		held = append(held, row)
	}
	return held, nil
}

// filterAlreadyHeldSeatKeys removes seat keys already linked to this very
// reservation, making a retried extension idempotent.
func filterAlreadyHeldSeatKeys(ctx context.Context, txq *gen.Queries, reservationID uuid.UUID, seatKeys []string) ([]string, error) {
	current, err := txq.ListReservationSeats(ctx, reservationID)
	if err != nil {
		return nil, fmt.Errorf("hcheckout: list reservation seats: %w", err)
	}
	if len(current) == 0 {
		return seatKeys, nil
	}
	own := make(map[string]struct{}, len(current))
	for _, s := range current {
		own[s.SeatKey] = struct{}{}
	}
	out := make([]string, 0, len(seatKeys))
	for _, k := range seatKeys {
		if _, ok := own[k]; ok {
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

// inHoldTx runs fn inside a fresh transaction started from pool.
func inHoldTx(
	ctx context.Context,
	pool TxStarter,
	q *gen.Queries,
	label string,
	fn func(txq *gen.Queries) (HoldMutationResult, error),
) (HoldMutationResult, error) {
	if pool == nil || q == nil {
		return HoldMutationResult{}, errors.New("hcheckout: hold mutation requires a pool and queries")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: begin %s tx: %w", label, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := fn(q.WithTx(tx))
	if err != nil {
		return HoldMutationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return HoldMutationResult{}, fmt.Errorf("hcheckout: commit %s: %w", label, err)
	}
	return out, nil
}

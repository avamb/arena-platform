// hold_api.go exposes the reservation hold machinery as a plain-Go
// (non-HTTP) API so other gateway surfaces can create and release real
// holds without duplicating the SEAT-C1 concurrency contract.
//
// First consumer: the Bil24 compatibility gateway (hbil24, feature #312
// second half). Per the cross-domain rules, hbil24 never imports package
// httpserver; the parent package's bil24_shims.go injects closures over
// these functions (plus the *Server query handles) as callbacks, matching
// the PromoValidator precedent in feed_shims.go.
//
// The three entry points mirror the HTTP handlers:
//
//   - CreateSeatedHold — the seated branch of POST /v1/reservations
//     (seat_reservations.go): deterministic seat_key-ordered locking,
//     conditional 'available' → 'held' transitions stamped with one
//     monotonic seat_status_version bump, reservation_seats links, and a
//     session-level capacity reserve — all in one transaction.
//
//   - CreateGAHold — the quantity branch: per-tier capacity reserves plus
//     the reservation row and its reservation_ga_items lines (migration
//     0063) in one transaction.
//
//   - ReleaseHold — the cancel path (mirrors HandleCancelReservation):
//     releases held seats, returns reserved capacity (session-level for
//     seats, per-tier for GA lines), and transitions the reservation to
//     'cancelled'.
//
// Failure modes surface as typed errors (SeatConflictsError,
// CapacityError, NotReleasableError, and the ErrHold* sentinels) so
// callers can translate them into their own wire envelopes.
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

// ─────────────────────────────────────────────────────────────────────────────
// Typed errors
// ─────────────────────────────────────────────────────────────────────────────

// Sentinel errors returned by the hold API. Callers translate them into
// their own wire envelopes (HTTP error codes, Bil24 resultCode values).
var (
	// ErrHoldSessionNotFound — the target session does not exist or has
	// been soft-deleted.
	ErrHoldSessionNotFound = errors.New("hcheckout: hold session not found")
	// ErrHoldSeatsNotSupported — seats[] was supplied for a
	// general_admission session.
	ErrHoldSeatsNotSupported = errors.New("hcheckout: session is general_admission — seats not supported")
	// ErrHoldQuantityNotSupported — a quantity/GA payload was supplied for
	// an assigned_seats session.
	ErrHoldQuantityNotSupported = errors.New("hcheckout: session is assigned_seats — quantity not supported")
	// ErrHoldInvalidInput — structurally invalid input (empty seat set,
	// empty / duplicate seat keys, non-positive quantities).
	ErrHoldInvalidInput = errors.New("hcheckout: invalid hold input")
	// ErrHoldNotFound — ReleaseHold could not find the reservation.
	ErrHoldNotFound = errors.New("hcheckout: reservation not found")
)

// SeatConflictsError reports that one or more requested seats could not be
// held. Conflicts uses the same {"seat_key","status"} maps as the HTTP 409
// envelopes (status "unknown" for keys that did not resolve to a row).
type SeatConflictsError struct {
	Conflicts []map[string]string
}

// Error implements the error interface.
func (e *SeatConflictsError) Error() string {
	return fmt.Sprintf("hcheckout: %d seat(s) not available", len(e.Conflicts))
}

// CapacityError reports an inventory over-capacity rejection. TierID is nil
// for session-level (seated) capacity; Requested is the amount that could
// not be reserved.
type CapacityError struct {
	TierID    *uuid.UUID
	Requested int32
}

// Error implements the error interface.
func (e *CapacityError) Error() string {
	if e.TierID != nil {
		return fmt.Sprintf("hcheckout: insufficient capacity for tier %s (requested %d)", e.TierID, e.Requested)
	}
	return fmt.Sprintf("hcheckout: insufficient session capacity (requested %d)", e.Requested)
}

// NotReleasableError reports that ReleaseHold was called on a reservation
// whose state machine does not permit the transition to 'cancelled'
// (e.g. already converted, expired, or cancelled).
type NotReleasableError struct {
	State string
}

// Error implements the error interface.
func (e *NotReleasableError) Error() string {
	return "hcheckout: reservation cannot be released from state '" + e.State + "'"
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateSeatedHold
// ─────────────────────────────────────────────────────────────────────────────

// SeatedHoldInput bundles the pre-resolved identifiers for CreateSeatedHold.
// SeatKeys are the canonical session_seats.seat_key values (callers holding
// session_seat ids translate them first); ExpiresAt is the absolute hold
// deadline computed by the caller's TTL policy.
type SeatedHoldInput struct {
	OrgID     uuid.UUID
	ChannelID uuid.UUID
	SessionID uuid.UUID
	UserID    *uuid.UUID
	SeatKeys  []string
	ExpiresAt time.Time
}

// SeatedHoldResult carries the committed reservation row plus the held
// session_seats rows (post-transition, including their tier_id values) so
// the caller can price the hold without a second round-trip.
type SeatedHoldResult struct {
	Reservation gen.ReservationRow
	Seats       []gen.SessionSeatRow
}

// CreateSeatedHold creates a real seated hold following the SEAT-C1
// concurrency contract (§5.2 of the seating backlog): one transaction that
// bumps sessions.seat_status_version, locks the target seats FOR UPDATE in
// seat_key order, reserves session-level capacity, inserts the draft
// reservation, flips every seat 'available' → 'held' with the new version
// stamp, and links the seats via reservation_seats.
//
// q must expose the reservation + inventory query surface (*gen.Queries
// carries both). Error contract:
//
//   - ErrHoldInvalidInput            — empty / duplicate seat keys
//   - ErrHoldSessionNotFound         — session missing / soft-deleted
//   - ErrHoldSeatsNotSupported       — session is general_admission
//   - *SeatConflictsError            — one or more seats not available
//   - *CapacityError                 — inventory over-capacity
//
// Any other error is an infrastructure failure.
func CreateSeatedHold(ctx context.Context, pool TxStarter, q *gen.Queries, in SeatedHoldInput) (SeatedHoldResult, error) {
	if pool == nil || q == nil {
		return SeatedHoldResult{}, errors.New("hcheckout: CreateSeatedHold requires a pool and queries")
	}

	seats, _, normErr := normalizeSeatKeys(in.SeatKeys)
	if normErr != nil || len(seats) == 0 || len(seats) > int(int32Max) {
		return SeatedHoldResult{}, ErrHoldInvalidInput
	}
	quantity := int32(len(seats)) //nolint:gosec // bounded above by int32Max

	var out SeatedHoldResult
	err := retryOnSerializationFailure(ctx, holdMutationRetryAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("hcheckout: begin seated hold tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		res, txErr := createSeatedHoldTx(ctx, q.WithTx(tx), in, seats, quantity)
		if txErr != nil {
			return txErr
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("hcheckout: commit seated hold: %w", err)
		}
		out = res
		return nil
	})
	if err != nil {
		return SeatedHoldResult{}, err
	}
	return out, nil
}

// createSeatedHoldTx is CreateSeatedHold's transaction body, extracted so
// retryOnSerializationFailure can re-run the whole thing from scratch inside
// a fresh transaction on a 40P01/40001 race.
//
// Lock order (must match every other hold mutation): sessions row
// (seat_status_version bump) → session_seats rows (deterministic
// seat_key-ordered FOR UPDATE) → inventory_ledger row (ReserveCapacity).
func createSeatedHoldTx(ctx context.Context, txq *gen.Queries, in SeatedHoldInput, seats []string, quantity int32) (SeatedHoldResult, error) {
	// Admission-mode gate: seated requires assigned_seats OR hybrid.
	mode, err := txq.GetSessionAdmissionModeByID(ctx, in.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SeatedHoldResult{}, ErrHoldSessionNotFound
		}
		return SeatedHoldResult{}, fmt.Errorf("hcheckout: admission_mode lookup: %w", err)
	}
	if mode.AdmissionMode == admissionGeneralAdmission {
		return SeatedHoldResult{}, ErrHoldSeatsNotSupported
	}

	// Step 1 — bump the session's monotonic seat_status_version stamp.
	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, in.SessionID)
	if err != nil {
		return SeatedHoldResult{}, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}

	// Step 2 — deterministic seat_key-ordered SELECT … FOR UPDATE.
	locked, err := txq.LockSessionSeatsForHold(ctx, in.SessionID, seats)
	if err != nil {
		return SeatedHoldResult{}, fmt.Errorf("hcheckout: lock seats: %w", err)
	}
	if conflicts := seatConflicts(seats, locked); len(conflicts) > 0 {
		return SeatedHoldResult{}, &SeatConflictsError{Conflicts: conflicts}
	}

	// Step 3 — session-level capacity reserve (nil tier for seated holds).
	if _, err := txq.ReserveCapacity(ctx, in.SessionID, nil, quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SeatedHoldResult{}, &CapacityError{Requested: quantity}
		}
		return SeatedHoldResult{}, fmt.Errorf("hcheckout: reserve seat capacity: %w", err)
	}

	// Step 4 — insert the draft reservation.
	res, err := txq.InsertReservation(ctx, in.OrgID, in.ChannelID, in.SessionID, nil, in.UserID, quantity, in.ExpiresAt)
	if err != nil {
		return SeatedHoldResult{}, fmt.Errorf("hcheckout: insert reservation: %w", err)
	}

	// Step 5 — conditional hold + reservation_seats link per seat.
	held := make([]gen.SessionSeatRow, 0, len(locked))
	for _, s := range locked {
		row, err := txq.HoldSessionSeat(ctx, s.ID, res.ID, newVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return SeatedHoldResult{}, &SeatConflictsError{Conflicts: []map[string]string{
					{"seat_key": s.SeatKey, "status": "unavailable"},
				}}
			}
			return SeatedHoldResult{}, fmt.Errorf("hcheckout: hold seat %s: %w", s.SeatKey, err)
		}
		if err := txq.InsertReservationSeat(ctx, res.ID, s.ID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				return SeatedHoldResult{}, &SeatConflictsError{Conflicts: []map[string]string{
					{"seat_key": s.SeatKey, "status": "unavailable"},
				}}
			}
			return SeatedHoldResult{}, fmt.Errorf("hcheckout: link seat %s: %w", s.SeatKey, err)
		}
		held = append(held, row)
	}

	return SeatedHoldResult{Reservation: res, Seats: held}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateGAHold
// ─────────────────────────────────────────────────────────────────────────────

// GAHoldItem is one general-admission line of a GA hold: the target tier,
// the number of tickets, and the platform-computed per-ticket price snapshot
// persisted onto reservation_ga_items.
type GAHoldItem struct {
	TierID    uuid.UUID
	Quantity  int32
	UnitPrice int64
}

// GAHoldInput bundles the pre-resolved identifiers for CreateGAHold.
type GAHoldInput struct {
	OrgID     uuid.UUID
	ChannelID uuid.UUID
	SessionID uuid.UUID
	UserID    *uuid.UUID
	Items     []GAHoldItem
	ExpiresAt time.Time
}

// CreateGAHold creates a real general-admission hold: per-tier capacity
// reserves plus the reservation row and its reservation_ga_items lines in
// one transaction. Single-tier holds keep the reservation.tier_id column
// populated (matching the pre-0063 convention); multi-tier holds leave it
// NULL and rely on the GA lines.
//
// Error contract:
//
//   - ErrHoldInvalidInput            — no items / non-positive quantities
//   - ErrHoldSessionNotFound         — session missing / soft-deleted
//   - ErrHoldQuantityNotSupported    — session is strictly assigned_seats
//   - *CapacityError                 — per-tier over-capacity (TierID set)
func CreateGAHold(ctx context.Context, pool TxStarter, q *gen.Queries, in GAHoldInput) (gen.ReservationRow, error) {
	if pool == nil || q == nil {
		return gen.ReservationRow{}, errors.New("hcheckout: CreateGAHold requires a pool and queries")
	}
	if len(in.Items) == 0 {
		return gen.ReservationRow{}, ErrHoldInvalidInput
	}
	var totalQty int32
	for _, it := range in.Items {
		if it.Quantity <= 0 || it.UnitPrice < 0 {
			return gen.ReservationRow{}, ErrHoldInvalidInput
		}
		totalQty += it.Quantity
	}

	var out gen.ReservationRow
	err := retryOnSerializationFailure(ctx, holdMutationRetryAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("hcheckout: begin GA hold tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		res, txErr := createGAHoldTx(ctx, q.WithTx(tx), in, totalQty)
		if txErr != nil {
			return txErr
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("hcheckout: commit GA hold: %w", err)
		}
		out = res
		return nil
	})
	if err != nil {
		return gen.ReservationRow{}, err
	}
	return out, nil
}

// createGAHoldTx is CreateGAHold's transaction body, extracted so
// retryOnSerializationFailure can re-run the whole thing from scratch inside
// a fresh transaction on a 40P01/40001 race.
//
// Lock order (must match every other hold mutation, and the root cause of
// the concurrent-GA-RESERVATION deadlock this function used to trigger):
// sessions row (seat_status_version bump) FIRST, THEN the inventory_ledger
// row (ReserveCapacity). Every other hold mutation in this package already
// bumps the version before touching capacity; this one used to reserve
// capacity first, which deadlocked (40P01) against a concurrent
// ExtendHold/ShrinkHold/CreateSeatedHold on the SAME session taking the two
// locks in the opposite order. Do not reorder these two calls again.
func createGAHoldTx(ctx context.Context, txq *gen.Queries, in GAHoldInput, totalQty int32) (gen.ReservationRow, error) {
	// Admission-mode gate: quantity requires general_admission OR hybrid.
	mode, err := txq.GetSessionAdmissionModeByID(ctx, in.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ReservationRow{}, ErrHoldSessionNotFound
		}
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: admission_mode lookup: %w", err)
	}
	if mode.AdmissionMode == admissionAssignedSeats {
		return gen.ReservationRow{}, ErrHoldQuantityNotSupported
	}

	// Step 1 — bump the session's monotonic seat_status_version stamp
	// BEFORE touching inventory_ledger (global hold-mutation lock order).
	newVersion, err := txq.IncrementSessionSeatStatusVersion(ctx, in.SessionID)
	if err != nil {
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: bump seat_status_version: %w", err)
	}

	// Step 2 — AB-51: session-level capacity reserve (the same accounting
	// the seated path uses); concrete ga_unit rows are the per-tier truth.
	if _, err := txq.ReserveCapacity(ctx, in.SessionID, nil, totalQty); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ReservationRow{}, &CapacityError{Requested: totalQty}
		}
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: reserve GA capacity: %w", err)
	}

	// Single-tier holds keep reservation.tier_id populated for backward
	// compatibility with pre-0063 consumers.
	var tierIDPtr *uuid.UUID
	if len(in.Items) == 1 {
		tid := in.Items[0].TierID
		tierIDPtr = &tid
	}
	res, err := txq.InsertReservation(ctx, in.OrgID, in.ChannelID, in.SessionID, tierIDPtr, in.UserID, totalQty, in.ExpiresAt)
	if err != nil {
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: insert GA reservation: %w", err)
	}

	// Allocate concrete GA units for every line (AB-51): available ->
	// held, reservation + tier stamped, linked via reservation_seats.
	lines := make([]GAUnitLine, 0, len(in.Items))
	for i := range in.Items {
		tid := in.Items[i].TierID
		lines = append(lines, GAUnitLine{TierID: &tid, Quantity: in.Items[i].Quantity})
	}
	if _, err := AllocateGAUnitsTx(
		ctx, txq, in.SessionID, res.ID, newVersion,
		mode.SeatingPlanVersionID != nil, lines,
	); err != nil {
		return gen.ReservationRow{}, err
	}

	// Persist the per-tier GA lines (migration 0063) in the same tx —
	// they remain the pricing snapshot the checkout confirm reads.
	for _, it := range in.Items {
		if err := txq.InsertReservationGAItem(ctx, res.ID, it.TierID, it.Quantity, it.UnitPrice); err != nil {
			return gen.ReservationRow{}, fmt.Errorf("hcheckout: insert GA line: %w", err)
		}
	}

	return res, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ReleaseHold
// ─────────────────────────────────────────────────────────────────────────────

// ReleaseHold cancels a hold created by CreateSeatedHold / CreateGAHold (or
// any compatible reservation): in one transaction it flips held seats back
// to 'available' (with a seat_status_version bump), returns the reserved
// capacity (session-level for the released seats, per-tier for the GA
// lines, falling back to the reservation's own tier_id + quantity for
// legacy rows without GA lines), and transitions the reservation to
// 'cancelled'.
//
// Error contract:
//
//   - ErrHoldNotFound      — reservation does not exist
//   - *NotReleasableError  — state machine forbids the transition
func ReleaseHold(ctx context.Context, pool TxStarter, q *gen.Queries, reservationID uuid.UUID) (gen.ReservationRow, error) {
	if pool == nil || q == nil {
		return gen.ReservationRow{}, errors.New("hcheckout: ReleaseHold requires a pool and queries")
	}

	var out gen.ReservationRow
	err := retryOnSerializationFailure(ctx, holdMutationRetryAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("hcheckout: begin release tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		res, txErr := releaseHoldTx(ctx, q.WithTx(tx), reservationID)
		if txErr != nil {
			return txErr
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("hcheckout: commit release: %w", err)
		}
		out = res
		return nil
	})
	if err != nil {
		return gen.ReservationRow{}, err
	}
	return out, nil
}

// releaseHoldTx is ReleaseHold's transaction body, extracted so
// retryOnSerializationFailure can re-run the whole thing from scratch inside
// a fresh transaction on a 40P01/40001 race. Lock order: releaseReservationSeatsTx
// bumps sessions.seat_status_version before releaseHoldCapacityTx touches
// inventory_ledger — see the hold-mutation lock order note on CreateGAHold.
func releaseHoldTx(ctx context.Context, txq *gen.Queries, reservationID uuid.UUID) (gen.ReservationRow, error) {
	current, err := txq.GetReservationByID(ctx, reservationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ReservationRow{}, ErrHoldNotFound
		}
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: load reservation: %w", err)
	}
	if !isValidReservationTransition(current.State, "cancelled") {
		return gen.ReservationRow{}, &NotReleasableError{State: current.State}
	}

	// Guarded state transition: only wins when state still matches current.State.
	// Prevents double-release when a concurrent TTL-expire or cancel races
	// ReleaseHold. If pgx.ErrNoRows is returned the reservation was already
	// transitioned — surface as NotReleasableError so callers handle it. (feature #365)
	cancelled, err := txq.UpdateReservationStateGuarded(ctx, current.ID, current.State, "cancelled")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ReservationRow{}, &NotReleasableError{State: current.State + " (changed concurrently)"}
		}
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: cancel reservation: %w", err)
	}

	// Release held seats (no-op for GA reservations). Only reached after
	// winning the guarded transition above. Bumps seat_status_version BEFORE
	// releaseHoldCapacityTx touches inventory_ledger.
	released, err := releaseReservationSeatsTx(ctx, txq, current.SessionID, current.ID)
	if err != nil {
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: release seats: %w", err)
	}

	// Return reserved capacity, mirroring how it was taken. Shared with the
	// TTL worker's expireReservation so the two capacity-release paths
	// cannot drift apart.
	if err := releaseHoldCapacityTx(ctx, txq, current.SessionID, current.ID, current.TierID, current.Quantity, released); err != nil {
		return gen.ReservationRow{}, fmt.Errorf("hcheckout: %w", err)
	}

	return cancelled, nil
}

// releaseHoldCapacityTx returns previously-reserved inventory_ledger capacity
// for a cancelled/expired reservation, mirroring how CreateSeatedHold /
// CreateGAHold took it:
//
//   - session-level (nil tier) when seats or GA units were released via
//     reservation_seats (releasedSeats > 0) — every AB-51 hold lives here,
//     since seats and GA units both reserve session-level capacity;
//   - per-tier for legacy GA lines (reservation_ga_items) when the hold
//     predates GA-unit stamping (releasedSeats == 0 but lines exist);
//   - the reservation's own tier_id + quantity fallback for legacy
//     single-tier holds with neither linked seats nor GA lines.
//
// Shared by ReleaseHold (the cancel path) and the TTL worker's
// expireReservation (reservation_processor.go) so the branch logic cannot
// silently drift between the two release paths — the bug this helper fixes
// is that the TTL worker used to release capacity ONLY session-level or via
// the legacy fallback, skipping the legacy-GA-lines branch entirely.
func releaseHoldCapacityTx(ctx context.Context, q *gen.Queries, sessionID, reservationID uuid.UUID, fallbackTierID *uuid.UUID, fallbackQuantity int32, releasedSeats int) error {
	if releasedSeats > 0 {
		relQty := int32(releasedSeats) //nolint:gosec // bounded by seat count
		if _, err := q.ReleaseCapacity(ctx, sessionID, nil, relQty); err != nil {
			return fmt.Errorf("release seat capacity: %w", err)
		}
		return nil
	}

	gaItems, err := q.ListReservationGAItems(ctx, reservationID)
	if err != nil {
		return fmt.Errorf("list GA lines: %w", err)
	}
	if len(gaItems) > 0 {
		for i := range gaItems {
			it := gaItems[i]
			tierID := it.TierID
			if _, err := q.ReleaseCapacity(ctx, sessionID, &tierID, it.Quantity); err != nil {
				return fmt.Errorf("release GA capacity: %w", err)
			}
		}
		return nil
	}

	if _, err := q.ReleaseCapacity(ctx, sessionID, fallbackTierID, fallbackQuantity); err != nil {
		return fmt.Errorf("release capacity: %w", err)
	}
	return nil
}

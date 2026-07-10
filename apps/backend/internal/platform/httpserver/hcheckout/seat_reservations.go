// seat_reservations.go implements the seated branch of POST /v1/reservations
// (feature #309, Wave SEAT-C1).
//
// Concurrency contract (§5.2 of the seating backlog):
//
//  1. Deduplicate + sort seat_keys ASC — deterministic lock order avoids
//     cross-request deadlocks.
//  2. In one PostgreSQL transaction:
//     a. Increment sessions.seat_status_version → new monotonic stamp.
//     b. SELECT … FOR UPDATE on the target session_seats rows in seat_key
//     order (LockSessionSeatsForHold).
//     c. Verify every requested key was returned (missing → 409
//     reservation.seats_conflict with the unknown keys listed) and
//     every returned row has status = 'available' (non-available →
//     409 reservation.seats_conflict with the conflicting keys and
//     their current statuses).
//     d. Reserve inventory_ledger capacity for len(seats).
//     e. Insert the draft reservation.
//     f. Conditional UPDATE session_seats.status='held' + reservation_id,
//     stamped with the new status_version (HoldSessionSeat). Any 0-row
//     result rolls back the tx with a 409.
//     g. Insert one row per seat into reservation_seats. A duplicate key
//     (23505) rolls back the tx with a 409.
//  3. Commit → seats are held; TTL expiry / cancel / conversion paths take
//     over from here.
//
// A partial conflict (some seats available, some not) rolls back the whole
// transaction: the response lists ALL conflicting seat_keys so the client can
// surface them together instead of re-trying keys one by one.
package hcheckout

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// seatedReservationInput bundles the pre-parsed inputs for
// createSeatedReservation so the parent HandleCreateReservation stays lean.
type seatedReservationInput struct {
	sessionID uuid.UUID
	channelID uuid.UUID
	orgID     uuid.UUID
	tierID    *uuid.UUID
	userID    *uuid.UUID
	seats     []string
	expiresAt time.Time
}

// createSeatedReservation implements the seated branch of POST /v1/reservations.
// The public entry point HandleCreateReservation dispatches here once it has
// verified that the caller supplied seats[] (and NOT quantity) and parsed the
// common UUID / TTL fields.
func (h *Handler) createSeatedReservation(w http.ResponseWriter, r *http.Request, in seatedReservationInput) {
	ctx := r.Context()

	// Normalize: reject empty entries + trim + dedupe + sort ASC.
	seats, dupKey, dupErr := normalizeSeatKeys(in.seats)
	if dupErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.duplicate_seat", "seats[] must not contain duplicate keys", r,
			map[string]any{"seat_key": dupKey},
		))
		return
	}
	if len(seats) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_seats", "seats[] must contain at least one non-empty seat_key", r,
			map[string]any{"field": "seats"},
		))
		return
	}

	// Overflow guard for the int32 quantity column.
	if len(seats) > int(int32Max) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_seats", "seats[] size exceeds the maximum allowed", r,
			map[string]any{"field": "seats"},
		))
		return
	}
	quantity := int32(len(seats)) //nolint:gosec // bounded above by int32Max

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := h.reservationQueries.WithTx(tx)
	invQ := h.inventoryQueries.WithTx(tx)

	// Admission-mode gate: seated requires assigned_seats OR hybrid.
	mode, err := q.GetSessionAdmissionModeByID(ctx, in.sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reservation.session_not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("reservation: admission_mode lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.admission_lookup_failed", "failed to resolve session admission_mode", r,
		))
		return
	}
	if mode.AdmissionMode == admissionGeneralAdmission {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"reservation.seats_not_supported",
			"session is general_admission — pass quantity instead of seats[]",
			r,
			map[string]any{"admission_mode": mode.AdmissionMode},
		))
		return
	}

	// Step 1 — bump the session's monotonic seat_status_version stamp.
	newVersion, err := q.IncrementSessionSeatStatusVersion(ctx, in.sessionID)
	if err != nil {
		h.logger.Error("reservation: increment seat_status_version failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.status_version_failed", "failed to bump seat_status_version", r,
		))
		return
	}

	// Step 2 — deterministic seat_key-ordered SELECT … FOR UPDATE.
	locked, err := q.LockSessionSeatsForHold(ctx, in.sessionID, seats)
	if err != nil {
		h.logger.Error("reservation: lock seats failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.lock_seats_failed", "failed to lock target seats", r,
		))
		return
	}

	// Verify every requested seat_key was returned by the SELECT and every
	// returned row is currently 'available'. Both failure modes surface as
	// the same 409 with a `conflicts` array — the client can distinguish
	// unknown keys (status: "unknown") from held/sold/blocked keys.
	conflicts := seatConflicts(seats, locked)
	if len(conflicts) > 0 {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"reservation.seats_conflict",
			"one or more requested seats are not available",
			r,
			map[string]any{"conflicts": conflicts},
		))
		return
	}

	// Step 3 — reserve inventory capacity for the seated hold; over-capacity
	// still surfaces as 409 reservation.over_capacity so downstream clients
	// have a single canonical over-capacity code.
	if _, err := invQ.ReserveCapacity(ctx, in.sessionID, in.tierID, quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"reservation.over_capacity", "insufficient capacity for this reservation", r,
			))
			return
		}
		h.logger.Error("reservation: reserve capacity failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.capacity_failed", "failed to reserve capacity", r,
		))
		return
	}

	// Step 4 — insert the reservation row (draft state).
	res, err := q.InsertReservation(ctx, in.orgID, in.channelID, in.sessionID, in.tierID, in.userID, quantity, in.expiresAt)
	if err != nil {
		h.logger.Error("reservation: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.insert_failed", "failed to create reservation", r,
		))
		return
	}

	// Step 5 — conditional UPDATE + reservation_seats INSERT for every seat.
	// The rows are already locked in seat_key order, so we can walk `locked`
	// directly (LockSessionSeatsForHold ORDERs BY seat_key ASC).
	for _, s := range locked {
		held, err := q.HoldSessionSeat(ctx, s.ID, res.ID, newVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Defensive: another writer flipped the row between the
				// SELECT and the UPDATE (shouldn't happen under FOR UPDATE
				// on the same tx, but the conditional guard is still cheap
				// insurance). Surface as a partial conflict.
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.seats_conflict",
					"seat "+s.SeatKey+" is no longer available",
					r,
					map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
				))
				return
			}
			h.logger.Error("reservation: hold seat failed",
				slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.hold_failed", "failed to hold seat", r,
			))
			return
		}
		_ = held // status_version stamp is already the newVersion we passed in

		if err := q.InsertReservationSeat(ctx, res.ID, s.ID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				// A concurrent request already linked this seat to some
				// reservation. Treat as a partial-conflict rollback so the
				// caller can retry with a fresh SELECT.
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.seats_conflict",
					"seat "+s.SeatKey+" is already linked to another reservation",
					r,
					map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
				))
				return
			}
			h.logger.Error("reservation: reservation_seats insert failed",
				slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.seats_link_failed", "failed to link seat to reservation", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	// Populate the response with the resolved seat_key set for round-trip
	// convenience (matches the deterministic ASC order from the SELECT).
	resp := reservationFromRow(res)
	resp.Seats = seats

	h.logger.Info("reservation.seats.created",
		slog.String("reservation_id", res.ID.String()),
		slog.String("session_id", in.sessionID.String()),
		slog.Int("seat_count", len(seats)),
	)

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"reservation": resp,
	})
}

// normalizeSeatKeys sorts the caller-supplied seat_keys ASC, rejects empty
// strings, and returns the first duplicate key detected (so the handler can
// surface it in the 400 response). A nil slice yields (nil, "", nil).
func normalizeSeatKeys(in []string) (out []string, dupKey string, err error) {
	if len(in) == 0 {
		return nil, "", nil
	}
	seen := make(map[string]struct{}, len(in))
	out = make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			return nil, "", errEmptySeatKey
		}
		if _, ok := seen[s]; ok {
			return nil, s, errDuplicateSeatKey
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out, "", nil
}

// seatConflicts returns the list of seat_keys that either did not resolve to
// a session_seats row (status "unknown") or resolved to a row whose status
// is not 'available'. An empty slice means the caller may proceed to hold.
func seatConflicts(requested []string, locked []gen.SessionSeatRow) []map[string]string {
	byKey := make(map[string]gen.SessionSeatRow, len(locked))
	for _, s := range locked {
		byKey[s.SeatKey] = s
	}
	var out []map[string]string
	for _, k := range requested {
		row, ok := byKey[k]
		switch {
		case !ok:
			out = append(out, map[string]string{"seat_key": k, "status": "unknown"})
		case row.Status != seatStatusAvailable:
			out = append(out, map[string]string{"seat_key": k, "status": row.Status})
		}
	}
	return out
}

// rejectGAOnAssignedSeatsSession short-circuits the GA (quantity) branch when
// the target session is strictly assigned_seats. Hybrid sessions accept both
// modes and are allowed through. Sessions that do not exist / have been
// soft-deleted return errAdmissionSessionNotFound so the parent handler emits
// a 404 with the reservation.session_not_found code.
func (h *Handler) rejectGAOnAssignedSeatsSession(ctx context.Context, q *gen.Queries, sessionID uuid.UUID) error {
	mode, err := q.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errAdmissionSessionNotFound
		}
		return err
	}
	if mode.AdmissionMode == admissionAssignedSeats {
		return errAdmissionQuantityNotSupported
	}
	return nil
}

// writeAdmissionModeError translates the sentinel errors from
// rejectGAOnAssignedSeatsSession into the canonical error envelopes. Any
// non-sentinel error surfaces as a 500 with reservation.admission_lookup_failed.
func writeAdmissionModeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAdmissionSessionNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"reservation.session_not_found", "session not found", r,
		))
	case errors.Is(err, errAdmissionQuantityNotSupported):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"reservation.quantity_not_supported",
			"session is assigned_seats — pass seats[] instead of quantity",
			r,
			map[string]any{"admission_mode": admissionAssignedSeats},
		))
	default:
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.admission_lookup_failed", "failed to resolve session admission_mode", r,
		))
	}
}

// releaseReservationSeatsTx is the shared helper called from the seated-cancel
// path (HandleCancelReservation) and from the TTL worker
// (ReservationProcessor.expireReservation). It bumps seat_status_version once,
// walks every reservation_seats row for the reservation, flips each linked
// session_seat back to 'available' via the conditional ReleaseSessionSeat,
// then removes the join rows. All three writes share the caller's transaction.
//
// Callers pass a *gen.Queries already scoped to their tx (via q.WithTx(tx)).
// Errors are returned to the caller so it can decide whether to abort or
// continue — the TTL worker treats seat-release failures as non-fatal and
// still marks the reservation expired.
func releaseReservationSeatsTx(ctx context.Context, q *gen.Queries, sessionID, reservationID uuid.UUID) (int, error) {
	seats, err := q.ListReservationSeats(ctx, reservationID)
	if err != nil {
		return 0, err
	}
	if len(seats) == 0 {
		// GA reservation — nothing to release.
		return 0, nil
	}

	newVersion, err := q.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		return 0, err
	}

	released := 0
	for _, s := range seats {
		if _, err := q.ReleaseSessionSeat(ctx, s.ID, reservationID, newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Seat has already been transitioned (e.g. concurrent
				// sell). Treat as idempotent no-op and continue.
				continue
			}
			return released, err
		}
		released++
	}

	if err := q.DeleteReservationSeats(ctx, reservationID); err != nil {
		return released, err
	}
	return released, nil
}

// ─── sentinels + shared string constants ─────────────────────────────────────

var (
	errEmptySeatKey     = errors.New("hcheckout: seats[] contains an empty seat_key")
	errDuplicateSeatKey = errors.New("hcheckout: seats[] contains duplicate seat_keys")

	errAdmissionSessionNotFound      = errors.New("hcheckout: session not found")
	errAdmissionQuantityNotSupported = errors.New("hcheckout: session is assigned_seats")
)

const (
	admissionGeneralAdmission = "general_admission"
	admissionAssignedSeats    = "assigned_seats"

	seatStatusAvailable = "available"

	int32Max = 2147483647
)

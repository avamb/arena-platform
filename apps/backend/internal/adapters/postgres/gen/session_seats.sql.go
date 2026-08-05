// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: session_seats.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SessionSeatRow — shared result type for all session_seats queries
// ─────────────────────────────────────────────────────────────────────────────

// SessionSeatRow is the result type returned by every session_seats query
// (feature #305, Wave SEAT-B1).
//
// TierID is non-nil once the session's category → tier mapping has been
// applied (§7 SEAT-B2). ReservationID is non-nil while the seat is held or
// sold — it points to the reservation that currently owns the row. Status
// transitions follow: available → held → sold, with orthogonal
// available ↔ unavailable admin transitions. StatusVersion is the stamp taken
// from sessions.seat_status_version at the moment of the last transition;
// callers polling delta seat status filter on StatusVersion > cursor.
type SessionSeatRow struct {
	ID            uuid.UUID  `json:"id"`
	SessionID     uuid.UUID  `json:"session_id"`
	SeatKey       string     `json:"seat_key"`
	SectorName    string     `json:"sector_name"`
	RowName       string     `json:"row_name"`
	SeatNumber    string     `json:"seat_number"`
	TierID        *uuid.UUID `json:"tier_id"`
	Status        string     `json:"status"`
	ReservationID *uuid.UUID `json:"reservation_id"`
	StatusVersion int64      `json:"status_version"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// scanSessionSeatRow scans a single session_seats row.
func scanSessionSeatRow(row interface {
	Scan(dest ...any) error
}) (SessionSeatRow, error) {
	var s SessionSeatRow
	err := row.Scan(
		&s.ID,
		&s.SessionID,
		&s.SeatKey,
		&s.SectorName,
		&s.RowName,
		&s.SeatNumber,
		&s.TierID,
		&s.Status,
		&s.ReservationID,
		&s.StatusVersion,
		&s.UpdatedAt,
	)
	return s, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSessionSeat
// ─────────────────────────────────────────────────────────────────────────────

const insertSessionSeat = `-- name: InsertSessionSeat :one
INSERT INTO session_seats (
    session_id, seat_key, sector_name, row_name, seat_number, tier_id
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// InsertSessionSeat materializes one seat row for a session. status defaults
// to 'available' via the table default; reservation_id and status_version
// default to NULL / 0. tierID is nil until the session's category → tier
// mapping is applied.
func (q *Queries) InsertSessionSeat(
	ctx context.Context,
	sessionID uuid.UUID,
	seatKey, sectorName, rowName, seatNumber string,
	tierID *uuid.UUID,
) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, insertSessionSeat,
		sessionID, seatKey, sectorName, rowName, seatNumber, tierID,
	)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSessionSeats
// ─────────────────────────────────────────────────────────────────────────────

const insertSessionSeats = `-- name: InsertSessionSeats :execrows
INSERT INTO session_seats (
    session_id, seat_key, sector_name, row_name, seat_number, tier_id
)
SELECT $1, u.seat_key, u.sector_name, u.row_name, u.seat_number, u.tier_id::uuid
FROM unnest(
    $2::text[], $3::text[], $4::text[], $5::text[], $6::text[]
) AS u(seat_key, sector_name, row_name, seat_number, tier_id)`

// InsertSessionSeats is the batch variant of InsertSessionSeat: it
// materializes every seat of a version geometry in a single multi-row
// INSERT via parallel unnest arrays (one round-trip instead of one per
// seat). All five slices MUST have the same length; tierIDs entries are
// UUID strings and may be nil for seats without a resolved tier (they
// travel as text[] and are cast per-row, matching the promo_codes uuid[]
// text-codec precedent). Returns the number of rows inserted.
func (q *Queries) InsertSessionSeats(
	ctx context.Context,
	sessionID uuid.UUID,
	seatKeys, sectorNames, rowNames, seatNumbers []string,
	tierIDs []*string,
) (int64, error) {
	tag, err := q.db.Exec(ctx, insertSessionSeats,
		sessionID, seatKeys, sectorNames, rowNames, seatNumbers, tierIDs,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteSessionSeatsBySession
// ─────────────────────────────────────────────────────────────────────────────

const deleteSessionSeatsBySession = `-- name: DeleteSessionSeatsBySession :execrows
DELETE FROM session_seats
WHERE  session_id = $1`

// DeleteSessionSeatsBySession wipes every materialized seat for a session.
// Called on the SEAT-B2 rebind path after the zero-reservations /
// zero-tickets guardrail has passed (under the same transaction) so the
// new bind starts from a clean slate. Any reservation_seats links MUST be
// removed first via DeleteReservationSeatsBySession — session_seats is
// the FK target. Returns the number of rows deleted.
func (q *Queries) DeleteSessionSeatsBySession(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteSessionSeatsBySession, sessionID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSeatByID
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatByID = `-- name: GetSessionSeatByID :one
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  id         = $1
  AND  session_id = $2`

// GetSessionSeatByID returns a single seat scoped by session so a mismatched
// session_id yields pgx.ErrNoRows rather than leaking existence.
func (q *Queries) GetSessionSeatByID(ctx context.Context, id, sessionID uuid.UUID) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, getSessionSeatByID, id, sessionID)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSeatByKey
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatByKey = `-- name: GetSessionSeatByKey :one
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
  AND  seat_key   = $2`

// GetSessionSeatByKey resolves a caller-supplied seat_key to its
// session_seats row inside the target session. Used by the seated-checkout
// path to translate the request payload into row ids before locking.
func (q *Queries) GetSessionSeatByKey(ctx context.Context, sessionID uuid.UUID, seatKey string) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, getSessionSeatByKey, sessionID, seatKey)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSeats
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSeats = `-- name: ListSessionSeats :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
ORDER  BY seat_key ASC, id ASC`

// ListSessionSeats returns every seat for a session in canonical seat_key
// order. Powers GET_SEAT_LIST snapshots + admin surfaces.
func (q *Queries) ListSessionSeats(ctx context.Context, sessionID uuid.UUID) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeats, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var seats []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		seats = append(seats, s)
	}
	return seats, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSeatsByStatus
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSeatsByStatus = `-- name: ListSessionSeatsByStatus :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
  AND  status     = $2
ORDER  BY seat_key ASC, id ASC`

// ListSessionSeatsByStatus returns seats in a session filtered by status,
// ordered by seat_key. Uses session_seats_status_idx.
func (q *Queries) ListSessionSeatsByStatus(ctx context.Context, sessionID uuid.UUID, status string) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeatsByStatus, sessionID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var seats []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		seats = append(seats, s)
	}
	return seats, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSeatsChangedSince
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSeatsChangedSince = `-- name: ListSessionSeatsChangedSince :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id     = $1
  AND  status_version > $2
ORDER  BY status_version ASC, seat_key ASC, id ASC`

// ListSessionSeatsChangedSince returns rows whose status_version strictly
// exceeds sinceVersion. Uses session_seats_version_idx and powers the
// delta seat-status endpoints (§5.2 / §7 SEAT-B4).
func (q *Queries) ListSessionSeatsChangedSince(ctx context.Context, sessionID uuid.UUID, sinceVersion int64) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeatsChangedSince, sessionID, sinceVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var seats []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		seats = append(seats, s)
	}
	return seats, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// LockSessionSeatsForHold
// ─────────────────────────────────────────────────────────────────────────────

const lockSessionSeatsForHold = `-- name: LockSessionSeatsForHold :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
  AND  seat_key   = ANY($2::text[])
ORDER  BY seat_key ASC
FOR UPDATE`

// LockSessionSeatsForHold acquires row-level locks on the target seats in
// deterministic seat_key order and returns their current status. MUST be
// called inside a transaction — the locks release on commit / rollback.
// Callers then issue per-seat conditional UPDATE via HoldSessionSeat; any
// UPDATE returning 0 rows aborts the reservation with 409.
func (q *Queries) LockSessionSeatsForHold(ctx context.Context, sessionID uuid.UUID, seatKeys []string) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, lockSessionSeatsForHold, sessionID, seatKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var seats []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		seats = append(seats, s)
	}
	return seats, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// HoldSessionSeat
// ─────────────────────────────────────────────────────────────────────────────

const holdSessionSeat = `-- name: HoldSessionSeat :one
UPDATE session_seats
SET    status         = 'held',
       reservation_id = $2,
       status_version = $3,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'available'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// HoldSessionSeat performs the conditional 'available' -> 'held' transition
// under the seat concurrency contract. Returns pgx.ErrNoRows when the seat
// is not in 'available' state — callers MUST translate that to a 409
// conflict and abort the enclosing transaction. statusVersion is the value
// obtained from IncrementSessionSeatStatusVersion earlier in the same tx.
func (q *Queries) HoldSessionSeat(ctx context.Context, id, reservationID uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, holdSessionSeat, id, reservationID, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ReleaseSessionSeat
// ─────────────────────────────────────────────────────────────────────────────

const releaseSessionSeat = `-- name: ReleaseSessionSeat :one
UPDATE session_seats
SET    status         = 'available',
       reservation_id = NULL,
       status_version = $3,
       updated_at     = now()
WHERE  id             = $1
  AND  reservation_id = $2
  AND  status         = 'held'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// ReleaseSessionSeat performs the conditional 'held' -> 'available'
// transition scoped by reservation_id. Called from the TTL worker on
// expiry and from the checkout-cancelled path. Returns pgx.ErrNoRows for
// mismatched reservation_id / non-held seat (treated as idempotent no-op).
func (q *Queries) ReleaseSessionSeat(ctx context.Context, id, reservationID uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, releaseSessionSeat, id, reservationID, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SellSessionSeat
// ─────────────────────────────────────────────────────────────────────────────

const sellSessionSeat = `-- name: SellSessionSeat :one
UPDATE session_seats
SET    status         = 'sold',
       status_version = $3,
       updated_at     = now()
WHERE  id             = $1
  AND  reservation_id = $2
  AND  status         = 'held'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// SellSessionSeat performs the conditional 'held' -> 'sold' transition
// scoped by reservation_id. Called during ticket issuance once the
// reservation converts. reservation_id is preserved for audit / re-lookup.
func (q *Queries) SellSessionSeat(ctx context.Context, id, reservationID uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, sellSessionSeat, id, reservationID, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// BlockSessionSeat
// ─────────────────────────────────────────────────────────────────────────────

const blockSessionSeat = `-- name: BlockSessionSeat :one
UPDATE session_seats
SET    status         = 'unavailable',
       status_version = $2,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'available'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// BlockSessionSeat performs the conditional 'available' -> 'unavailable' admin
// transition. Returns pgx.ErrNoRows when the seat is not available (e.g.
// already held / sold / unavailable).
func (q *Queries) BlockSessionSeat(ctx context.Context, id uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, blockSessionSeat, id, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// UnblockSessionSeat
// ─────────────────────────────────────────────────────────────────────────────

const unblockSessionSeat = `-- name: UnblockSessionSeat :one
UPDATE session_seats
SET    status         = 'available',
       status_version = $2,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'unavailable'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// UnblockSessionSeat performs the conditional 'unavailable' -> 'available'
// admin transition. Returns pgx.ErrNoRows when the seat is not unavailable.
func (q *Queries) UnblockSessionSeat(ctx context.Context, id uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, unblockSessionSeat, id, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SetSessionSeatTier
// ─────────────────────────────────────────────────────────────────────────────

const setSessionSeatTier = `-- name: SetSessionSeatTier :one
UPDATE session_seats
SET    tier_id    = $3,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at`

// SetSessionSeatTier assigns / re-assigns a ticket_tier to a seat. Not
// gated by status because tier changes can happen before the session
// opens. Pass tierID = nil to clear the binding.
func (q *Queries) SetSessionSeatTier(ctx context.Context, id, sessionID uuid.UUID, tierID *uuid.UUID) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, setSessionSeatTier, id, sessionID, tierID)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// CountSessionSeatsByStatus
// ─────────────────────────────────────────────────────────────────────────────

const countSessionSeatsByStatus = `-- name: CountSessionSeatsByStatus :one
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  status     = $2`

// CountSessionSeatsByStatus returns the number of seats in the given
// status for a session. Uses session_seats_status_idx.
func (q *Queries) CountSessionSeatsByStatus(ctx context.Context, sessionID uuid.UUID, status string) (int64, error) {
	row := q.db.QueryRow(ctx, countSessionSeatsByStatus, sessionID, status)
	var count int64
	err := row.Scan(&count)
	return count, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionAdmissionModeByID
// ─────────────────────────────────────────────────────────────────────────────

// SessionAdmissionRow is the narrow projection returned by
// GetSessionAdmissionModeByID (feature #309, Wave SEAT-C1). Callers of the
// seated-checkout path need to know a session's admission_mode without also
// knowing its event_id.
type SessionAdmissionRow struct {
	ID                uuid.UUID `json:"id"`
	AdmissionMode     string    `json:"admission_mode"`
	SeatStatusVersion int64     `json:"seat_status_version"`
	CapacityTotal     int32     `json:"capacity_total"`
	// SeatingPlanVersionID is non-nil for plan-bound sessions (AB-51:
	// GA unit allocation filters by tier only when plan-bound).
	SeatingPlanVersionID *uuid.UUID `json:"seating_plan_version_id"`
}

const getSessionAdmissionModeByID = `-- name: GetSessionAdmissionModeByID :one
SELECT id, admission_mode, seat_status_version, capacity_total,
       seating_plan_version_id
FROM   sessions
WHERE  id         = $1
  AND  deleted_at IS NULL`

// GetSessionAdmissionModeByID resolves the admission_mode of a session so
// POST /v1/reservations can route to the GA (quantity) or seated (seats[])
// branch. Returns pgx.ErrNoRows when the session does not exist or has been
// soft-deleted.
func (q *Queries) GetSessionAdmissionModeByID(ctx context.Context, sessionID uuid.UUID) (SessionAdmissionRow, error) {
	row := q.db.QueryRow(ctx, getSessionAdmissionModeByID, sessionID)
	var r SessionAdmissionRow
	err := row.Scan(&r.ID, &r.AdmissionMode, &r.SeatStatusVersion, &r.CapacityTotal, &r.SeatingPlanVersionID)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// IncrementSessionSeatStatusVersion
// ─────────────────────────────────────────────────────────────────────────────

const incrementSessionSeatStatusVersion = `-- name: IncrementSessionSeatStatusVersion :one
UPDATE sessions
SET    seat_status_version = seat_status_version + 1,
       updated_at          = now()
WHERE  id = $1
RETURNING seat_status_version`

// IncrementSessionSeatStatusVersion atomically bumps
// sessions.seat_status_version and returns the new value. MUST be called
// at the start of every transaction that mutates session_seats.status so
// the row-level status_version stamp is monotonic (§5.2 concurrency
// contract).
func (q *Queries) IncrementSessionSeatStatusVersion(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	row := q.db.QueryRow(ctx, incrementSessionSeatStatusVersion, sessionID)
	var v int64
	err := row.Scan(&v)
	return v, err
}

// ─────────────────────────────────────────────────────────────────────────────
// AB-51: General Admission units (kind = 'ga_unit')
// ─────────────────────────────────────────────────────────────────────────────

const insertGAUnits = `-- name: InsertGAUnits :execrows
INSERT INTO session_seats
    (session_id, seat_key, sector_name, row_name, seat_number,
     tier_id, status, kind)
SELECT $1,
       $2 || '|' || lpad((gs + $3)::text, 6, '0'),
       '', '', '',
       $4::uuid,
       'available',
       'ga_unit'
FROM generate_series(1, $5::int) gs`

// InsertGAUnits materializes quantity GA units for a session under the
// given seat-key prefix ("ga|c3" for a plan category, "ga|pool" for a
// plan-less pool) starting at startIndex+1. tierID is the category tier
// for plan-bound units, nil for pool units. Returns the inserted count.
func (q *Queries) InsertGAUnits(
	ctx context.Context,
	sessionID uuid.UUID,
	keyPrefix string,
	startIndex int32,
	tierID *uuid.UUID,
	quantity int32,
) (int64, error) {
	tag, err := q.db.Exec(ctx, insertGAUnits,
		sessionID, keyPrefix, startIndex, tierID, quantity)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const allocateGAUnitsForHold = `-- name: AllocateGAUnitsForHold :many
UPDATE session_seats ss
SET    status         = 'held',
       reservation_id = $2,
       tier_id        = $3,
       status_version = $4,
       updated_at     = now()
FROM (
    SELECT id
    FROM   session_seats
    WHERE  session_id = $1
      AND  kind = 'ga_unit'
      AND  status = 'available'
      AND  tier_id IS NOT DISTINCT FROM $5::uuid
    ORDER  BY seat_key
    LIMIT  $6
    FOR UPDATE SKIP LOCKED
) picked
WHERE ss.id = picked.id
RETURNING ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
          ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
          ss.status_version, ss.updated_at`

// AllocateGAUnitsForHold atomically claims `limit` available GA units
// for a reservation (available -> held, reservation + tier stamped,
// status_version set). unitTierFilter selects the pool: the category
// tier uuid for plan-bound sessions, nil for plan-less pools (IS NOT
// DISTINCT FROM semantics). SKIP LOCKED keeps an on-sale burst from
// serializing; if fewer than limit rows return, the caller MUST roll
// back the transaction — the pool is over capacity.
func (q *Queries) AllocateGAUnitsForHold(
	ctx context.Context,
	sessionID, reservationID uuid.UUID,
	stampTierID *uuid.UUID,
	statusVersion int64,
	unitTierFilter *uuid.UUID,
	limit int32,
) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, allocateGAUnitsForHold,
		sessionID, reservationID, stampTierID, statusVersion, unitTierFilter, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const resetAvailableGAPoolTierStamps = `-- name: ResetAvailableGAPoolTierStamps :execrows
UPDATE session_seats
SET    tier_id    = NULL,
       updated_at = now()
WHERE  session_id = $1
  AND  kind = 'ga_unit'
  AND  status = 'available'
  AND  tier_id IS NOT NULL`

// ResetAvailableGAPoolTierStamps returns released plan-less pool units
// to the NULL-tier pool so the pool does not fragment across tiers.
// Idempotent; call after release/expiry on plan-less GA sessions only.
func (q *Queries) ResetAvailableGAPoolTierStamps(
	ctx context.Context, sessionID uuid.UUID,
) (int64, error) {
	tag, err := q.db.Exec(ctx, resetAvailableGAPoolTierStamps, sessionID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const countGAUnits = `-- name: CountGAUnits :one
SELECT COUNT(*) FROM session_seats
WHERE  session_id = $1 AND kind = 'ga_unit'`

// CountGAUnits returns the total GA unit count for a session.
func (q *Queries) CountGAUnits(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countGAUnits, sessionID).Scan(&n)
	return n, err
}

const deleteAvailableGAPoolUnits = `-- name: DeleteAvailableGAPoolUnits :execrows
DELETE FROM session_seats
WHERE id IN (
    SELECT id FROM session_seats
    WHERE  session_id = $1
      AND  kind = 'ga_unit'
      AND  status = 'available'
    ORDER  BY seat_key DESC
    LIMIT  $2
)`

// DeleteAvailableGAPoolUnits shrinks a plan-less pool by removing the
// highest-numbered available units. Returns the deleted count; callers
// must verify it matches the requested shrink (held/sold units are
// never deleted).
func (q *Queries) DeleteAvailableGAPoolUnits(
	ctx context.Context, sessionID uuid.UUID, limit int32,
) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteAvailableGAPoolUnits, sessionID, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const countGAUnitsHeldSoldByTier = `-- name: CountGAUnitsHeldSoldByTier :one
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  kind = 'ga_unit'
  AND  tier_id = $2
  AND  status IN ('held', 'sold')`

// CountGAUnitsHeldSoldByTier reports how many GA units a tier currently
// occupies (held or sold) — the tier-capacity guard for plan-less pools
// (AB-51).
func (q *Queries) CountGAUnitsHeldSoldByTier(ctx context.Context, sessionID, tierID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countGAUnitsHeldSoldByTier, sessionID, tierID).Scan(&n)
	return n, err
}

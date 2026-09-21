// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: session_seats.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Allowed values of session_seats.system_seat_id_source (migration 0090,
// spec §3.1). 'arena' means the id came from
// session_seats_system_id_seq; 'bil24' means it was assigned upstream and
// copied verbatim from geometry.seats[].external_id at materialisation
// so it survives a rebind (§13.2 step 6).
const (
	SeatIDSourceArena = "arena"
	SeatIDSourceBil24 = "bil24"
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
	// AB-50a (migration 0088): stable bigint identity for MACS scanning integration.
	SystemSeatID int64 `json:"system_seat_id"`
	// Kind discriminates a coordinate-bearing seat ('seat') from a General
	// Admission place ('ga_unit'). Since migration 0101 EVERY GA place
	// carries its category's tier_id, so tier_id alone no longer tells the
	// two apart — anything deciding "is this a seated hold?" must read this
	// column (hbil24.orderIsSeated).
	Kind string `json:"kind"`
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
		&s.SystemSeatID,
		&s.Kind,
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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

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
    session_id, seat_key, sector_name, row_name, seat_number, tier_id,
    system_seat_id, system_seat_id_source
)
SELECT $1, u.seat_key, u.sector_name, u.row_name, u.seat_number, u.tier_id::uuid,
       COALESCE(u.system_seat_id, nextval('session_seats_system_id_seq')),
       CASE WHEN u.system_seat_id IS NULL THEN 'arena' ELSE $8::text END
FROM unnest(
    $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::bigint[]
) AS u(seat_key, sector_name, row_name, seat_number, tier_id, system_seat_id)`

// InsertSessionSeats is the batch variant of InsertSessionSeat: it
// materializes every seat of a version geometry in a single multi-row
// INSERT via parallel unnest arrays (one round-trip instead of one per
// seat). All five slices MUST have the same length; tierIDs entries are
// UUID strings and may be nil for seats without a resolved tier (they
// travel as text[] and are cast per-row, matching the promo_codes uuid[]
// text-codec precedent). Returns the number of rows inserted.
//
// W1-C3b (§3.1 / §13.2 step 6): systemSeatIDs carries the OPTIONAL
// upstream seat identity (geometry.seats[].external_id — the Bil24
// seatId). Non-nil entries are written verbatim into
// session_seats.system_seat_id with system_seat_id_source = source, so
// the same integer survives a rebind; nil entries fall back to the arena
// sequence with source 'arena'. Pass nil to materialize a plan without
// any external ids. Callers assigning explicit ids MUST call
// AdvanceSessionSeatSystemIDSeq with the maximum id FIRST so the
// sequence can never mint a colliding value later.
func (q *Queries) InsertSessionSeats(
	ctx context.Context,
	sessionID uuid.UUID,
	seatKeys, sectorNames, rowNames, seatNumbers []string,
	tierIDs []*string,
	systemSeatIDs []*int64,
	source string,
) (int64, error) {
	// Normalise the optional array to the seat count. unnest() does pad
	// the shorter array with NULLs, but relying on that would make a nil
	// slice depend on the driver's NULL-array encoding; an explicit
	// same-length slice of nils is unambiguous.
	if len(systemSeatIDs) != len(seatKeys) {
		padded := make([]*int64, len(seatKeys))
		copy(padded, systemSeatIDs)
		systemSeatIDs = padded
	}
	if source == "" {
		source = SeatIDSourceArena
	}
	tag, err := q.db.Exec(ctx, insertSessionSeats,
		sessionID, seatKeys, sectorNames, rowNames, seatNumbers, tierIDs,
		systemSeatIDs, source,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AdvanceSessionSeatSystemIDSeq
// ─────────────────────────────────────────────────────────────────────────────

const advanceSessionSeatSystemIDSeq = `-- name: AdvanceSessionSeatSystemIDSeq :exec
SELECT setval('session_seats_system_id_seq', $1::bigint)
WHERE  $1::bigint > (SELECT last_value FROM session_seats_system_id_seq)`

// AdvanceSessionSeatSystemIDSeq pushes session_seats_system_id_seq past an
// explicitly assigned system_seat_id (W1-C3b). Materialising an imported
// Bil24 plan writes upstream seat ids verbatim; without this the arena
// sequence would eventually mint the same integer and violate the UNIQUE
// index on session_seats.system_seat_id. Idempotent and monotone — the
// sequence is only ever moved forward. Values <= 0 are a no-op.
func (q *Queries) AdvanceSessionSeatSystemIDSeq(ctx context.Context, minValue int64) error {
	if minValue <= 0 {
		return nil
	}
	_, err := q.db.Exec(ctx, advanceSessionSeatSystemIDSeq, minValue)
	return err
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
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
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
// GetSessionSeatBySystemSeatID
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatBySystemSeatID = `-- name: GetSessionSeatBySystemSeatID :one
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
FROM   session_seats
WHERE  session_id     = $1
  AND  system_seat_id = $2`

// GetSessionSeatBySystemSeatID resolves a wire seatId (spec §4 / §7.4 —
// session_seats.system_seat_id, bigint, migration 0088) to the underlying
// SessionSeatRow. Feature #476 (W1-A2b): the compat-wired RESERVATION seated
// branch calls this helper in place of GetSessionSeatByID so the seatId
// field on the wire stays int64 end-to-end. Session scope keeps
// cross-session existence from leaking (pgx.ErrNoRows when the row is
// in another session).
func (q *Queries) GetSessionSeatBySystemSeatID(ctx context.Context, sessionID uuid.UUID, systemSeatID int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, getSessionSeatBySystemSeatID, sessionID, systemSeatID)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSeatByKey
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatByKey = `-- name: GetSessionSeatByKey :one
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
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
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
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
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
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
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
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
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

// BlockSessionSeat performs the conditional 'available' -> 'unavailable' admin
// transition. Returns pgx.ErrNoRows when the seat is not available (e.g.
// already held / sold / unavailable).
func (q *Queries) BlockSessionSeat(ctx context.Context, id uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, blockSessionSeat, id, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// MarkSessionSeatSoldUpstream
// ─────────────────────────────────────────────────────────────────────────────

const markSessionSeatSoldUpstream = `-- name: MarkSessionSeatSoldUpstream :one
UPDATE session_seats
SET    status         = 'sold',
       status_version = $2,
       updated_at     = now()
WHERE  id             = $1
  AND  kind           = 'seat'
  AND  status         IN ('available', 'unavailable')
  AND  reservation_id IS NULL
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

// MarkSessionSeatSoldUpstream records a seat that was sold in the system a
// session was imported from: 'available' or 'unavailable' -> 'sold' with NO
// reservation behind it. Such a row is told apart from an arena sale by its
// NULL reservation_id. It is never on sale and the operator unblock action
// skips it like any other sold seat. Returns pgx.ErrNoRows when the seat is
// held, already sold, or not a plan seat.
func (q *Queries) MarkSessionSeatSoldUpstream(ctx context.Context, id uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, markSessionSeatSoldUpstream, id, statusVersion)
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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

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
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

// SetSessionSeatTier assigns / re-assigns a ticket_tier to a seat. Not
// gated by status because tier changes can happen before the session
// opens. Pass tierID = nil to clear the binding.
func (q *Queries) SetSessionSeatTier(ctx context.Context, id, sessionID uuid.UUID, tierID *uuid.UUID) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, setSessionSeatTier, id, sessionID, tierID)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// BulkSetSessionSeatTier  (AB-39, feature #429)
// ─────────────────────────────────────────────────────────────────────────────

const bulkSetSessionSeatTier = `-- name: BulkSetSessionSeatTier :execrows
UPDATE session_seats
SET    tier_id    = $3::uuid,
       updated_at = now()
WHERE  session_id = $1
  AND  seat_key   = ANY($2::text[])
  AND  kind       = 'seat'
  AND  status     IN ('available', 'unavailable')`

// BulkSetSessionSeatTier reassigns every seat named in seatKeys to tierID
// under session sessionID in one UPDATE. Column-side gated to
// kind='seat' in a reassignable status (available/unavailable) so a seat
// that became held/sold after the handler's pre-check is skipped rather
// than silently re-priced. Callers MUST compare the returned
// rows-affected against the intended target count and treat a mismatch
// as a concurrent-change conflict (§AB-39).
func (q *Queries) BulkSetSessionSeatTier(
	ctx context.Context,
	sessionID uuid.UUID,
	seatKeys []string,
	tierID uuid.UUID,
) (int64, error) {
	tag, err := q.db.Exec(ctx, bulkSetSessionSeatTier, sessionID, seatKeys, tierID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSeatsAdmin  (AB-39, feature #429)
// ─────────────────────────────────────────────────────────────────────────────

// SessionSeatAdminRow is a SessionSeatRow plus the kind discriminator
// ('seat' | 'ga_unit', AB-51) for surfaces that must tell physical seats
// from GA units.
type SessionSeatAdminRow struct {
	SessionSeatRow
	Kind string `json:"kind"`
}

const listSessionSeatsAdmin = `-- name: ListSessionSeatsAdmin :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
FROM   session_seats
WHERE  session_id = $1
ORDER  BY seat_key ASC, id ASC`

// ListSessionSeatsAdmin returns every session_seats row (seats AND GA
// units) for a session with the kind column, in canonical seat_key
// order. Powers the AB-39 admin seat inventory and the category
// reassignment pre-check.
func (q *Queries) ListSessionSeatsAdmin(ctx context.Context, sessionID uuid.UUID) ([]SessionSeatAdminRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeatsAdmin, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionSeatAdminRow
	for rows.Next() {
		var r SessionSeatAdminRow
		if err := rows.Scan(
			&r.ID, &r.SessionID, &r.SeatKey, &r.SectorName, &r.RowName,
			&r.SeatNumber, &r.TierID, &r.Status, &r.ReservationID,
			&r.StatusVersion, &r.UpdatedAt, &r.SystemSeatID, &r.Kind,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	// CapacityOverride is the operator knob of a plan-less session, nil
	// when none was ever set. Plan 08_architecture/23 step 3 reads it as
	// the wave-A default quantity basis for the FIRST GA category of a
	// session (the admin still sends it until wave B).
	CapacityOverride *int32 `json:"capacity_override"`
}

const getSessionAdmissionModeByID = `-- name: GetSessionAdmissionModeByID :one
SELECT id, admission_mode, seat_status_version, capacity_total,
       seating_plan_version_id, capacity_override
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
	err := row.Scan(&r.ID, &r.AdmissionMode, &r.SeatStatusVersion, &r.CapacityTotal,
		&r.SeatingPlanVersionID, &r.CapacityOverride)
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
// given seat-key prefix ("ga|c3" for a plan geometry category) starting at
// startIndex+1. tierID is the category that owns the places: since
// migration 0101 every GA place belongs to one, and a nil tier would be
// inventory no sales path can allocate. Returns the inserted count.
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
       status_version = $4,
       updated_at     = now()
FROM (
    SELECT id
    FROM   session_seats
    WHERE  session_id = $1
      AND  kind = 'ga_unit'
      AND  status = 'available'
      AND  tier_id = $3::uuid
    ORDER  BY seat_key
    LIMIT  $5
    FOR UPDATE SKIP LOCKED
) picked
WHERE ss.id = picked.id
RETURNING ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
          ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
          ss.status_version, ss.updated_at, ss.system_seat_id, ss.kind`

// AllocateGAUnitsForHold atomically claims `limit` available places OF
// ONE CATEGORY for a reservation (available -> held, reservation stamped,
// status_version set).
//
// Since migration 0101 a General Admission category OWNS its places, so
// the allocation pool is simply the category's own rows and tierID is both
// the filter and (already) the stored value — the pre-0101 fungible
// NULL-tier pool, which had to be stamped on hold and reset on release, is
// gone. SKIP LOCKED keeps an on-sale burst from serializing; if fewer than
// limit rows return, the caller MUST roll back the transaction — the
// category is sold out.
func (q *Queries) AllocateGAUnitsForHold(
	ctx context.Context,
	sessionID, reservationID, tierID uuid.UUID,
	statusVersion int64,
	limit int32,
) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, allocateGAUnitsForHold,
		sessionID, reservationID, tierID, statusVersion, limit)
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

const releaseGAUnitsForReservationTier = `-- name: ReleaseGAUnitsForReservationTier :many
UPDATE session_seats ss
SET    status         = 'available',
       reservation_id = NULL,
       status_version = $4,
       updated_at     = now()
FROM (
    SELECT id
    FROM   session_seats
    WHERE  session_id = $1
      AND  kind = 'ga_unit'
      AND  status = 'held'
      AND  reservation_id = $2
      AND  tier_id IS NOT DISTINCT FROM $3::uuid
    ORDER  BY seat_key DESC
    LIMIT  $5
    FOR UPDATE
) picked
WHERE ss.id = picked.id
RETURNING ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
          ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
          ss.status_version, ss.updated_at, ss.system_seat_id, ss.kind`

// ReleaseGAUnitsForReservationTier is the inverse of AllocateGAUnitsForHold:
// it returns up to limit GA units currently held by reservationID for tierID
// back to 'available'. Used by ShrinkHold (W1-A5a, feature #483) when a cart
// drops GA quantity. Fewer rows than limit means the reservation held fewer
// units than requested — the caller clamps. A released place keeps its
// category: since migration 0101 the category owns it. statusVersion comes
// from IncrementSessionSeatStatusVersion in the same transaction.
func (q *Queries) ReleaseGAUnitsForReservationTier(
	ctx context.Context,
	sessionID, reservationID uuid.UUID,
	tierID *uuid.UUID,
	statusVersion int64,
	limit int32,
) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, releaseGAUnitsForReservationTier,
		sessionID, reservationID, tierID, statusVersion, limit)
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

const countGAUnits = `-- name: CountGAUnits :one
SELECT COUNT(*) FROM session_seats
WHERE  session_id = $1 AND kind = 'ga_unit'`

// CountGAUnits returns the total GA unit count for a session.
func (q *Queries) CountGAUnits(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countGAUnits, sessionID).Scan(&n)
	return n, err
}

const deleteSeatRowsBySession = `-- name: DeleteSeatRowsBySession :execrows
DELETE FROM session_seats
WHERE  session_id = $1
  AND  kind       = 'seat'`

// DeleteSeatRowsBySession wipes only the coordinate-bearing seats of a
// session, leaving every kind='ga_unit' place alone. It is the rebind
// wipe of a session whose GA categories own their places (plan
// 08_architecture/23 decision 8): re-binding the SAME plan version
// re-materializes the geometry seats but must not re-mint or drop the
// category places, whose quantity an operator may have edited since.
// Any reservation_seats links MUST be removed first — session_seats is
// the FK target. Returns the number of rows deleted.
func (q *Queries) DeleteSeatRowsBySession(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteSeatRowsBySession, sessionID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AB-49: post-issuance seat release (ticket cancellation)
// ─────────────────────────────────────────────────────────────────────────────

const releaseSoldSessionSeat = `-- name: ReleaseSoldSessionSeat :one
UPDATE session_seats ss
SET    status         = 'available',
       reservation_id = NULL,
       status_version = $3,
       updated_at     = now()
WHERE  ss.session_id = $1
  AND  ss.seat_key   = $2
  AND  ss.kind       = 'seat'
  AND  ss.status     = 'sold'
  AND  NOT EXISTS (
         SELECT 1 FROM tickets t
         WHERE  t.session_id = ss.session_id
           AND  t.seat_key   = ss.seat_key
           AND  t.status     = 'active'
       )
RETURNING ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
          ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
          ss.status_version, ss.updated_at, ss.system_seat_id, ss.kind`

// ReleaseSoldSessionSeat performs the conditional 'sold' -> 'available'
// transition — the ONLY legal way a sold seat returns to sale (AB-49;
// sold -> unavailable stays forbidden). Guarded so it cannot fire while
// any ACTIVE ticket still references the seat. Returns pgx.ErrNoRows
// when the seat is not sold or an active ticket still points at it —
// the caller MUST abort the cancellation transaction on that.
func (q *Queries) ReleaseSoldSessionSeat(ctx context.Context, sessionID uuid.UUID, seatKey string, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, releaseSoldSessionSeat, sessionID, seatKey, statusVersion)
	return scanSessionSeatRow(row)
}

const releaseSoldGAUnitForReservation = `-- name: ReleaseSoldGAUnitForReservation :one
UPDATE session_seats ss
SET    status         = 'available',
       reservation_id = NULL,
       status_version = $4,
       updated_at     = now()
FROM (
    SELECT id
    FROM   session_seats
    WHERE  session_id = $1
      AND  kind = 'ga_unit'
      AND  status = 'sold'
      AND  reservation_id = $2
      AND  tier_id IS NOT DISTINCT FROM $3::uuid
    ORDER  BY seat_key DESC
    LIMIT  1
    FOR UPDATE SKIP LOCKED
) picked
WHERE ss.id = picked.id
RETURNING ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
          ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
          ss.status_version, ss.updated_at, ss.system_seat_id, ss.kind`

// ReleaseSoldGAUnitForReservation releases exactly ONE sold GA unit of
// the cancelled ticket's reservation + tier (units are fungible within
// a tier; GA tickets carry no seat_key). Returns pgx.ErrNoRows for
// legacy pre-AB-51 reservations without unit rows — callers treat that
// as ledger-only restore, not an error.
func (q *Queries) ReleaseSoldGAUnitForReservation(ctx context.Context, sessionID, reservationID uuid.UUID, tierID *uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, releaseSoldGAUnitForReservation, sessionID, reservationID, tierID, statusVersion)
	return scanSessionSeatRow(row)
}

const releaseSoldGAUnitBySeatKey = `-- name: ReleaseSoldGAUnitBySeatKey :one
UPDATE session_seats ss
SET    status         = 'available',
       reservation_id = NULL,
       status_version = $3,
       updated_at     = now()
WHERE  ss.session_id = $1
  AND  ss.seat_key   = $2
  AND  ss.kind       = 'ga_unit'
  AND  ss.status     = 'sold'
  AND  NOT EXISTS (
         SELECT 1 FROM tickets t
         WHERE  t.session_id = ss.session_id
           AND  t.seat_key   = ss.seat_key
           AND  t.status     = 'active'
       )
RETURNING ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
          ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
          ss.status_version, ss.updated_at, ss.system_seat_id, ss.kind`

// ReleaseSoldGAUnitBySeatKey is the ga_unit twin of ReleaseSoldSessionSeat.
// Since AB-51 issuance stamps the concrete unit's seat_key on the GA
// ticket, so a cancellation releases exactly THAT unit, guarded the same
// way (sold, and no other ACTIVE ticket still references it). Returns
// pgx.ErrNoRows when the unit is not sold or still referenced.
func (q *Queries) ReleaseSoldGAUnitBySeatKey(ctx context.Context, sessionID uuid.UUID, seatKey string, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, releaseSoldGAUnitBySeatKey, sessionID, seatKey, statusVersion)
	return scanSessionSeatRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// CountSessionSeatsByTier (AB-48)
// ─────────────────────────────────────────────────────────────────────────────

// SessionSeatTierCountRow is one (tier, kind) inventory count.
type SessionSeatTierCountRow struct {
	TierID    uuid.UUID `json:"tier_id"`
	Kind      string    `json:"kind"`
	Count     int64     `json:"count"`
	Held      int64     `json:"held"`
	Sold      int64     `json:"sold"`
	Available int64     `json:"available"`
}

const countSessionSeatsByTier = `-- name: CountSessionSeatsByTier :many
SELECT tier_id, kind, COUNT(*)::bigint AS count,
       COUNT(*) FILTER (WHERE status = 'held')::bigint      AS held,
       COUNT(*) FILTER (WHERE status = 'sold')::bigint      AS sold,
       COUNT(*) FILTER (WHERE status = 'available')::bigint AS available
FROM   session_seats
WHERE  session_id = $1
  AND  tier_id IS NOT NULL
GROUP  BY tier_id, kind`

// CountSessionSeatsByTier returns per-(tier, kind) counts of materialized
// rows for a session — the seat count / GA capacity shown beside each
// category price (AB-48 step 3).
func (q *Queries) CountSessionSeatsByTier(ctx context.Context, sessionID uuid.UUID) ([]SessionSeatTierCountRow, error) {
	rows, err := q.db.Query(ctx, countSessionSeatsByTier, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSeatTierCountRow
	for rows.Next() {
		var r SessionSeatTierCountRow
		if err := rows.Scan(&r.TierID, &r.Kind, &r.Count, &r.Held, &r.Sold, &r.Available); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

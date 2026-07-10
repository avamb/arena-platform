-- session_seats.sql — sqlc-style query definitions for the session_seats
-- table (feature #305, Wave SEAT-B1). Companion hand-written gen file:
--   apps/backend/internal/adapters/postgres/gen/session_seats.sql.go
--
-- The seat concurrency contract is enforced app-side (see §5.2 of
-- 09_autoforge/seating_backlog.md):
--
--   * Holds acquire target rows via SELECT … FOR UPDATE in seat_key
--     ORDER — deterministic lock order → no deadlocks.
--   * Every status transition (hold / release / sell / block /
--     unblock) increments sessions.seat_status_version FIRST, then
--     stamps the affected session_seats rows with the new value in
--     their status_version column.
--   * The conditional UPDATE … WHERE status = <expected> is the
--     canonical guard against lost-update races; a 0-row result
--     MUST abort the enclosing transaction.

-- name: InsertSessionSeat :one
-- Materializes one seat for a session. Called from the SEAT-B2
-- provisioning path (once per seat in the version geometry).
-- reservation_id is NULL and status defaults to 'available' via the
-- table default; callers pass tier_id = nil until the category ->
-- tier mapping is applied.
INSERT INTO session_seats (
    session_id, seat_key, sector_name, row_name, seat_number, tier_id
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: GetSessionSeatByID :one
-- Fetches a single seat by id, scoped to its session so a caller with
-- a mismatched session_id receives pgx.ErrNoRows instead of leaking
-- cross-session existence.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  id         = $1
  AND  session_id = $2;

-- name: GetSessionSeatByKey :one
-- Fetches a single seat by (session_id, seat_key). Used by the
-- seated-checkout path to translate caller-supplied seat_keys into
-- session_seats.id values before locking them.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
  AND  seat_key   = $2;

-- name: ListSessionSeats :many
-- Returns every seat for a session in canonical seat_key order. Used
-- to render GET_SEAT_LIST / seat_status_url snapshots and admin
-- surfaces. The status_idx / version_idx do NOT cover this query;
-- callers who need paginated status-filtered walks should use
-- ListSessionSeatsByStatus below.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
ORDER  BY seat_key ASC, id ASC;

-- name: ListSessionSeatsByStatus :many
-- Returns seats in a session filtered by status, ordered by seat_key.
-- Uses session_seats_status_idx.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
  AND  status     = $2
ORDER  BY seat_key ASC, id ASC;

-- name: ListSessionSeatsChangedSince :many
-- Returns seats whose status_version is strictly greater than $2 for
-- the given session. Ordered by (status_version, seat_key) so callers
-- iterating page-by-page get deterministic paging behaviour. Powers
-- delta seat-status endpoints (§5.2 / §7 SEAT-B4).
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id     = $1
  AND  status_version > $2
ORDER  BY status_version ASC, seat_key ASC, id ASC;

-- name: LockSessionSeatsForHold :many
-- Acquires row-level locks on the target seats in deterministic
-- seat_key order and returns their current status. MUST be called
-- inside a transaction. Caller then issues per-seat conditional
-- UPDATEs; any UPDATE returning 0 rows aborts the reservation.
-- Uses session_seats.UNIQUE(session_id, seat_key).
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  session_id = $1
  AND  seat_key   = ANY($2::text[])
ORDER  BY seat_key ASC
FOR UPDATE;

-- name: HoldSessionSeat :one
-- Conditional 'available' -> 'held' transition. Stamps the row with
-- the new sessions.seat_status_version passed by the caller (already
-- incremented in the same transaction). Returns pgx.ErrNoRows if the
-- seat is not available — the caller MUST treat that as a conflict
-- (409) and abort the reservation.
UPDATE session_seats
SET    status         = 'held',
       reservation_id = $2,
       status_version = $3,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'available'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: ReleaseSessionSeat :one
-- Conditional 'held' -> 'available' transition scoped by
-- reservation_id, so releasing another reservation's hold is a
-- no-op (pgx.ErrNoRows). Called from the TTL worker and from the
-- checkout-cancelled path.
UPDATE session_seats
SET    status         = 'available',
       reservation_id = NULL,
       status_version = $3,
       updated_at     = now()
WHERE  id             = $1
  AND  reservation_id = $2
  AND  status         = 'held'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: SellSessionSeat :one
-- Conditional 'held' -> 'sold' transition scoped by reservation_id.
-- Called during ticket issuance once the reservation converts.
-- reservation_id is intentionally preserved for audit / re-lookup.
UPDATE session_seats
SET    status         = 'sold',
       status_version = $3,
       updated_at     = now()
WHERE  id             = $1
  AND  reservation_id = $2
  AND  status         = 'held'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: BlockSessionSeat :one
-- Conditional 'available' -> 'blocked' transition. Admin block
-- (§7 SEAT-B3). Returns pgx.ErrNoRows if the seat is not available.
UPDATE session_seats
SET    status         = 'blocked',
       status_version = $2,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'available'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: UnblockSessionSeat :one
-- Conditional 'blocked' -> 'available' transition. Admin unblock.
UPDATE session_seats
SET    status         = 'available',
       status_version = $2,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'blocked'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: SetSessionSeatTier :one
-- Assigns / re-assigns a ticket_tier to a seat. Called from the
-- SEAT-B2 category-mapping path; not gated by status because tier
-- changes can happen before the session opens.
UPDATE session_seats
SET    tier_id    = $3,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: CountSessionSeatsByStatus :one
-- Returns the number of seats in the given status for a session.
-- Uses session_seats_status_idx.
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  status     = $2;

-- name: GetSessionAdmissionModeByID :one
-- Returns the admission_mode + seat_status_version + capacity_total for a
-- session, without requiring the caller to know its event_id. Used by the
-- seated-checkout path (§7 SEAT-C1) to decide whether a POST /v1/reservations
-- request should route down the GA (quantity) branch or the seated (seats[])
-- branch. Returns pgx.ErrNoRows if the session does not exist or has been
-- soft-deleted.
SELECT id, admission_mode, seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  deleted_at IS NULL;

-- name: IncrementSessionSeatStatusVersion :one
-- Atomically bumps sessions.seat_status_version and returns the new
-- value. MUST be called at the start of every transaction that
-- mutates session_seats.status so the row-level status_version stamp
-- is monotonic.
UPDATE sessions
SET    seat_status_version = seat_status_version + 1,
       updated_at          = now()
WHERE  id = $1
RETURNING seat_status_version;

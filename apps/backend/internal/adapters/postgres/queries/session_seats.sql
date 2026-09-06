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

-- name: InsertSessionSeats :execrows
-- Batch variant of InsertSessionSeat: materializes every seat of a
-- version geometry in a single multi-row INSERT via parallel unnest
-- arrays (one round-trip instead of one per seat). All five arrays MUST
-- have the same length; tier_ids entries are UUID strings and may be
-- NULL for seats without a resolved tier (they travel as text[] and are
-- cast per-row, matching the promo_codes uuid[] text-codec precedent).
-- Same column defaults as InsertSessionSeat (status 'available',
-- reservation_id NULL, status_version 0).
--
-- W1-C3b (§3.1 / §13.2 step 6): system_seat_ids carries the OPTIONAL
-- upstream seat identity (geometry.seats[].external_id — the Bil24
-- seatId). A non-NULL entry is written verbatim into
-- session_seats.system_seat_id with system_seat_id_source = $8, so the
-- very same integer keeps addressing the seat across a rebind. A NULL
-- entry falls back to the arena sequence with source 'arena'. Callers
-- passing explicit ids MUST first run AdvanceSessionSeatSystemIDSeq so
-- the sequence can never later mint a colliding value.
INSERT INTO session_seats (
    session_id, seat_key, sector_name, row_name, seat_number, tier_id,
    system_seat_id, system_seat_id_source
)
SELECT $1, u.seat_key, u.sector_name, u.row_name, u.seat_number, u.tier_id::uuid,
       COALESCE(u.system_seat_id, nextval('session_seats_system_id_seq')),
       CASE WHEN u.system_seat_id IS NULL THEN 'arena' ELSE $8::text END
FROM unnest(
    $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::bigint[]
) AS u(seat_key, sector_name, row_name, seat_number, tier_id, system_seat_id);

-- name: AdvanceSessionSeatSystemIDSeq :exec
-- Pushes session_seats_system_id_seq past an explicitly assigned
-- system_seat_id (W1-C3b). Materialisation of an imported Bil24 plan
-- writes upstream seat ids verbatim; without this the arena sequence
-- would eventually mint the same integer and violate the UNIQUE index
-- on session_seats.system_seat_id. Idempotent and monotone: the
-- sequence is only ever moved forward, never back.
SELECT setval('session_seats_system_id_seq', $1::bigint)
WHERE  $1::bigint > (SELECT last_value FROM session_seats_system_id_seq);

-- name: DeleteSessionSeatsBySession :execrows
-- Wipes every materialized seat for a session. Called on the SEAT-B2
-- rebind path after the zero-reservations / zero-tickets guardrail has
-- passed (under the same transaction) so the new bind starts from a
-- clean slate. Any reservation_seats links MUST be removed first via
-- DeleteReservationSeatsBySession — session_seats is the FK target.
DELETE FROM session_seats
WHERE  session_id = $1;

-- name: GetSessionSeatByID :one
-- Fetches a single seat by id, scoped to its session so a caller with
-- a mismatched session_id receives pgx.ErrNoRows instead of leaking
-- cross-session existence.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at
FROM   session_seats
WHERE  id         = $1
  AND  session_id = $2;

-- name: GetSessionSeatBySystemSeatID :one
-- Fetches a single seat by (session_id, system_seat_id). Feature #476
-- (Bil24 compat W1-A2b): the seatId field on the wire is
-- session_seats.system_seat_id (bigint, migration 0088) so the
-- RESERVATION seated branch reverse-maps int64 → SessionSeatRow in one
-- round-trip. Session scope avoids leaking cross-session existence.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id
FROM   session_seats
WHERE  session_id     = $1
  AND  system_seat_id = $2;

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
-- Conditional 'available' -> 'unavailable' transition. Admin withhold
-- (§7 SEAT-B3). Returns pgx.ErrNoRows if the seat is not available.
UPDATE session_seats
SET    status         = 'unavailable',
       status_version = $2,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'available'
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at;

-- name: UnblockSessionSeat :one
-- Conditional 'unavailable' -> 'available' transition. Admin release.
UPDATE session_seats
SET    status         = 'available',
       status_version = $2,
       updated_at     = now()
WHERE  id     = $1
  AND  status = 'unavailable'
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

-- name: BulkSetSessionSeatTier :execrows
-- AB-39: bulk assign one tier_id to every seat in seat_keys[] for a session.
-- Gated column-side to the reassignable states so a seat that became
-- held/sold between the handler's pre-check and this UPDATE is skipped
-- rather than silently re-priced (TOCTOU guard; the handler compares
-- rows-affected against the target count and 409s on a mismatch).
-- kind='seat' keeps AB-51 GA units out — GA categories have no seats to
-- assign (AB-40 Part C) and re-tiering a unit corrupts pool accounting.
UPDATE session_seats
SET    tier_id    = $3::uuid,
       updated_at = now()
WHERE  session_id = $1
  AND  seat_key   = ANY($2::text[])
  AND  kind       = 'seat'
  AND  status     IN ('available', 'unavailable');

-- name: ListSessionSeatsAdmin :many
-- AB-39 admin seat inventory: same shape as ListSessionSeats plus kind,
-- so the admin table and the category-reassignment pre-check can tell
-- physical seats from AB-51 GA units.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, kind
FROM   session_seats
WHERE  session_id = $1
ORDER  BY seat_key ASC, id ASC;

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
SELECT id, admission_mode, seat_status_version, capacity_total,
       seating_plan_version_id
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

-- ─────────────────────────────────────────────────────────────────────
-- AB-51: General Admission units (kind = 'ga_unit')
-- ─────────────────────────────────────────────────────────────────────

-- name: InsertGAUnits :execrows
-- Materializes `quantity` GA units for a session under the given key
-- prefix ("ga|c3" / "ga|pool") starting at start_index+1. tier_id is
-- the category tier for plan-bound units, NULL for pool units.
INSERT INTO session_seats
    (session_id, seat_key, sector_name, row_name, seat_number,
     tier_id, status, kind)
SELECT $1,
       $2 || '|' || lpad((gs + $3)::text, 6, '0'),
       '', '', '',
       $4::uuid,
       'available',
       'ga_unit'
FROM generate_series(1, $5::int) gs;

-- name: AllocateGAUnitsForHold :many
-- Atomically claims `limit` available GA units for a reservation:
-- status available -> held, reservation stamped, tier stamped (no-op
-- for plan-bound units whose tier already matches; stamps pool units so
-- ticket issuance knows the line tier). tier filter: IS NOT DISTINCT
-- FROM so NULL selects pool units. SKIP LOCKED keeps an on-sale burst
-- from serializing on row locks; a short allocation means over-capacity
-- and the caller must roll back.
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
          ss.status_version, ss.updated_at;

-- name: ResetAvailableGAPoolTierStamps :execrows
-- Plan-less GA sessions treat units as a fungible pool: a released unit
-- must return to the NULL-tier pool or the pool fragments across tiers.
-- Safe to run after any release/expiry on a plan-less session.
UPDATE session_seats
SET    tier_id    = NULL,
       updated_at = now()
WHERE  session_id = $1
  AND  kind = 'ga_unit'
  AND  status = 'available'
  AND  tier_id IS NOT NULL;

-- name: CountGAUnits :one
SELECT COUNT(*) FROM session_seats
WHERE  session_id = $1 AND kind = 'ga_unit';

-- name: ReleaseGAUnitsForReservationTier :many
-- W1-A5a (feature #483): the inverse of AllocateGAUnitsForHold — returns
-- up to $5 GA units currently held by reservation $2 for tier $3 back to
-- 'available'. Used by ShrinkHold when a cart drops GA quantity. The inner
-- SELECT takes FOR UPDATE (no SKIP LOCKED: these rows belong to the caller's
-- own reservation, which is already row-locked, so no other transaction may
-- legitimately be mutating them). Plan-less pools additionally need
-- ResetAvailableGAPoolTierStamps afterwards so units rejoin the NULL pool.
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
          ss.status_version, ss.updated_at, ss.system_seat_id;

-- name: DeleteAvailableGAPoolUnits :execrows
-- Shrinks a plan-less GA session's pool by removing the highest-
-- numbered AVAILABLE units. Held/sold units are never touched — the
-- ledger's UpdateCapacityTotal guard already refuses a total below
-- held+sold.
DELETE FROM session_seats
WHERE id IN (
    SELECT id FROM session_seats
    WHERE  session_id = $1
      AND  kind = 'ga_unit'
      AND  status = 'available'
    ORDER  BY seat_key DESC
    LIMIT  $2
);

-- name: CountGAUnitsHeldSoldByTier :one
-- AB-51: tier-capacity guard for plan-less GA pools — how many units a
-- tier currently occupies (held or sold).
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  kind = 'ga_unit'
  AND  tier_id = $2
  AND  status IN ('held', 'sold');

-- ─────────────────────────────────────────────────────────────────────
-- AB-49: post-issuance seat release (ticket cancellation)
-- ─────────────────────────────────────────────────────────────────────

-- name: ReleaseSoldSessionSeat :one
-- Conditional 'sold' -> 'available' transition — the ONLY legal way a
-- sold seat returns to sale, and only via ticket cancellation/refund
-- (AB-49 transition table; sold -> unavailable stays forbidden).
-- Guarded so it cannot fire while any ACTIVE ticket still references
-- the seat: several ticket rows may exist for one seat over a session's
-- life (sold -> cancelled -> sold again), but only one is valid at a
-- time. Clears reservation_id and stamps the caller's freshly bumped
-- seat_status_version. Returns pgx.ErrNoRows when the seat is not sold
-- or an active ticket still points at it — the caller MUST treat that
-- as an inconsistency and abort the cancellation transaction.
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
          ss.status_version, ss.updated_at;

-- name: ReleaseSoldGAUnitForReservation :one
-- AB-49 GA counterpart of ReleaseSoldSessionSeat: releases exactly ONE
-- sold GA unit belonging to the cancelled ticket's reservation and tier
-- (units are fungible within a tier — GA tickets carry no seat_key).
-- tier filter is IS NOT DISTINCT FROM so pool units (NULL tier before
-- allocation stamping) and plan-bound units both resolve. Returns
-- pgx.ErrNoRows for legacy pre-AB-51 reservations that never had unit
-- rows — the caller treats that as "ledger-only" restore, not an error.
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
          ss.status_version, ss.updated_at;

-- name: CountSessionSeatsByTier :many
-- AB-48 step 3: per-tier inventory counts for the price forms ("Third:
-- EUR 30 · Seats: 260"). Physical seats and GA units are reported
-- separately; rows with NULL tier are skipped.
SELECT tier_id, kind, COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  tier_id IS NOT NULL
GROUP  BY tier_id, kind;

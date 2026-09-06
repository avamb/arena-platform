-- reservations.sql — typed sqlc queries for the reservations table.
-- Feature #131 — Wave 5 Inventory & Reservations.
--
-- The state machine is: draft → active → converted|expired|cancelled.
-- GetExpiredReservations uses FOR UPDATE SKIP LOCKED so concurrent TTL worker
-- instances never double-process the same reservation.

-- name: InsertReservation :one
-- Creates a new reservation in 'draft' state for the given session/tier.
-- expires_at is computed by the caller based on org/channel TTL settings.
-- Returns the full row including the uuidv7 PK assigned by the database.
INSERT INTO reservations (org_id, channel_id, session_id, tier_id, user_id, quantity, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
          expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at;

-- name: GetReservationByID :one
-- Fetches a single reservation by its UUID primary key.
-- Returns pgx.ErrNoRows when not found.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  id = $1;

-- name: UpdateReservationState :one
-- Transitions the reservation to a new state and sets the appropriate timestamp.
-- Sets cancelled_at, converted_at, or expired_at depending on the new state.
-- Returns pgx.ErrNoRows when the reservation does not exist.
UPDATE reservations
SET    state        = $2,
       updated_at   = now(),
       cancelled_at = CASE WHEN $2 = 'cancelled' THEN now() ELSE cancelled_at END,
       converted_at = CASE WHEN $2 = 'converted' THEN now() ELSE converted_at END,
       expired_at   = CASE WHEN $2 = 'expired'   THEN now() ELSE expired_at   END
WHERE  id = $1
RETURNING id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
          expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at;

-- name: UpdateReservationStateGuarded :one
-- Conditionally transitions the reservation only when the current state matches
-- expectedState ($2). Returns pgx.ErrNoRows when the row does not exist OR the
-- state guard failed (another transition already won the race). Callers MUST
-- treat pgx.ErrNoRows as a signal to skip any side-effects (e.g. capacity
-- release) — they did not win the transition. Used to prevent double-release
-- when cancel and TTL-expire execute concurrently (feature #365).
UPDATE reservations
SET    state        = $3,
       updated_at   = now(),
       cancelled_at = CASE WHEN $3 = 'cancelled' THEN now() ELSE cancelled_at END,
       converted_at = CASE WHEN $3 = 'converted' THEN now() ELSE converted_at END,
       expired_at   = CASE WHEN $3 = 'expired'   THEN now() ELSE expired_at   END
WHERE  id = $1
  AND  state = $2
RETURNING id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
          expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at;

-- name: GetExpiredReservations :many
-- Polls up to $1 reservations whose TTL has elapsed but have not yet been
-- marked expired. Uses FOR UPDATE SKIP LOCKED so concurrent TTL worker
-- instances skip rows already being processed by another worker.
-- Must be called inside a transaction.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  state IN ('draft', 'active')
  AND  expires_at < now()
ORDER BY expires_at ASC
LIMIT  $1
FOR UPDATE SKIP LOCKED;

-- name: ListReservationsBySession :many
-- Lists all reservations for the given session, newest first.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  session_id = $1
ORDER BY created_at DESC, id DESC;

-- name: CountReservationsBySession :one
-- CountReservationsBySession returns the number of reservations attached to
-- a session. Powers the seating-plan rebind gate (feature #306, Wave SEAT-B2):
-- a rebind is rejected with 409 when this count is non-zero, so any
-- historical, cancelled, or expired reservation locks the current binding.
SELECT COUNT(*)::bigint AS count
FROM   reservations
WHERE  session_id = $1;

-- name: ListReservationsByUser :many
-- Lists all reservations for the given user, newest first.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  user_id = $1
ORDER BY created_at DESC, id DESC;

-- name: LockReservationForUpdate :one
-- W1-A5a (feature #483): takes a row-level lock on the reservation so that
-- concurrent cart mutations (ExtendHold / ShrinkHold / ReacquireHold) of the
-- same gateway cart serialize on it before touching session_seats. MUST be
-- called inside a transaction; the lock is held until commit/rollback.
-- Returns pgx.ErrNoRows when the reservation does not exist.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  id = $1
FOR UPDATE;

-- name: UpdateReservationQuantity :one
-- W1-A5a: rewrites the reservation's aggregate quantity after an
-- extend/shrink. Only open reservations (draft/active) may be resized;
-- pgx.ErrNoRows signals the reservation is closed and the caller must abort.
UPDATE reservations
SET    quantity   = $2,
       updated_at = now()
WHERE  id = $1
  AND  state IN ('draft', 'active')
RETURNING id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
          expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at;

-- name: RefreshReservationsExpiry :many
-- W1-A5a: slides the TTL of the given open reservations to $2. Closed
-- reservations are silently skipped (not returned), which lets the caller
-- detect a swept cart by comparing the returned count with len(ids).
UPDATE reservations
SET    expires_at = $2,
       updated_at = now()
WHERE  id = ANY($1::uuid[])
  AND  state IN ('draft', 'active')
RETURNING id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
          expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at;

-- name: UpdateReservationCustomer :exec
-- W1-A4d (feature #482): attaches the resolved customers.Resolve() result to
-- the reservation once the public feed checkout (or any other buyer-
-- collecting surface) has identified the buyer. Deliberately :exec (not
-- :one) and NOT part of the shared ReservationRow projection — widening that
-- scanner would require updating every other reservations query per the
-- repo convention, and no caller needs customer_id back on the same
-- round-trip. Safe to call unconditionally; a closed reservation still
-- records who it was for.
UPDATE reservations
SET    customer_id = $2,
       updated_at  = now()
WHERE  id = $1;

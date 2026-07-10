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

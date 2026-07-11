-- sessions.sql — sqlc query definitions for the sessions table (feature #126).
-- Sessions are scoped to an event via the event_id foreign key.
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertSession :one
-- InsertSession creates a new session for the given event.
-- status defaults to 'scheduled' when empty.
-- Returns the created row including the uuidv7 PK assigned by the database.
INSERT INTO sessions (event_id, start_at, end_at, capacity_total, status)
VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, ''), 'scheduled'))
RETURNING id, event_id, start_at, end_at, capacity_total, status, created_at, updated_at, deleted_at;

-- name: GetSessionByID :one
-- GetSessionByID fetches an active session by its UUID primary key scoped to the event.
-- Returns pgx.ErrNoRows when not found, already deleted, or belongs to a different event.
SELECT id, event_id, start_at, end_at, capacity_total, status, created_at, updated_at, deleted_at
FROM   sessions
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL;

-- name: ListSessionsByEvent :many
-- ListSessionsByEvent returns all active sessions for the given event.
-- Ordered by start_at ASC so the earliest session is first.
SELECT id, event_id, start_at, end_at, capacity_total, status, created_at, updated_at, deleted_at
FROM   sessions
WHERE  event_id = $1
  AND  deleted_at IS NULL
ORDER BY start_at ASC, id ASC;

-- name: UpdateSession :one
-- UpdateSession applies a partial update to an active session.
-- Scoped by event_id. NULL/zero optional fields keep the existing values.
-- capacity_total can only be updated to a positive value (CHECK enforced by DB).
UPDATE sessions
SET    start_at       = CASE WHEN $3::timestamptz IS NOT NULL THEN $3::timestamptz ELSE start_at END,
       end_at         = CASE WHEN $4::timestamptz IS NOT NULL THEN $4::timestamptz ELSE end_at END,
       capacity_total = CASE WHEN $5::integer     IS NOT NULL THEN $5::integer     ELSE capacity_total END,
       status         = COALESCE(NULLIF($6, ''), status),
       updated_at     = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, start_at, end_at, capacity_total, status, created_at, updated_at, deleted_at;

-- name: SoftDeleteSession :one
-- SoftDeleteSession marks a session as deleted by setting deleted_at.
-- Scoped by event_id to enforce owner-gated mutation policy.
-- The row is not physically removed.
UPDATE sessions
SET    deleted_at = now(),
       updated_at = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, start_at, end_at, capacity_total, status, created_at, updated_at, deleted_at;

-- name: GetSessionSeatingBinding :one
-- GetSessionSeatingBinding fetches the seating-related columns for a session
-- scoped by event. Returns pgx.ErrNoRows when not found, already soft-deleted,
-- or belongs to a different event. Used by the seating-binding endpoint
-- (feature #306, Wave SEAT-B2) to decide first-bind vs rebind and to know
-- whether a rebind is safe.
SELECT id, event_id, admission_mode, seating_plan_version_id,
       seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  event_id   = $2
  AND  deleted_at IS NULL;

-- name: GetSessionSeatingBindingForUpdate :one
-- GetSessionSeatingBindingForUpdate is the row-locking variant of
-- GetSessionSeatingBinding. Taken at the top of the seating-bind transaction
-- (feature #306, Wave SEAT-B2) so binds serialize against any concurrent
-- transaction that mutates the session's seat inventory — the seated
-- reservation path locks the same sessions row via
-- IncrementSessionSeatStatusVersion, which closes the TOCTOU window between
-- the rebind zero-reservations check and the session_seats wipe. MUST be
-- called inside a transaction; the lock releases on commit / rollback.
SELECT id, event_id, admission_mode, seating_plan_version_id,
       seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  event_id   = $2
  AND  deleted_at IS NULL
FOR UPDATE;

-- name: BindSessionSeatingPlan :one
-- BindSessionSeatingPlan flips a session onto the (admission_mode,
-- seating_plan_version_id) tuple and recomputes capacity_total from the
-- materialized-seat count computed by the caller. seat_status_version is left
-- untouched — bind is a metadata change, not a seat-status transition. The
-- SET is guarded by the same event_id + soft-delete filter as the plain
-- sessions CRUD to keep the mutation policy consistent across the domain.
UPDATE sessions
SET    admission_mode          = $3,
       seating_plan_version_id = $4,
       capacity_total          = $5,
       updated_at              = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, admission_mode, seating_plan_version_id,
          seat_status_version, capacity_total;

-- name: CountOverlappingSessions :one
-- CountOverlappingSessions counts active sessions for the given event whose
-- time range overlaps with [start_at, end_at). The session with id=exclude_id
-- is excluded from the count so update operations can check against their
-- siblings without counting themselves.
-- Overlap condition: a.start_at < end_at AND a.end_at > start_at.
SELECT COUNT(*)::int
FROM   sessions
WHERE  event_id    = $1
  AND  id         <> $2
  AND  deleted_at  IS NULL
  AND  start_at    < $4
  AND  end_at      > $3;

-- sessions.sql — sqlc query definitions for the sessions table (feature #126).
-- Sessions are scoped to an event via the event_id foreign key.
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.
--
-- Wave 4 (AB-36/AB-38): the session owns its venue (venue_id NOT NULL), an
-- optional capacity_override, and its currency (+ currency_source telling
-- whether the value was derived from the venue geography or set explicitly).
-- capacity_total is always a derived value (plan version -> capacity_override
-- -> venues.capacity_default), computed at the application layer.

-- name: InsertSession :one
-- InsertSession creates a new session for the given event at the given venue.
-- status defaults to 'scheduled' when empty. currency / currency_source are
-- resolved by the handler before the insert (AB-38).
-- Returns the created row including the uuidv7 PK assigned by the database.
INSERT INTO sessions (event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, currency, currency_source)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF($7, ''), 'scheduled'), $8, $9)
RETURNING id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at;

-- name: GetSessionByID :one
-- GetSessionByID fetches an active session by its UUID primary key scoped to the event.
-- Returns pgx.ErrNoRows when not found, already deleted, or belongs to a different event.
SELECT id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at
FROM   sessions
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL;

-- name: ListSessionsByEvent :many
-- ListSessionsByEvent returns all active sessions for the given event.
-- Ordered by start_at ASC so the earliest session is first.
SELECT id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at
FROM   sessions
WHERE  event_id = $1
  AND  deleted_at IS NULL
ORDER BY start_at ASC, id ASC;

-- name: UpdateSession :one
-- UpdateSession applies a partial update to an active session.
-- Scoped by event_id. NULL/zero optional fields keep the existing values.
-- capacity_total is the derived value computed by the handler; changing the
-- currency cascades to ticket_tiers via ticket_tiers_currency_matches_session
-- (ON UPDATE CASCADE), so a session can never end up with mixed-currency tiers.
UPDATE sessions
SET    venue_id          = CASE WHEN $3::uuid        IS NOT NULL THEN $3::uuid        ELSE venue_id END,
       start_at          = CASE WHEN $4::timestamptz IS NOT NULL THEN $4::timestamptz ELSE start_at END,
       end_at            = CASE WHEN $5::timestamptz IS NOT NULL THEN $5::timestamptz ELSE end_at END,
       capacity_total    = CASE WHEN $6::integer     IS NOT NULL THEN $6::integer     ELSE capacity_total END,
       capacity_override = CASE WHEN $7::integer     IS NOT NULL THEN $7::integer     ELSE capacity_override END,
       status            = COALESCE(NULLIF($8, ''), status),
       currency          = CASE WHEN $9::text        IS NOT NULL THEN $9::text        ELSE currency END,
       currency_source   = COALESCE(NULLIF($10, ''), currency_source),
       updated_at        = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at;

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
RETURNING id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at;

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

-- name: GetSessionOrgContext :one
-- GetSessionOrgContext resolves the owning organization of a session by
-- walking the sessions → events join. Used by the Bil24 compatibility
-- gateway (RESERVATION, feature #312 wiring) to anchor a reservation to
-- the correct tenant without requiring the caller to know the event_id.
-- Returns pgx.ErrNoRows when the session or its event does not exist or
-- has been soft-deleted.
SELECT s.id AS session_id, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.id         = $1
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- name: GetSessionCurrency :one
-- GetSessionCurrency returns the ISO 4217 currency of an active session.
-- Used by the seating bind's tier auto-creation so every minted tier is
-- denominated in the session currency (AB-38 — one currency per session).
SELECT trim(currency)::text AS currency
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL;

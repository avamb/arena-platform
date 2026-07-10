-- session_seating_public.sql — public read queries backing the SEAT-B3
-- unauthenticated schema + seat-status endpoints (feature #307).
--
-- Companion hand-written gen file:
--   apps/backend/internal/adapters/postgres/gen/session_seating_public.sql.go
--
-- Visibility contract (mirrors the public feed):
--   * events.status = 'published'
--   * events.deleted_at IS NULL
--   * sessions.deleted_at IS NULL
--   * sessions.admission_mode != 'general_admission'
--     (GA sessions do not expose per-seat schema / status; they use the
--     regular capacity / inventory_ledger path.)

-- name: GetPublicSessionSchema :one
-- Resolves the geometry payload for a published seated session in one
-- round-trip. Joins sessions -> events (visibility gate) -> seating_plan_versions
-- (geometry + checksum). Returns pgx.ErrNoRows when the session is missing,
-- soft-deleted, unpublished, general-admission, or references a
-- seating_plan_version_id that no longer exists.
SELECT s.id,
       s.event_id,
       s.admission_mode,
       s.seating_plan_version_id,
       s.seat_status_version,
       v.geometry,
       v.geometry_checksum,
       v.capacity_seated,
       v.capacity_standing
FROM   sessions s
JOIN   events e             ON e.id = s.event_id
JOIN   seating_plan_versions v ON v.id = s.seating_plan_version_id
WHERE  s.id             = $1
  AND  s.deleted_at    IS NULL
  AND  e.deleted_at    IS NULL
  AND  e.status        = 'published'
  AND  s.admission_mode <> 'general_admission';

-- name: GetPublicSessionSeatStatusMeta :one
-- Cheaper sibling of GetPublicSessionSchema for the seat-status endpoints:
-- fetches only the version cursor + visibility gate, avoiding the jsonb
-- geometry column. Handlers use the returned seat_status_version to build
-- the response envelope and to determine whether a delta caller is already
-- up to date.
SELECT s.id,
       s.event_id,
       s.admission_mode,
       s.seat_status_version
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  s.id             = $1
  AND  s.deleted_at    IS NULL
  AND  e.deleted_at    IS NULL
  AND  e.status        = 'published'
  AND  s.admission_mode <> 'general_admission';

-- name: ListSessionAdmissionModesByEvent :many
-- Returns (id, admission_mode) for every active session of an event so
-- the public feed session serializer can expose schema_url / seat_status_url
-- only when the session is seated (admission_mode != 'general_admission').
SELECT s.id,
       s.admission_mode
FROM   sessions s
WHERE  s.event_id     = $1
  AND  s.deleted_at  IS NULL
ORDER  BY s.start_at ASC, s.id ASC;

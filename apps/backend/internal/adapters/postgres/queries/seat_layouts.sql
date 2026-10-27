-- seat_layouts.sql — sqlc-style query definitions for saved seat layouts,
-- plan annotations and the colour of a session category (task S1d,
-- migration 0171). Companion hand-written gen file:
-- apps/backend/internal/adapters/postgres/gen/seat_layouts.sql.go
--
-- Applying a layout is NOT here: seatlayout.ApplyLayout locks the sessions
-- row first and writes the seats through the seat batch queries.

-- name: LockSeatLayoutsForPlan :exec
SELECT pg_advisory_xact_lock(hashtextextended('seat_layouts:' || $1::text || ':' || $2::text, 0));

-- name: CountSeatLayouts :one
SELECT count(*) FROM seat_layouts WHERE org_id = $1 AND seating_plan_id = $2;

-- name: InsertSeatLayout :one
INSERT INTO seat_layouts (org_id, seating_plan_id, name, source, algorithm, params,
                          payload, created_by, created_by_label)
VALUES ($1, $2, btrim($3), $4, $5, $6::jsonb, $7::jsonb, $8, $9)
RETURNING *;

-- name: GetSeatLayout :one
SELECT * FROM seat_layouts WHERE id = $1 AND org_id = $2 AND seating_plan_id = $3;

-- name: GetSeatLayoutByID :one
SELECT * FROM seat_layouts WHERE id = $1 AND org_id = $2;

-- name: ListSeatLayoutSummaries :many
-- (slots and counts only; the seat map is in GetSeatLayout)

-- name: RenameSeatLayout :one
UPDATE seat_layouts SET name = btrim($4), updated_at = now()
WHERE  id = $1 AND org_id = $2 AND seating_plan_id = $3
RETURNING *;

-- name: DeleteSeatLayout :execrows
DELETE FROM seat_layouts WHERE id = $1 AND org_id = $2 AND seating_plan_id = $3;

-- name: GetSeatingPlanAnnotations :one
SELECT id, owner_org_id, visibility, current_version_id, annotations, updated_at
FROM   seating_plans WHERE id = $1 AND deleted_at IS NULL;

-- name: SetSeatingPlanAnnotations :one
UPDATE seating_plans SET annotations = $3::jsonb, updated_at = now()
WHERE  id = $1 AND owner_org_id = $2 AND deleted_at IS NULL
RETURNING id, owner_org_id, visibility, current_version_id, annotations, updated_at;

-- name: ListSessionTierColors :many
SELECT id, color FROM ticket_tiers
WHERE  session_id = $1 AND deleted_at IS NULL AND color IS NOT NULL;

-- name: GetTicketTierColor :one
SELECT color FROM ticket_tiers WHERE id = $1 AND session_id = $2;

-- name: SetTicketTierColor :execrows
UPDATE ticket_tiers SET color = $3, updated_at = now()
WHERE  id = $1 AND session_id = $2 AND deleted_at IS NULL;

-- name: ListSessionSeatAssignments :many
SELECT seat_key, tier_id, status FROM session_seats
WHERE  session_id = $1 AND kind = 'seat' ORDER BY seat_key;

-- name: GetSessionPlanID :one
SELECT v.seating_plan_id FROM sessions s
JOIN   seating_plan_versions v ON v.id = s.seating_plan_version_id
WHERE  s.id = $1;

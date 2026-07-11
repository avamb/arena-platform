-- seating_plans.sql — sqlc-style query definitions for the
-- seating_plans and seating_plan_versions tables (feature #302, Wave
-- SEAT-A1). Companion hand-written gen file:
--   apps/backend/internal/adapters/postgres/gen/seating_plans.sql.go
--
-- All write queries are scoped by owner_org_id so RBAC and the
-- owner-gated mutation policy (§5.1 of 09_autoforge/seating_backlog.md)
-- are enforced at the SQL boundary. Read queries follow the shared-read
-- / public-template visibility model — org-scope filtering happens at
-- the hseating handler layer since the RBAC decision depends on the
-- caller's org and role.
--
-- Soft-delete: seating_plans.deleted_at IS NULL for active rows.
-- seating_plan_versions is append-only; individual rows are never
-- deleted, and are treated as immutable once locked_at is non-NULL.

-- ─────────────────────────────────────────────────────────────────────
-- seating_plans
-- ─────────────────────────────────────────────────────────────────────

-- name: InsertSeatingPlan :one
INSERT INTO seating_plans (
    venue_id, owner_org_id, name, plan_type, visibility, status,
    source_seating_plan_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, venue_id, owner_org_id, name, plan_type, visibility, status,
          source_seating_plan_id, current_version_id,
          created_at, updated_at, deleted_at,
          (SELECT v.version_number
           FROM   seating_plan_versions v
           WHERE  v.id = seating_plans.current_version_id) AS current_version_number;

-- name: GetSeatingPlanByID :one
SELECT id, venue_id, owner_org_id, name, plan_type, visibility, status,
       source_seating_plan_id, current_version_id,
       created_at, updated_at, deleted_at,
       (SELECT v.version_number
        FROM   seating_plan_versions v
        WHERE  v.id = seating_plans.current_version_id) AS current_version_number
FROM   seating_plans
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: GetSeatingPlanByIDForOwner :one
SELECT id, venue_id, owner_org_id, name, plan_type, visibility, status,
       source_seating_plan_id, current_version_id,
       created_at, updated_at, deleted_at,
       (SELECT v.version_number
        FROM   seating_plan_versions v
        WHERE  v.id = seating_plans.current_version_id) AS current_version_number
FROM   seating_plans
WHERE  id = $1
  AND  owner_org_id = $2
  AND  deleted_at IS NULL;

-- name: ListSeatingPlansByOwner :many
SELECT id, venue_id, owner_org_id, name, plan_type, visibility, status,
       source_seating_plan_id, current_version_id,
       created_at, updated_at, deleted_at,
       (SELECT v.version_number
        FROM   seating_plan_versions v
        WHERE  v.id = seating_plans.current_version_id) AS current_version_number
FROM   seating_plans
WHERE  owner_org_id = $1
  AND  deleted_at IS NULL
ORDER  BY created_at DESC, id DESC;

-- name: ListSeatingPlansByVenue :many
SELECT id, venue_id, owner_org_id, name, plan_type, visibility, status,
       source_seating_plan_id, current_version_id,
       created_at, updated_at, deleted_at,
       (SELECT v.version_number
        FROM   seating_plan_versions v
        WHERE  v.id = seating_plans.current_version_id) AS current_version_number
FROM   seating_plans
WHERE  venue_id = $1
  AND  deleted_at IS NULL
ORDER  BY created_at DESC, id DESC;

-- name: UpdateSeatingPlan :one
UPDATE seating_plans
SET    name       = $3,
       plan_type  = $4,
       visibility = $5,
       status     = $6,
       updated_at = now()
WHERE  id = $1
  AND  owner_org_id = $2
  AND  deleted_at IS NULL
RETURNING id, venue_id, owner_org_id, name, plan_type, visibility, status,
          source_seating_plan_id, current_version_id,
          created_at, updated_at, deleted_at,
          (SELECT v.version_number
           FROM   seating_plan_versions v
           WHERE  v.id = seating_plans.current_version_id) AS current_version_number;

-- name: SetSeatingPlanCurrentVersion :one
UPDATE seating_plans
SET    current_version_id = $3,
       updated_at         = now()
WHERE  id = $1
  AND  owner_org_id = $2
  AND  deleted_at IS NULL
RETURNING id, venue_id, owner_org_id, name, plan_type, visibility, status,
          source_seating_plan_id, current_version_id,
          created_at, updated_at, deleted_at,
          (SELECT v.version_number
           FROM   seating_plan_versions v
           WHERE  v.id = seating_plans.current_version_id) AS current_version_number;

-- name: ArchiveSeatingPlan :one
UPDATE seating_plans
SET    status     = 'archived',
       updated_at = now()
WHERE  id = $1
  AND  owner_org_id = $2
  AND  deleted_at IS NULL
RETURNING id, venue_id, owner_org_id, name, plan_type, visibility, status,
          source_seating_plan_id, current_version_id,
          created_at, updated_at, deleted_at,
          (SELECT v.version_number
           FROM   seating_plan_versions v
           WHERE  v.id = seating_plans.current_version_id) AS current_version_number;

-- name: SoftDeleteSeatingPlan :one
UPDATE seating_plans
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  owner_org_id = $2
  AND  deleted_at IS NULL
RETURNING id, venue_id, owner_org_id, name, plan_type, visibility, status,
          source_seating_plan_id, current_version_id,
          created_at, updated_at, deleted_at,
          (SELECT v.version_number
           FROM   seating_plan_versions v
           WHERE  v.id = seating_plans.current_version_id) AS current_version_number;

-- ─────────────────────────────────────────────────────────────────────
-- seating_plan_versions
-- ─────────────────────────────────────────────────────────────────────

-- name: InsertSeatingPlanVersion :one
INSERT INTO seating_plan_versions (
    seating_plan_id, version_number, geometry, geometry_checksum,
    svg_asset_media_id, capacity_seated, capacity_standing
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, seating_plan_id, version_number, geometry, geometry_checksum,
          svg_asset_media_id, capacity_seated, capacity_standing,
          locked_at, created_at;

-- name: GetSeatingPlanVersionByID :one
SELECT id, seating_plan_id, version_number, geometry, geometry_checksum,
       svg_asset_media_id, capacity_seated, capacity_standing,
       locked_at, created_at
FROM   seating_plan_versions
WHERE  id = $1;

-- name: GetSeatingPlanVersionByNumber :one
-- Fetches a single version by its 1-based positional number scoped to
-- the plan. Uses the seating_plan_versions_plan_recent index — replaces
-- the list-and-scan pattern in the GET /versions/{n} handler.
SELECT id, seating_plan_id, version_number, geometry, geometry_checksum,
       svg_asset_media_id, capacity_seated, capacity_standing,
       locked_at, created_at
FROM   seating_plan_versions
WHERE  seating_plan_id = $1
  AND  version_number  = $2;

-- name: ListSeatingPlanVersionsByPlan :many
SELECT id, seating_plan_id, version_number, geometry, geometry_checksum,
       svg_asset_media_id, capacity_seated, capacity_standing,
       locked_at, created_at
FROM   seating_plan_versions
WHERE  seating_plan_id = $1
ORDER  BY version_number DESC, id DESC;

-- name: GetLatestSeatingPlanVersionNumber :one
SELECT COALESCE(MAX(version_number), 0)::integer AS latest
FROM   seating_plan_versions
WHERE  seating_plan_id = $1;

-- name: LockSeatingPlanVersion :one
UPDATE seating_plan_versions
SET    locked_at = COALESCE(locked_at, now())
WHERE  id = $1
RETURNING id, seating_plan_id, version_number, geometry, geometry_checksum,
          svg_asset_media_id, capacity_seated, capacity_standing,
          locked_at, created_at;

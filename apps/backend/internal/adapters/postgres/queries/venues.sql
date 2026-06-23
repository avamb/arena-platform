-- venues.sql — sqlc query definitions for the venues table (feature #124).
-- All write queries are scoped by org_id to enforce owner-gated mutation policy.
-- GET queries are NOT scoped by org_id (shared read-only across orgs).
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertVenue :one
INSERT INTO venues (org_id, city_id, name, address, capacity_default)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at;

-- name: GetVenueByID :one
SELECT id, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at
FROM   venues
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: ListVenues :many
SELECT id, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at
FROM   venues
WHERE  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: ListVenuesByOrg :many
SELECT id, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at
FROM   venues
WHERE  org_id = $1
  AND  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: UpdateVenue :one
UPDATE venues
SET    city_id          = CASE WHEN $3::uuid IS NOT NULL THEN $3::uuid ELSE city_id END,
       name             = COALESCE(NULLIF($4, ''), name),
       address          = CASE WHEN $5::text IS NOT NULL THEN $5::text ELSE address END,
       capacity_default = CASE WHEN $6::integer IS NOT NULL THEN $6::integer ELSE capacity_default END,
       updated_at       = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at;

-- name: SoftDeleteVenue :one
UPDATE venues
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at;

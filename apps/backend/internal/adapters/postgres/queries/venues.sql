-- venues.sql — sqlc query definitions for the venues table (feature #124).
-- All write queries are scoped by org_id to enforce owner-gated mutation policy.
-- GET queries are NOT scoped by org_id (shared read-only across orgs).
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertVenue :one
INSERT INTO venues (org_id, city_id, name, address, capacity_default)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, display_number, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at;

-- name: GetVenueByID :one
SELECT id, display_number, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at
FROM   venues
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: ListVenues :many
SELECT id, display_number, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at
FROM   venues
WHERE  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: ListVenuesByOrg :many
SELECT id, display_number, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at
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
RETURNING id, display_number, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at;

-- name: SoftDeleteVenue :one
UPDATE venues
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, city_id, name, address, capacity_default, created_at, updated_at, deleted_at;

-- name: GetVenueSessionContext :one
-- GetVenueSessionContext returns the venue attributes the session
-- create/update path needs in one round trip (Wave 4, AB-36/AB-38):
-- the owning org (venue must belong to the event's org), the default
-- capacity (last stop of the capacity resolution chain), and the currency
-- derived from the venue geography:
--   city.currency_override -> city's country currency -> country matching
--   venues.country (ISO2). NULL when the venue has neither a city nor a
--   recognized country — the operator must then supply the currency.
SELECT v.org_id,
       v.capacity_default,
       COALESCE(ci.currency_override, cc.currency, vc.currency)::text AS derived_currency
FROM   venues v
LEFT JOIN cities    ci ON ci.id   = v.city_id
LEFT JOIN countries cc ON cc.id   = ci.country_id
LEFT JOIN countries vc ON vc.iso2 = v.country
WHERE  v.id = $1
  AND  v.deleted_at IS NULL;

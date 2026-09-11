-- imports_arena.sql — queries backing the source=arena branch of
-- POST /v1/organizations/{org_id}/imports/event-bundle
-- (feature #525, W1-E1c; event-bundle spec §3.2).
--
-- An arena-native bundle carries NO Bil24 identifiers: the caller either
-- returns the compatibility ids arena minted for it, or nothing at all. The
-- venue is therefore never looked up by venues.external_bil24_id (that column
-- carries a GLOBAL partial-unique index and must stay reserved for genuinely
-- Bil24-sourced venues) but by the organization plus a normalised name.

-- name: FindActiveVenueByNormalizedName :one
-- Spec §3.2 step 4: an arena bundle without a venueId reuses an existing
-- ACTIVE venue of the same organization whose name matches case- and
-- whitespace-insensitively. Ordered so the outcome is stable when an operator
-- created the same venue twice: the oldest row wins, because that is the one
-- earlier bundles will already have been mapped to.
SELECT id, display_number, org_id, city_id, name, address, capacity_default,
       created_at, updated_at, deleted_at
FROM   venues
WHERE  org_id = $1
  AND  lower(btrim(name)) = lower(btrim($2::text))
  AND  deleted_at IS NULL
ORDER BY created_at ASC, id ASC
LIMIT  1;

-- name: InsertArenaVenue :one
-- Creates a venue for an arena-native bundle. Deliberately does NOT write
-- external_bil24_id: the venue has no Bil24 identity, and stamping a
-- placeholder there would collide on the global partial-unique index the
-- moment a second organization imported the same placeholder.
INSERT INTO venues (
    org_id, city_id, name, address, timezone,
    geo_lat, geo_lng, country
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, display_number, org_id, city_id, name, address, capacity_default,
          created_at, updated_at, deleted_at;

-- imports_bil24.sql — queries backing POST /v1/organizations/{org_id}/imports/bil24-session
-- (feature #517, W1-C3c; spec §13.2).
--
-- The import upserts a venue / event / session / tier graph keyed by the
-- Bil24 source identifiers. events.external_bil24_id and
-- venues.external_bil24_id carry GLOBAL partial-unique indexes (migrations
-- 0070 / 0073), so lookups intentionally match on the external id alone and
-- the caller compares org_id afterwards to produce a cross-tenant conflict
-- instead of a unique-violation that would abort the import transaction.

-- name: GetVenueByBil24ExternalID :one
SELECT id, display_number, org_id, city_id, name, address, capacity_default,
       created_at, updated_at, deleted_at
FROM   venues
WHERE  external_bil24_id = $1
  AND  deleted_at IS NULL;

-- name: GetVenueImportContext :one
SELECT org_id, timezone, external_bil24_id
FROM   venues
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: InsertImportedVenue :one
INSERT INTO venues (
    org_id, city_id, name, address, timezone,
    geo_lat, geo_lng, country, external_bil24_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, display_number, org_id, city_id, name, address, capacity_default,
          created_at, updated_at, deleted_at;

-- name: UpdateImportedVenueGeography :exec
UPDATE venues
SET    city_id    = COALESCE($2::uuid, city_id),
       address    = COALESCE($3::text, address),
       timezone   = COALESCE(NULLIF($4::text, ''), timezone),
       geo_lat    = COALESCE($5::numeric, geo_lat),
       geo_lng    = COALESCE($6::numeric, geo_lng),
       country    = COALESCE($7::text, country),
       updated_at = now()
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: GetEventByBil24ExternalID :one
SELECT id, display_number, org_id, name, description, status, first_session_at,
       last_session_at, visibility, image_url, poster_media_id, slug,
       short_description, genre, age_rating, duration_minutes, teaser_url,
       trailer_url, meta_description, meta_keywords, created_at, updated_at,
       deleted_at
FROM   events
WHERE  external_bil24_id = $1
  AND  deleted_at IS NULL;

-- name: SetEventBil24ExternalID :exec
UPDATE events
SET    external_bil24_id = $3,
       updated_at        = now()
WHERE  id     = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: SetEventPosterMediaID :exec
UPDATE events
SET    poster_media_id = $3,
       updated_at      = now()
WHERE  id     = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: SetEventImportMetadata :exec
UPDATE events
SET    name        = COALESCE(NULLIF($3::text, ''), name),
       description = COALESCE($4::text, description),
       age_rating  = COALESCE($5::text, age_rating),
       updated_at  = now()
WHERE  id     = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: GetSessionImportContext :one
SELECT s.id, s.event_id, s.venue_id, s.currency, s.currency_source, s.status,
       s.admission_mode, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.id = $1
  AND  s.deleted_at IS NULL;

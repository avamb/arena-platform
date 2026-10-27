-- Venue sharing (migration 0172): a verified venue is open to every organization.

-- name: ListSharedVenuesForOrg :many
-- Verified venues of OTHER organizations, optionally narrowed to one city or
-- one country (ISO 3166-1 alpha-2). The caller's own venues are not listed:
-- they come from ListVenuesByOrg.
SELECT id, display_number, org_id, city_id, name, address, capacity_default,
       address_line1, address_line2, postal_code, country,
       geo_lat::float8 AS geo_lat, geo_lng::float8 AS geo_lng, timezone,
       contact_phone, contact_email, website_url, status,
       created_at, updated_at, deleted_at
FROM   venues
WHERE  review_status = 'verified'
  AND  deleted_at IS NULL
  AND  status = 'active'
  AND  org_id <> $1
  AND  ($2::uuid IS NULL OR city_id = $2)
  AND  ($3::text = '' OR upper(country) = $3)
ORDER  BY name ASC, id ASC
LIMIT  $4;

-- name: SetVenueReviewStatus :one
UPDATE venues
SET    review_status = $2,
       verified_at   = CASE WHEN $2 = 'verified' THEN now() ELSE NULL END,
       updated_at    = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, org_id, name, review_status, verified_at;

-- name: CountForeignSessionsAtVenue :one
-- Live sessions of other organizations held at the venue.
SELECT count(*)
FROM   sessions s
JOIN   events  e ON e.id = s.event_id
WHERE  s.venue_id = $1
  AND  s.deleted_at IS NULL
  AND  e.org_id <> $2;

-- name: ListVenueReviewStates :many
-- Verification state of the given venues (VenueRow does not carry it).
SELECT id, review_status, verified_at
FROM   venues
WHERE  id = ANY($1::uuid[]);

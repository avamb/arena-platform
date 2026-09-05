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

-- name: ListActionVenuesByOrg :many
-- ListActionVenuesByOrg returns the distinct set of venues owned by the given
-- organization that host at least one non-deleted session of a published event,
-- joined with localized city and country data. Feature #476 W1-A2b slice 14
-- (Bil24 compat spec §7.1): the Bil24-compat GET_ALL_ACTIONS aggregation
-- projects countryList / cityList / venueList blocks off this shape; each
-- venue row already carries the parent city and country identifiers so the
-- handler can group without additional round-trips. Localized names follow
-- the same locale → 'en' → key fallback chain as ListCountries / ListCities.
-- Country is resolved from the city.country_id link first, falling back to
-- the venues.country ISO2 direct link for venues without a city_id.
-- Only published events participate — draft/archived sessions must never
-- surface a venue block in the compat catalog.
SELECT DISTINCT
    v.id,
    v.display_number,
    v.name,
    v.city_id,
    ci.slug                                                                  AS city_slug,
    COALESCE(t_city_loc.value, t_city_en.value, ci.slug)                     AS city_name,
    COALESCE(co_city.id, co_vn.id)                                           AS country_id,
    COALESCE(co_city.iso2, co_vn.iso2)                                       AS country_iso2,
    COALESCE(co_city.iso3, co_vn.iso3)                                       AS country_iso3,
    COALESCE(co_city.slug, co_vn.slug)                                       AS country_slug,
    COALESCE(t_ctry_loc.value, t_ctry_en.value, co_city.iso2, co_vn.iso2)    AS country_name
FROM   venues v
JOIN   sessions s ON s.venue_id = v.id AND s.deleted_at IS NULL
JOIN   events   e ON e.id       = s.event_id AND e.status = 'published'
LEFT JOIN cities    ci      ON ci.id       = v.city_id
LEFT JOIN countries co_city ON co_city.id  = ci.country_id
LEFT JOIN countries co_vn   ON co_vn.iso2  = v.country
LEFT JOIN i18n_text t_city_loc ON t_city_loc.namespace = 'geo.cities'
    AND t_city_loc.key = ci.slug
    AND t_city_loc.locale = $2
LEFT JOIN i18n_text t_city_en  ON t_city_en.namespace  = 'geo.cities'
    AND t_city_en.key = ci.slug
    AND t_city_en.locale = 'en'
LEFT JOIN i18n_text t_ctry_loc ON t_ctry_loc.namespace = 'geo.countries'
    AND t_ctry_loc.key = COALESCE(co_city.iso2, co_vn.iso2)
    AND t_ctry_loc.locale = $2
LEFT JOIN i18n_text t_ctry_en  ON t_ctry_en.namespace  = 'geo.countries'
    AND t_ctry_en.key = COALESCE(co_city.iso2, co_vn.iso2)
    AND t_ctry_en.locale = 'en'
WHERE  v.org_id = $1
  AND  v.deleted_at IS NULL
ORDER BY country_iso2 NULLS LAST, city_slug NULLS LAST, v.display_number;

-- Geo reference queries: countries and cities (feature #123)

-- name: ListCountries :many
-- ListCountries returns all countries ordered by iso2, with localized names
-- resolved from i18n_text. Falls back to the English name, then to iso2 itself.
SELECT
    c.id,
    c.iso2,
    c.iso3,
    c.slug,
    COALESCE(t_loc.value, t_en.value, c.iso2) AS name
FROM countries c
LEFT JOIN i18n_text t_loc ON t_loc.namespace = 'geo.countries'
    AND t_loc.key = c.iso2
    AND t_loc.locale = $1
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.countries'
    AND t_en.key = c.iso2
    AND t_en.locale = 'en'
ORDER BY c.iso2;

-- name: ListCities :many
-- ListCities returns cities, optionally filtered by country_id.
-- Pass NULL to return all cities.  Localized names are resolved from
-- i18n_text with the same fallback chain as ListCountries.
SELECT
    ci.id,
    ci.country_id,
    ci.slug,
    c.iso2    AS country_iso2,
    COALESCE(t_loc.value, t_en.value, ci.slug) AS name
FROM cities ci
JOIN countries c ON c.id = ci.country_id
LEFT JOIN i18n_text t_loc ON t_loc.namespace = 'geo.cities'
    AND t_loc.key = ci.slug
    AND t_loc.locale = $1
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
    AND t_en.key = ci.slug
    AND t_en.locale = 'en'
WHERE ($2::uuid IS NULL OR ci.country_id = $2::uuid)
ORDER BY ci.slug;

-- name: GetCountryByISO2 :one
-- GetCountryByISO2 fetches a single country row by its ISO 3166-1 alpha-2 code.
SELECT id, iso2, iso3, slug, created_at
FROM countries
WHERE iso2 = $1;

-- name: GetCountryBySlug :one
-- GetCountryBySlug fetches a single country row by its slug.
SELECT id, iso2, iso3, slug, created_at
FROM countries
WHERE slug = $1;

-- name: InsertCountry :one
-- InsertCountry creates a new country row and returns the full row.
INSERT INTO countries (iso2, iso3, slug)
VALUES ($1, $2, $3)
RETURNING id, iso2, iso3, slug, created_at;

-- name: UpdateCountry :one
-- UpdateCountry updates the iso3 and slug of an existing country identified by iso2.
UPDATE countries
SET iso3 = $2,
    slug = $3
WHERE iso2 = $1
RETURNING id, iso2, iso3, slug, created_at;

-- name: GetCityByID :one
-- GetCityByID fetches a city row by its UUID.
SELECT id, country_id, slug, created_at
FROM cities
WHERE id = $1;

-- name: GetCityBySlug :one
-- GetCityBySlug fetches a city row by its slug.
SELECT id, country_id, slug, created_at
FROM cities
WHERE slug = $1;

-- name: InsertCity :one
-- InsertCity creates a new city row linked to the given country and returns the full row.
INSERT INTO cities (country_id, slug)
VALUES ($1, $2)
RETURNING id, country_id, slug, created_at;

-- name: UpdateCity :one
-- UpdateCity updates the slug of an existing city identified by id.
UPDATE cities
SET slug = $2
WHERE id = $1
RETURNING id, country_id, slug, created_at;

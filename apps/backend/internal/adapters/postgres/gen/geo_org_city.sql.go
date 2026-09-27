// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: geo.sql
//
// Organization-side city creation (POST /v1/organizations/{org_id}/cities,
// migration 0113's city.create permission): an organizer creating a venue in
// a city arena does not know yet adds it inside an existing country.

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getCountryByID = `-- name: GetCountryByID :one
SELECT id, iso2, iso3, slug, currency, created_at
FROM countries
WHERE id = $1
`

// GetCountryByID fetches a country by its UUID. Returns pgx.ErrNoRows when
// no such country exists.
func (q *Queries) GetCountryByID(ctx context.Context, id uuid.UUID) (CountryRow, error) {
	var c CountryRow
	err := q.db.QueryRow(ctx, getCountryByID, id).Scan(&c.ID, &c.Iso2, &c.Iso3, &c.Slug, &c.Currency, &c.CreatedAt)
	return c, err
}

const lockCountryCities = `-- name: LockCountryCities :exec
SELECT pg_advisory_xact_lock(hashtextextended('org_city_create:' || $1::text, 0))`

// LockCountryCities serializes organization-side city creation within one
// country for the rest of the transaction, so two concurrent requests for
// the same new city cannot both miss the lookup and insert twice.
func (q *Queries) LockCountryCities(ctx context.Context, countryID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockCountryCities, countryID)
	return err
}

const findCityInCountryByName = `-- name: FindCityInCountryByName :one
SELECT ci.id
FROM   cities ci
WHERE  ci.country_id = $1
  AND  ( (NULLIF($3::text, '') IS NOT NULL AND ci.slug = $3::text)
      OR EXISTS (
           SELECT 1
           FROM   i18n_text t
           WHERE  t.namespace = 'geo.cities'
             AND  t.key = ci.slug
             AND  lower(regexp_replace(btrim(t.value), '\s+', ' ', 'g')) = $2::text))
ORDER  BY (ci.slug = $3::text) DESC, ci.created_at, ci.id
LIMIT  1`

// FindCityInCountryByName returns the id of a city of the country whose
// stored name in ANY locale equals normalizedName (already lower-cased and
// whitespace-collapsed by the caller), or whose slug equals slug. An empty
// slug disables the slug match. Returns pgx.ErrNoRows when there is none.
func (q *Queries) FindCityInCountryByName(ctx context.Context, countryID uuid.UUID, normalizedName, slug string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, findCityInCountryByName, countryID, normalizedName, slug).Scan(&id)
	return id, err
}

const getCityWithName = `-- name: GetCityWithName :one
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
    AND t_loc.locale = $2
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
    AND t_en.key = ci.slug
    AND t_en.locale = 'en'
WHERE ci.id = $1
`

// GetCityWithName returns one city in the GET /v1/geo/cities item shape,
// with the same name fallback (locale → en → slug).
func (q *Queries) GetCityWithName(ctx context.Context, id uuid.UUID, locale string) (ListCityRow, error) {
	var i ListCityRow
	err := q.db.QueryRow(ctx, getCityWithName, id, locale).Scan(&i.ID, &i.CountryID, &i.Slug, &i.CountryIso2, &i.Name)
	return i, err
}

const citySlugTaken = `-- name: CitySlugTaken :one
SELECT EXISTS (SELECT 1 FROM cities WHERE slug = $1)`

// CitySlugTaken reports whether cities.slug (globally unique) is in use.
func (q *Queries) CitySlugTaken(ctx context.Context, slug string) (bool, error) {
	var taken bool
	err := q.db.QueryRow(ctx, citySlugTaken, slug).Scan(&taken)
	return taken, err
}

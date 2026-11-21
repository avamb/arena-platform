// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: none (the venue directory of spec 64 section 2.1.1)

package gen

import (
	"context"

	"github.com/google/uuid"
)

// venueDirectoryScope is the venues an organization may hold a date at: its
// own venues that are not archived and the verified active venues of other
// organizations (migration 0182). A venue's country is its city's country,
// else its address country. $1 is the organization.
const venueDirectoryScope = `WITH scope AS (
    SELECT v.id, v.name, v.city_id, v.org_id, v.address, v.address_line1,
           v.geo_lat::float8 AS geo_lat, v.geo_lng::float8 AS geo_lng,
           v.timezone, v.capacity_default, v.review_status,
           (v.org_id = $1) AS own,
           COALESCE(c.iso2::text, NULLIF(upper(btrim(v.country)), '')) AS country_iso2
    FROM   venues v
    LEFT JOIN cities ci   ON ci.id = v.city_id
    LEFT JOIN countries c ON c.id = ci.country_id
    WHERE  v.deleted_at IS NULL
      AND  ((v.org_id = $1 AND v.status <> 'archived')
         OR (v.org_id <> $1 AND v.review_status = 'verified' AND v.status = 'active'))
)
`

// VenueDirectoryCountryRow is one country of the directory.
type VenueDirectoryCountryRow struct {
	Iso2      string
	CountryID *uuid.UUID
	Name      *string
	Region    *string
	Venues    int64
	OwnVenues int64
}

const listVenueDirectoryCountries = `-- name: ListVenueDirectoryCountries :many
` + venueDirectoryScope + `
SELECT s.country_iso2, c.id, COALESCE(t_loc.value, t_en.value), c.region,
       count(*) AS venues, count(*) FILTER (WHERE s.own) AS own_venues
FROM   scope s
LEFT JOIN countries c ON c.iso2 = s.country_iso2
LEFT JOIN i18n_text t_loc ON t_loc.namespace = 'geo.countries' AND t_loc.key = s.country_iso2 AND t_loc.locale = $2
LEFT JOIN i18n_text t_en  ON t_en.namespace  = 'geo.countries' AND t_en.key  = s.country_iso2 AND t_en.locale  = 'en'
WHERE  s.country_iso2 IS NOT NULL
GROUP  BY s.country_iso2, c.id, t_loc.value, t_en.value, c.region
ORDER  BY s.country_iso2`

// ListVenueDirectoryCountries returns every country with at least one venue
// in the organization's scope, with the count of venues and of own venues.
// Name is the i18n name in the locale, else English, nil when there is none.
func (q *Queries) ListVenueDirectoryCountries(ctx context.Context, orgID uuid.UUID, locale string) ([]VenueDirectoryCountryRow, error) {
	rows, err := q.db.Query(ctx, listVenueDirectoryCountries, orgID, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VenueDirectoryCountryRow
	for rows.Next() {
		var r VenueDirectoryCountryRow
		if err := rows.Scan(&r.Iso2, &r.CountryID, &r.Name, &r.Region, &r.Venues, &r.OwnVenues); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VenueDirectoryCityRow is one city of the directory.
type VenueDirectoryCityRow struct {
	ID          uuid.UUID
	Name        string
	CountryIso2 string
	Venues      int64
	OwnVenues   int64
}

const listVenueDirectoryCities = `-- name: ListVenueDirectoryCities :many
` + venueDirectoryScope + `
SELECT ci.id, COALESCE(t_loc.value, t_en.value, ci.slug), s.country_iso2,
       count(*) AS venues, count(*) FILTER (WHERE s.own) AS own_venues
FROM   scope s
JOIN   cities ci ON ci.id = s.city_id
LEFT JOIN i18n_text t_loc ON t_loc.namespace = 'geo.cities' AND t_loc.key = ci.slug AND t_loc.locale = $2
LEFT JOIN i18n_text t_en  ON t_en.namespace  = 'geo.cities' AND t_en.key  = ci.slug AND t_en.locale  = 'en'
WHERE  s.country_iso2 = $3
GROUP  BY ci.id, t_loc.value, t_en.value, ci.slug, s.country_iso2
ORDER  BY 2, ci.id`

// ListVenueDirectoryCities returns the cities of the country that have venues
// in the organization's scope.
func (q *Queries) ListVenueDirectoryCities(ctx context.Context, orgID uuid.UUID, locale, country string) ([]VenueDirectoryCityRow, error) {
	rows, err := q.db.Query(ctx, listVenueDirectoryCities, orgID, locale, country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VenueDirectoryCityRow
	for rows.Next() {
		var r VenueDirectoryCityRow
		if err := rows.Scan(&r.ID, &r.Name, &r.CountryIso2, &r.Venues, &r.OwnVenues); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VenueDirectoryVenueRow is one venue of the directory. It carries no
// contacts: the directory never shows them.
type VenueDirectoryVenueRow struct {
	ID              uuid.UUID
	Name            string
	CityID          *uuid.UUID
	CityName        *string
	CountryIso2     *string
	Address         *string
	AddressLine1    *string
	GeoLat          *float64
	GeoLng          *float64
	Timezone        *string
	CapacityDefault *int32
	Own             bool
	ReviewStatus    string
	HasOwnPlan      bool
	HasPlan         bool
	Total           int64
}

const listVenueDirectoryVenues = `-- name: ListVenueDirectoryVenues :many
` + venueDirectoryScope + `
SELECT s.id, s.name, s.city_id, COALESCE(t_loc.value, t_en.value, ci.slug), s.country_iso2,
       s.address, s.address_line1, s.geo_lat, s.geo_lng, s.timezone, s.capacity_default,
       s.own, s.review_status, own_plan.yes, (own_plan.yes OR template.yes),
       count(*) OVER () AS total
FROM   scope s
LEFT JOIN cities ci ON ci.id = s.city_id
LEFT JOIN i18n_text t_loc ON t_loc.namespace = 'geo.cities' AND t_loc.key = ci.slug AND t_loc.locale = $2
LEFT JOIN i18n_text t_en  ON t_en.namespace  = 'geo.cities' AND t_en.key  = ci.slug AND t_en.locale  = 'en'
CROSS JOIN LATERAL (
    SELECT EXISTS (
        SELECT 1 FROM seating_plans p
        WHERE  p.venue_id = s.id AND p.owner_org_id = $1 AND p.status = 'active'
          AND  p.deleted_at IS NULL AND p.current_version_id IS NOT NULL) AS yes
) own_plan
CROSS JOIN LATERAL (
    SELECT EXISTS (
        SELECT 1 FROM seating_plans p
        WHERE  p.venue_id = s.id AND p.visibility = 'operator_verified'
          AND  p.verification_state = 'verified' AND p.status = 'active'
          AND  p.deleted_at IS NULL AND p.current_version_id IS NOT NULL) AS yes
) template
WHERE  ($3::text = '' OR s.country_iso2 = $3)
  AND  ($4::uuid IS NULL OR s.city_id = $4)
  AND  ($5::text = '' OR s.name ILIKE '%' || $5 || '%' ESCAPE '\')
ORDER  BY s.own DESC, lower(s.name), s.id
LIMIT  $6`

// ListVenueDirectoryVenues returns the venues of the scope narrowed by
// country (upper-case alpha-2, "" = any), city and a part of the name (a
// LIKE pattern already escaped with backslash, "" = any), own venues first,
// then by name. Total on every row is the count before the limit.
func (q *Queries) ListVenueDirectoryVenues(ctx context.Context, orgID uuid.UUID, locale, country string, cityID *uuid.UUID, namePart string, limit int32) ([]VenueDirectoryVenueRow, error) {
	rows, err := q.db.Query(ctx, listVenueDirectoryVenues, orgID, locale, country, cityID, namePart, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VenueDirectoryVenueRow
	for rows.Next() {
		var r VenueDirectoryVenueRow
		if err := rows.Scan(&r.ID, &r.Name, &r.CityID, &r.CityName, &r.CountryIso2,
			&r.Address, &r.AddressLine1, &r.GeoLat, &r.GeoLng, &r.Timezone, &r.CapacityDefault,
			&r.Own, &r.ReviewStatus, &r.HasOwnPlan, &r.HasPlan, &r.Total); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OrgCountryCount is a country and how many organizations are in it.
type OrgCountryCount struct {
	Iso2  string
	Count int64
}

const listVenueDirectoryQuickPickSources = `-- name: ListVenueDirectoryQuickPickSources :many
SELECT 'org' AS kind, upper(btrim(o.country)) AS iso2, 0::bigint AS n
FROM   organizations o
WHERE  o.id = $1 AND btrim(o.country) <> ''
UNION ALL
SELECT 'ref', upper(btrim(o.country)), count(*)
FROM   organizations o
JOIN   countries c ON c.iso2 = upper(btrim(o.country))
WHERE  o.deleted_at IS NULL AND btrim(o.country) <> ''
GROUP  BY upper(btrim(o.country))
ORDER  BY 1, 3 DESC, 2`

// ListVenueDirectoryQuickPickSources returns, in one round, the
// organization's own country (OrgCountry, "" when unset) and the reference
// countries where organizers work, most organizations first.
func (q *Queries) ListVenueDirectoryQuickPickSources(ctx context.Context, orgID uuid.UUID) (string, []OrgCountryCount, error) {
	rows, err := q.db.Query(ctx, listVenueDirectoryQuickPickSources, orgID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	own := ""
	var ref []OrgCountryCount
	for rows.Next() {
		var kind string
		var r OrgCountryCount
		if err := rows.Scan(&kind, &r.Iso2, &r.Count); err != nil {
			return "", nil, err
		}
		if kind == "org" {
			own = r.Iso2
			continue
		}
		ref = append(ref, r)
	}
	return own, ref, rows.Err()
}

// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: none (the superadmin's list of seating plans, spec 64 section 3.4)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AdminSeatingPlanFilter narrows the superadmin's list of seating plans.
// Every field is optional: nil or "" means any.
type AdminSeatingPlanFilter struct {
	// OwnerOrgID is the organization that owns the plan.
	OwnerOrgID *uuid.UUID
	// Country is the upper-case ISO 3166-1 alpha-2 code of the venue's
	// country: the country of the venue's city, else the venue's address
	// country (the rule of the venue directory).
	Country string
	// CityID is the city of the venue.
	CityID *uuid.UUID
	// VenueNamePart is a part of the venue name, a LIKE pattern already
	// escaped with backslash; it matches in any letter case.
	VenueNamePart string
}

// adminSeatingPlanScope is the live plans the superadmin may list: not
// deleted, not archived, at a venue that is not deleted or archived. Draft
// plans stay in. $1..$4 are the fields of AdminSeatingPlanFilter in order.
const adminSeatingPlanScope = `WITH scope AS (
    SELECT p.id, p.name, p.plan_type, p.visibility, p.status, p.owner_org_id, o.name AS org_name,
           p.venue_id, v.name AS venue_name, v.city_id, ci.slug AS city_slug,
           COALESCE(c.iso2::text, NULLIF(upper(btrim(v.country)), '')) AS country_iso2,
           p.verification_state, p.verification_note, p.verified_at,
           p.current_version_id, sv.version_number, sv.capacity_seated, sv.capacity_standing,
           p.created_at, p.updated_at
    FROM   seating_plans p
    JOIN   organizations o ON o.id = p.owner_org_id
    JOIN   venues v        ON v.id = p.venue_id
    LEFT JOIN cities ci    ON ci.id = v.city_id
    LEFT JOIN countries c  ON c.id = ci.country_id
    LEFT JOIN seating_plan_versions sv ON sv.id = p.current_version_id
    WHERE  p.deleted_at IS NULL AND p.status <> 'archived'
      AND  v.deleted_at IS NULL AND v.status <> 'archived'
      AND  ($1::uuid IS NULL OR p.owner_org_id = $1)
      AND  ($2::text = '' OR COALESCE(c.iso2::text, NULLIF(upper(btrim(v.country)), '')) = $2)
      AND  ($3::uuid IS NULL OR v.city_id = $3)
      AND  ($4::text = '' OR v.name ILIKE '%' || $4 || '%' ESCAPE '\')
)
`

// AdminSeatingPlanRow is one plan of the superadmin's list.
type AdminSeatingPlanRow struct {
	ID                uuid.UUID
	Name              string
	PlanType          string
	Visibility        string
	Status            string
	OwnerOrgID        uuid.UUID
	OrgName           string
	VenueID           uuid.UUID
	VenueName         string
	CityID            *uuid.UUID
	CityName          *string
	CountryIso2       *string
	VerificationState *string
	VerificationNote  *string
	VerifiedAt        *time.Time
	CurrentVersionID  *uuid.UUID
	VersionNumber     *int32
	CapacitySeated    *int32
	CapacityStanding  *int32
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const listAdminSeatingPlans = `-- name: ListAdminSeatingPlans :many
` + adminSeatingPlanScope + `
SELECT s.id, s.name, s.plan_type, s.visibility, s.status, s.owner_org_id, s.org_name,
       s.venue_id, s.venue_name, s.city_id,
       CASE WHEN s.city_id IS NULL THEN NULL ELSE COALESCE(t_loc.value, t_en.value, s.city_slug) END,
       s.country_iso2, s.verification_state, s.verification_note, s.verified_at,
       s.current_version_id, s.version_number, s.capacity_seated, s.capacity_standing,
       s.created_at, s.updated_at
FROM   scope s
LEFT JOIN i18n_text t_loc ON t_loc.namespace = 'geo.cities' AND t_loc.key = s.city_slug AND t_loc.locale = $6
LEFT JOIN i18n_text t_en  ON t_en.namespace  = 'geo.cities' AND t_en.key  = s.city_slug AND t_en.locale  = 'en'
WHERE  ($5::text = ''
        OR ($5::text = 'none' AND s.verification_state IS NULL)
        OR s.verification_state = $5::text)
ORDER  BY s.created_at DESC, s.id DESC
LIMIT  $7 OFFSET $8`

// ListAdminSeatingPlans returns one page of the superadmin's list, newest
// first. state is "" (any), "none" (verification_state IS NULL) or a
// verification state. The city name is in locale, else English, else the
// city slug.
func (q *Queries) ListAdminSeatingPlans(ctx context.Context, f AdminSeatingPlanFilter, state, locale string, limit, offset int32) ([]AdminSeatingPlanRow, error) {
	rows, err := q.db.Query(ctx, listAdminSeatingPlans, f.OwnerOrgID, f.Country, f.CityID, f.VenueNamePart, state, locale, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminSeatingPlanRow
	for rows.Next() {
		var r AdminSeatingPlanRow
		if err := rows.Scan(&r.ID, &r.Name, &r.PlanType, &r.Visibility, &r.Status, &r.OwnerOrgID, &r.OrgName,
			&r.VenueID, &r.VenueName, &r.CityID, &r.CityName, &r.CountryIso2,
			&r.VerificationState, &r.VerificationNote, &r.VerifiedAt,
			&r.CurrentVersionID, &r.VersionNumber, &r.CapacitySeated, &r.CapacityStanding,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminSeatingPlanCounts is the number of plans in each verification state.
type AdminSeatingPlanCounts struct {
	None       int64
	Unverified int64
	NeedsFix   int64
	Verified   int64
}

const countAdminSeatingPlans = `-- name: CountAdminSeatingPlans :one
` + adminSeatingPlanScope + `
SELECT count(*) FILTER (WHERE verification_state IS NULL),
       count(*) FILTER (WHERE verification_state = 'unverified'),
       count(*) FILTER (WHERE verification_state = 'needs_fix'),
       count(*) FILTER (WHERE verification_state = 'verified')
FROM   scope`

// CountAdminSeatingPlans counts the plans of the filter by verification
// state, in one round.
func (q *Queries) CountAdminSeatingPlans(ctx context.Context, f AdminSeatingPlanFilter) (AdminSeatingPlanCounts, error) {
	var c AdminSeatingPlanCounts
	err := q.db.QueryRow(ctx, countAdminSeatingPlans, f.OwnerOrgID, f.Country, f.CityID, f.VenueNamePart).
		Scan(&c.None, &c.Unverified, &c.NeedsFix, &c.Verified)
	return c, err
}

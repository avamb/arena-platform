// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: venue_sharing.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Venue review statuses (migration 0172).
const (
	VenueReviewUnverified = "unverified"
	VenueReviewVerified   = "verified"
)

const listSharedVenuesForOrg = `-- name: ListSharedVenuesForOrg :many
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
LIMIT  $4`

// ListSharedVenuesForOrg returns the verified venues of other organizations,
// narrowed to a city and/or a country when given (country is upper-case
// ISO 3166-1 alpha-2, "" = any).
func (q *Queries) ListSharedVenuesForOrg(ctx context.Context, orgID uuid.UUID, cityID *uuid.UUID, country string, limit int32) ([]VenueRow, error) {
	rows, err := q.db.Query(ctx, listSharedVenuesForOrg, orgID, cityID, country, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var venues []VenueRow
	for rows.Next() {
		v, err := scanVenueRow(rows)
		if err != nil {
			return nil, err
		}
		venues = append(venues, v)
	}
	return venues, rows.Err()
}

// VenueReviewRow is the answer of SetVenueReviewStatus.
type VenueReviewRow struct {
	ID           uuid.UUID  `json:"id"`
	OrgID        uuid.UUID  `json:"org_id"`
	Name         string     `json:"name"`
	ReviewStatus string     `json:"review_status"`
	VerifiedAt   *time.Time `json:"verified_at"`
}

const setVenueReviewStatus = `-- name: SetVenueReviewStatus :one
UPDATE venues
SET    review_status = $2,
       verified_at   = CASE WHEN $2 = 'verified' THEN now() ELSE NULL END,
       updated_at    = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, org_id, name, review_status, verified_at`

// SetVenueReviewStatus verifies a venue or takes the verification back.
// pgx.ErrNoRows when the venue does not exist or is deleted.
func (q *Queries) SetVenueReviewStatus(ctx context.Context, id uuid.UUID, status string) (VenueReviewRow, error) {
	var r VenueReviewRow
	err := q.db.QueryRow(ctx, setVenueReviewStatus, id, status).
		Scan(&r.ID, &r.OrgID, &r.Name, &r.ReviewStatus, &r.VerifiedAt)
	return r, err
}

// VenueReviewState is the verification state of one venue (migration 0172).
type VenueReviewState struct {
	ReviewStatus string     `json:"review_status"`
	VerifiedAt   *time.Time `json:"verified_at"`
}

const listVenueReviewStates = `-- name: ListVenueReviewStates :many
SELECT id, review_status, verified_at
FROM   venues
WHERE  id = ANY($1::uuid[])`

// ListVenueReviewStates reads the verification state of the given venues in one
// round trip. VenueRow does not carry it (scanVenueRow feeds many queries), so
// the venue routes that show it hydrate it from here.
func (q *Queries) ListVenueReviewStates(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]VenueReviewState, error) {
	out := make(map[uuid.UUID]VenueReviewState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listVenueReviewStates, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var s VenueReviewState
		if err := rows.Scan(&id, &s.ReviewStatus, &s.VerifiedAt); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

const countForeignSessionsAtVenue = `-- name: CountForeignSessionsAtVenue :one
SELECT count(*)
FROM   sessions s
JOIN   events  e ON e.id = s.event_id
WHERE  s.venue_id = $1
  AND  s.deleted_at IS NULL
  AND  e.org_id <> $2`

// CountForeignSessionsAtVenue counts live sessions of other organizations held
// at the venue (a verified venue stays under the owner's hand but must not
// vanish under somebody else's dates).
func (q *Queries) CountForeignSessionsAtVenue(ctx context.Context, venueID, orgID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countForeignSessionsAtVenue, venueID, orgID).Scan(&n)
	return n, err
}

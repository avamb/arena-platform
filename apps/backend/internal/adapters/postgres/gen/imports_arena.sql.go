// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: imports_arena.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// FindActiveVenueByNormalizedName
// ─────────────────────────────────────────────────────────────────────────────

const findActiveVenueByNormalizedName = `-- name: FindActiveVenueByNormalizedName :one
SELECT id, display_number, org_id, city_id, name, address, capacity_default,
       created_at, updated_at, deleted_at
FROM   venues
WHERE  org_id = $1
  AND  lower(btrim(name)) = lower(btrim($2::text))
  AND  deleted_at IS NULL
ORDER BY created_at ASC, id ASC
LIMIT  1`

// FindActiveVenueByNormalizedName resolves an arena-native bundle's venueName
// onto an existing active venue of the same organization (event-bundle spec
// §3.2 step 4). Matching is case- and whitespace-insensitive on both sides.
// Returns pgx.ErrNoRows when the organization has no such venue, which the
// caller treats as "create it".
func (q *Queries) FindActiveVenueByNormalizedName(ctx context.Context, orgID uuid.UUID, name string) (VenueRow, error) {
	row := q.db.QueryRow(ctx, findActiveVenueByNormalizedName, orgID, name)
	return scanVenueRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertArenaVenue
// ─────────────────────────────────────────────────────────────────────────────

const insertArenaVenue = `-- name: InsertArenaVenue :one
INSERT INTO venues (
    org_id, city_id, name, address, timezone,
    geo_lat, geo_lng, country
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, display_number, org_id, city_id, name, address, capacity_default,
          created_at, updated_at, deleted_at`

// InsertArenaVenue creates the venue of an arena-native bundle. Unlike
// InsertImportedVenue it leaves external_bil24_id NULL: the venue has no Bil24
// identity, and that column's global partial-unique index must stay reserved
// for venues that genuinely came from Bil24. country is the ISO-3166-1 alpha-2
// code and must be upper-case to satisfy venues_country_check (migration 0050).
func (q *Queries) InsertArenaVenue(
	ctx context.Context,
	orgID uuid.UUID,
	cityID *uuid.UUID,
	name string,
	address *string,
	timezone string,
	geoLat, geoLng *float64,
	country *string,
) (VenueRow, error) {
	row := q.db.QueryRow(ctx, insertArenaVenue,
		orgID, cityID, name, address, timezone,
		geoLat, geoLng, country,
	)
	return scanVenueRow(row)
}

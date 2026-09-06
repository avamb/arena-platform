// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: imports_bil24.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetVenueByBil24ExternalID
// ─────────────────────────────────────────────────────────────────────────────

const getVenueByBil24ExternalID = `-- name: GetVenueByBil24ExternalID :one
SELECT id, display_number, org_id, city_id, name, address, capacity_default,
       created_at, updated_at, deleted_at
FROM   venues
WHERE  external_bil24_id = $1
  AND  deleted_at IS NULL`

// GetVenueByBil24ExternalID resolves a venue by its Bil24 source identity.
// venues.external_bil24_id carries a GLOBAL partial-unique index (migration
// 0073), so this lookup is intentionally not scoped by org_id — the caller
// compares VenueRow.OrgID itself and reports a cross-tenant conflict rather
// than letting a unique violation abort the import transaction.
// Returns pgx.ErrNoRows when no venue carries that external id.
func (q *Queries) GetVenueByBil24ExternalID(ctx context.Context, externalID string) (VenueRow, error) {
	row := q.db.QueryRow(ctx, getVenueByBil24ExternalID, externalID)
	return scanVenueRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetVenueImportContext
// ─────────────────────────────────────────────────────────────────────────────

// VenueImportContextRow is the narrow projection the Bil24 session import
// needs for an already-known venue: the owning org (tenant guard), the IANA
// timezone (used to interpret the payload's local day/time) and the current
// external identity.
type VenueImportContextRow struct {
	OrgID           uuid.UUID `json:"org_id"`
	Timezone        *string   `json:"timezone"`
	ExternalBil24ID *string   `json:"external_bil24_id"`
}

const getVenueImportContext = `-- name: GetVenueImportContext :one
SELECT org_id, timezone, external_bil24_id
FROM   venues
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetVenueImportContext returns the org, timezone and Bil24 external id for
// an active venue. Returns pgx.ErrNoRows when the venue does not exist or is
// soft-deleted.
func (q *Queries) GetVenueImportContext(ctx context.Context, id uuid.UUID) (VenueImportContextRow, error) {
	row := q.db.QueryRow(ctx, getVenueImportContext, id)
	var r VenueImportContextRow
	err := row.Scan(&r.OrgID, &r.Timezone, &r.ExternalBil24ID)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertImportedVenue
// ─────────────────────────────────────────────────────────────────────────────

const insertImportedVenue = `-- name: InsertImportedVenue :one
INSERT INTO venues (
    org_id, city_id, name, address, timezone,
    geo_lat, geo_lng, country, external_bil24_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, display_number, org_id, city_id, name, address, capacity_default,
          created_at, updated_at, deleted_at`

// InsertImportedVenue creates a venue carrying the full geography the Bil24
// session import supplies (timezone is mandatory upstream — the handler
// rejects a payload without one before reaching this query). country is the
// ISO-3166-1 alpha-2 code and must be upper-case to satisfy the
// venues_country_check constraint added by migration 0050.
func (q *Queries) InsertImportedVenue(
	ctx context.Context,
	orgID uuid.UUID,
	cityID *uuid.UUID,
	name string,
	address *string,
	timezone string,
	geoLat, geoLng *float64,
	country *string,
	externalID string,
) (VenueRow, error) {
	row := q.db.QueryRow(ctx, insertImportedVenue,
		orgID, cityID, name, address, timezone,
		geoLat, geoLng, country, externalID,
	)
	return scanVenueRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateImportedVenueGeography
// ─────────────────────────────────────────────────────────────────────────────

const updateImportedVenueGeography = `-- name: UpdateImportedVenueGeography :exec
UPDATE venues
SET    city_id    = COALESCE($2::uuid, city_id),
       address    = COALESCE($3::text, address),
       timezone   = COALESCE(NULLIF($4::text, ''), timezone),
       geo_lat    = COALESCE($5::numeric, geo_lat),
       geo_lng    = COALESCE($6::numeric, geo_lng),
       country    = COALESCE($7::text, country),
       updated_at = now()
WHERE  id = $1
  AND  deleted_at IS NULL`

// UpdateImportedVenueGeography refreshes the geography fields of a venue on a
// repeat import. Every field is COALESCE-guarded: a nil/empty payload value
// leaves the stored value untouched, so a partial Bil24 payload can never
// erase geography that a previous richer import established.
func (q *Queries) UpdateImportedVenueGeography(
	ctx context.Context,
	id uuid.UUID,
	cityID *uuid.UUID,
	address *string,
	timezone string,
	geoLat, geoLng *float64,
	country *string,
) error {
	_, err := q.db.Exec(ctx, updateImportedVenueGeography,
		id, cityID, address, timezone, geoLat, geoLng, country,
	)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetEventByBil24ExternalID
// ─────────────────────────────────────────────────────────────────────────────

const getEventByBil24ExternalID = `-- name: GetEventByBil24ExternalID :one
SELECT id, display_number, org_id, name, description, status, first_session_at,
       last_session_at, visibility, image_url, poster_media_id, slug,
       short_description, genre, age_rating, duration_minutes, teaser_url,
       trailer_url, meta_description, meta_keywords, created_at, updated_at,
       deleted_at
FROM   events
WHERE  external_bil24_id = $1
  AND  deleted_at IS NULL`

// GetEventByBil24ExternalID resolves an event by its Bil24 source identity.
// events.external_bil24_id carries a GLOBAL partial-unique index (migration
// 0070); see GetVenueByBil24ExternalID for why this is not org-scoped.
// Returns pgx.ErrNoRows when no event carries that external id.
func (q *Queries) GetEventByBil24ExternalID(ctx context.Context, externalID string) (EventRow, error) {
	row := q.db.QueryRow(ctx, getEventByBil24ExternalID, externalID)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SetEventBil24ExternalID
// ─────────────────────────────────────────────────────────────────────────────

const setEventBil24ExternalID = `-- name: SetEventBil24ExternalID :exec
UPDATE events
SET    external_bil24_id = $3,
       updated_at        = now()
WHERE  id     = $1
  AND  org_id = $2
  AND  deleted_at IS NULL`

// SetEventBil24ExternalID stamps the Bil24 source identity onto an event.
// The caller MUST have verified via GetEventByBil24ExternalID that no other
// event already holds the id — the global partial-unique index would
// otherwise raise 23505 and abort the surrounding import transaction.
func (q *Queries) SetEventBil24ExternalID(ctx context.Context, id, orgID uuid.UUID, externalID string) error {
	_, err := q.db.Exec(ctx, setEventBil24ExternalID, id, orgID, externalID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// SetEventPosterMediaID
// ─────────────────────────────────────────────────────────────────────────────

const setEventPosterMediaID = `-- name: SetEventPosterMediaID :exec
UPDATE events
SET    poster_media_id = $3,
       updated_at      = now()
WHERE  id     = $1
  AND  org_id = $2
  AND  deleted_at IS NULL`

// SetEventPosterMediaID attaches a side-loaded poster media object to an
// event. Scoped by org_id to enforce the tenant boundary.
func (q *Queries) SetEventPosterMediaID(ctx context.Context, id, orgID uuid.UUID, posterMediaID *uuid.UUID) error {
	_, err := q.db.Exec(ctx, setEventPosterMediaID, id, orgID, posterMediaID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// SetEventImportMetadata
// ─────────────────────────────────────────────────────────────────────────────

const setEventImportMetadata = `-- name: SetEventImportMetadata :exec
UPDATE events
SET    name        = COALESCE(NULLIF($3::text, ''), name),
       description = COALESCE($4::text, description),
       age_rating  = COALESCE($5::text, age_rating),
       updated_at  = now()
WHERE  id     = $1
  AND  org_id = $2
  AND  deleted_at IS NULL`

// SetEventImportMetadata refreshes the descriptive fields a repeat Bil24
// import can carry. Empty/nil values leave the stored value untouched.
func (q *Queries) SetEventImportMetadata(ctx context.Context, id, orgID uuid.UUID, name string, description, ageRating *string) error {
	_, err := q.db.Exec(ctx, setEventImportMetadata, id, orgID, name, description, ageRating)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionImportContext
// ─────────────────────────────────────────────────────────────────────────────

// SessionImportContextRow is the narrow projection the Bil24 session import
// needs when a session already exists: enough to enforce the tenant boundary
// and to decide whether the currency may still be changed.
type SessionImportContextRow struct {
	ID             uuid.UUID `json:"id"`
	EventID        uuid.UUID `json:"event_id"`
	VenueID        uuid.UUID `json:"venue_id"`
	Currency       string    `json:"currency"`
	CurrencySource string    `json:"currency_source"`
	Status         string    `json:"status"`
	AdmissionMode  string    `json:"admission_mode"`
	OrgID          uuid.UUID `json:"org_id"`
}

const getSessionImportContext = `-- name: GetSessionImportContext :one
SELECT s.id, s.event_id, s.venue_id, s.currency, s.currency_source, s.status,
       s.admission_mode, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.id = $1
  AND  s.deleted_at IS NULL`

// GetSessionImportContext returns the import-relevant projection of an active
// session joined to its owning organization. Returns pgx.ErrNoRows when the
// session does not exist or is soft-deleted.
func (q *Queries) GetSessionImportContext(ctx context.Context, id uuid.UUID) (SessionImportContextRow, error) {
	row := q.db.QueryRow(ctx, getSessionImportContext, id)
	var r SessionImportContextRow
	err := row.Scan(
		&r.ID, &r.EventID, &r.VenueID, &r.Currency, &r.CurrencySource,
		&r.Status, &r.AdmissionMode, &r.OrgID,
	)
	return r, err
}

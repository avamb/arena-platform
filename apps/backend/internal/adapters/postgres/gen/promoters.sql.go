// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: promoters.sql
//
// Promoters of an organization's events (migration 0113). The organization
// selling an event is often, but not always, its promoter: org_promoters is
// the organization's own list of partners, and event_promoters links an event
// to one of them. No event_promoters row means the organization itself is the
// promoter.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrgPromoterRow is one org_promoters row.
type OrgPromoterRow struct {
	ID      uuid.UUID `json:"id"`
	OrgID   uuid.UUID `json:"org_id"`
	Name    string    `json:"name"`
	LegalID *string   `json:"legal_id"`
	Phone   *string   `json:"phone"`
	Email   *string   `json:"email"`
	// Slug is the promoter's public page address (migration 0117); nil =
	// no page yet.
	Slug       *string    `json:"slug"`
	ArchivedAt *time.Time `json:"archived_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const orgPromoterColumns = `id, org_id, name, legal_id, phone, email, slug, archived_at, created_at, updated_at`

func scanOrgPromoterRow(row interface{ Scan(dest ...any) error }) (OrgPromoterRow, error) {
	var p OrgPromoterRow
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.LegalID, &p.Phone, &p.Email, &p.Slug, &p.ArchivedAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListOrgPromoters
// ─────────────────────────────────────────────────────────────────────────────

const listOrgPromoters = `-- name: ListOrgPromoters :many
SELECT ` + orgPromoterColumns + `
FROM   org_promoters
WHERE  org_id = $1
  AND  ($2::boolean OR archived_at IS NULL)
ORDER  BY lower(name), id`

// ListOrgPromoters returns the organization's promoters ordered by name.
// Archived promoters are included only when includeArchived is true.
func (q *Queries) ListOrgPromoters(ctx context.Context, orgID uuid.UUID, includeArchived bool) ([]OrgPromoterRow, error) {
	rows, err := q.db.Query(ctx, listOrgPromoters, orgID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgPromoterRow
	for rows.Next() {
		p, err := scanOrgPromoterRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// GetOrgPromoter
// ─────────────────────────────────────────────────────────────────────────────

const getOrgPromoter = `-- name: GetOrgPromoter :one
SELECT ` + orgPromoterColumns + `
FROM   org_promoters
WHERE  id = $1
  AND  org_id = $2`

// GetOrgPromoter fetches one promoter of the organization, archived or not.
// Returns pgx.ErrNoRows for an unknown id or another organization's promoter.
func (q *Queries) GetOrgPromoter(ctx context.Context, id, orgID uuid.UUID) (OrgPromoterRow, error) {
	return scanOrgPromoterRow(q.db.QueryRow(ctx, getOrgPromoter, id, orgID))
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertOrgPromoter
// ─────────────────────────────────────────────────────────────────────────────

const insertOrgPromoter = `-- name: InsertOrgPromoter :one
INSERT INTO org_promoters (org_id, name, legal_id, phone, email, slug)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING ` + orgPromoterColumns

// InsertOrgPromoter creates a promoter. A name already used by an active
// promoter of the same organization (case- and edge-whitespace-insensitive)
// fails with 23505 on org_promoters_active_name_uq.
func (q *Queries) InsertOrgPromoter(ctx context.Context, orgID uuid.UUID, name string, legalID, phone, email, slug *string) (OrgPromoterRow, error) {
	return scanOrgPromoterRow(q.db.QueryRow(ctx, insertOrgPromoter, orgID, name, legalID, phone, email, slug))
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateOrgPromoter
// ─────────────────────────────────────────────────────────────────────────────

const updateOrgPromoter = `-- name: UpdateOrgPromoter :one
UPDATE org_promoters
SET    name        = $3,
       legal_id    = $4,
       phone       = $5,
       email       = $6,
       archived_at = CASE WHEN $7::boolean THEN COALESCE(archived_at, now()) ELSE NULL END,
       slug        = $8,
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING ` + orgPromoterColumns

// UpdateOrgPromoter writes the complete, already-merged state of a promoter
// (the handler resolves the PATCH tri-state against the current row first).
// archived=true keeps an existing archived_at; false clears it. Returns
// pgx.ErrNoRows for an unknown id or another organization's promoter.
func (q *Queries) UpdateOrgPromoter(ctx context.Context, id, orgID uuid.UUID, name string, legalID, phone, email *string, archived bool, slug *string) (OrgPromoterRow, error) {
	return scanOrgPromoterRow(q.db.QueryRow(ctx, updateOrgPromoter, id, orgID, name, legalID, phone, email, archived, slug))
}

const promoterSlugTaken = `-- name: PromoterSlugTaken :one
SELECT EXISTS (SELECT 1 FROM org_promoters WHERE lower(slug) = lower($1))
    OR EXISTS (SELECT 1 FROM organizations WHERE lower(slug) = lower($1) AND deleted_at IS NULL)`

// PromoterSlugTaken reports whether slug is already used by a promoter or
// by an organization — the two share the public page namespace
// (migration 0117), so a promoter may never shadow an organization's page.
func (q *Queries) PromoterSlugTaken(ctx context.Context, slug string) (bool, error) {
	var taken bool
	err := q.db.QueryRow(ctx, promoterSlugTaken, slug).Scan(&taken)
	return taken, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Event ↔ promoter link
// ─────────────────────────────────────────────────────────────────────────────

// EventPromoterRef is the promoter linked to an event.
type EventPromoterRef struct {
	PromoterID uuid.UUID `json:"promoter_id"`
	Name       string    `json:"name"`
}

const setEventPromoter = `-- name: SetEventPromoter :exec
INSERT INTO event_promoters (event_id, org_id, promoter_id)
VALUES ($1, $2, $3)
ON CONFLICT (event_id) DO UPDATE
SET    promoter_id = EXCLUDED.promoter_id,
       org_id      = EXCLUDED.org_id,
       updated_at  = now()`

// SetEventPromoter links the event to the promoter, replacing any previous
// link. Both foreign keys carry org_id, so a promoter of another
// organization fails with 23503.
func (q *Queries) SetEventPromoter(ctx context.Context, eventID, orgID, promoterID uuid.UUID) error {
	_, err := q.db.Exec(ctx, setEventPromoter, eventID, orgID, promoterID)
	return err
}

const deleteEventPromoter = `-- name: DeleteEventPromoter :exec
DELETE FROM event_promoters WHERE event_id = $1 AND org_id = $2`

// DeleteEventPromoter removes the event's promoter link, making the
// organization itself the promoter again. Deleting a missing link is a no-op.
func (q *Queries) DeleteEventPromoter(ctx context.Context, eventID, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteEventPromoter, eventID, orgID)
	return err
}

const listEventPromoters = `-- name: ListEventPromoters :many
SELECT ep.event_id, p.id, p.name
FROM   event_promoters ep
JOIN   org_promoters   p ON p.id = ep.promoter_id
WHERE  ep.event_id = ANY($1::uuid[])`

// ListEventPromoters returns the linked promoter of each given event that
// has one, keyed by event id. Events without a link are absent from the map
// (their organization is the promoter).
func (q *Queries) ListEventPromoters(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]EventPromoterRef, error) {
	out := make(map[uuid.UUID]EventPromoterRef, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listEventPromoters, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eventID uuid.UUID
		var ref EventPromoterRef
		if err := rows.Scan(&eventID, &ref.PromoterID, &ref.Name); err != nil {
			return nil, err
		}
		out[eventID] = ref
	}
	return out, rows.Err()
}

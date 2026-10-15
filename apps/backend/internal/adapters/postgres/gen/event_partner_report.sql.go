// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: event_partner_report.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const getEventOrgForMaterials = `-- name: GetEventOrgForMaterials :one
SELECT org_id FROM events WHERE id = $1 AND deleted_at IS NULL`

// GetEventOrgForMaterials returns the organization of an active (not deleted)
// event; pgx.ErrNoRows otherwise.
func (q *Queries) GetEventOrgForMaterials(ctx context.Context, eventID uuid.UUID) (uuid.UUID, error) {
	var orgID uuid.UUID
	err := q.db.QueryRow(ctx, getEventOrgForMaterials, eventID).Scan(&orgID)
	return orgID, err
}

const lockEventExtras = `-- name: LockEventExtras :exec
SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0))`

// LockEventExtras serializes the writers of one list of one event (kind is
// "partner_links" or "report_photos") until the transaction ends. It uses the
// same key scheme as LockEventMaterials of task 1-2a and must be the first
// statement of such a transaction.
func (q *Queries) LockEventExtras(ctx context.Context, eventID uuid.UUID, kind string) error {
	_, err := q.db.Exec(ctx, lockEventExtras, eventID.String(), kind)
	return err
}

// ── 1-2b partner links ───────────────────────────────────────────────────

// EventPartnerLinkRow is one "where else to buy" link of an event.
type EventPartnerLinkRow struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	EventID    uuid.UUID
	URL        string
	SellerName string
	Position   int16
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const partnerLinkColumns = `id, org_id, event_id, url, seller_name, position, created_at, updated_at`

func scanPartnerLink(row interface{ Scan(...any) error }) (EventPartnerLinkRow, error) {
	var r EventPartnerLinkRow
	err := row.Scan(&r.ID, &r.OrgID, &r.EventID, &r.URL, &r.SellerName, &r.Position, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const listEventPartnerLinks = `-- name: ListEventPartnerLinks :many
SELECT ` + partnerLinkColumns + `
FROM event_partner_links
WHERE event_id = $1
ORDER BY position`

// ListEventPartnerLinks lists an event's links in display order.
func (q *Queries) ListEventPartnerLinks(ctx context.Context, eventID uuid.UUID) ([]EventPartnerLinkRow, error) {
	rows, err := q.db.Query(ctx, listEventPartnerLinks, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventPartnerLinkRow{}
	for rows.Next() {
		r, err := scanPartnerLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getEventPartnerLink = `-- name: GetEventPartnerLink :one
SELECT ` + partnerLinkColumns + `
FROM event_partner_links
WHERE id = $1 AND event_id = $2`

// GetEventPartnerLink reads one link of the event; pgx.ErrNoRows when none.
func (q *Queries) GetEventPartnerLink(ctx context.Context, id, eventID uuid.UUID) (EventPartnerLinkRow, error) {
	return scanPartnerLink(q.db.QueryRow(ctx, getEventPartnerLink, id, eventID))
}

const insertEventPartnerLink = `-- name: InsertEventPartnerLink :one
INSERT INTO event_partner_links (org_id, event_id, url, seller_name, position)
SELECT $1::uuid, $2::uuid, $3::text, $4::text, p.pos::smallint
FROM generate_series(0, $5::int - 1) AS p(pos)
WHERE NOT EXISTS (
    SELECT 1 FROM event_partner_links x WHERE x.event_id = $2::uuid AND x.position = p.pos
)
ORDER BY p.pos
LIMIT 1
RETURNING ` + partnerLinkColumns

// InsertEventPartnerLink stores a link at the lowest free position below
// limit. pgx.ErrNoRows means the event already holds limit links; a 23505
// on event_partner_links_position_uq means another request took the same
// position first.
func (q *Queries) InsertEventPartnerLink(ctx context.Context, orgID, eventID uuid.UUID, url, sellerName string, limit int32) (EventPartnerLinkRow, error) {
	return scanPartnerLink(q.db.QueryRow(ctx, insertEventPartnerLink, orgID, eventID, url, sellerName, limit))
}

const updateEventPartnerLink = `-- name: UpdateEventPartnerLink :one
UPDATE event_partner_links
SET url = $3, seller_name = $4, updated_at = now()
WHERE id = $1 AND event_id = $2
RETURNING ` + partnerLinkColumns

// UpdateEventPartnerLink rewrites url and seller_name; pgx.ErrNoRows when
// the link is not the event's.
func (q *Queries) UpdateEventPartnerLink(ctx context.Context, id, eventID uuid.UUID, url, sellerName string) (EventPartnerLinkRow, error) {
	return scanPartnerLink(q.db.QueryRow(ctx, updateEventPartnerLink, id, eventID, url, sellerName))
}

const deleteEventPartnerLink = `-- name: DeleteEventPartnerLink :execrows
DELETE FROM event_partner_links WHERE id = $1 AND event_id = $2`

// DeleteEventPartnerLink removes a link and reports whether one was removed.
func (q *Queries) DeleteEventPartnerLink(ctx context.Context, id, eventID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, deleteEventPartnerLink, id, eventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const compactEventPartnerLinks = `-- name: CompactEventPartnerLinks :exec
UPDATE event_partner_links t
SET position = r.rn - 1, updated_at = now()
FROM (
    SELECT id, (row_number() OVER (ORDER BY position))::int AS rn
    FROM event_partner_links WHERE event_id = $1
) r
WHERE t.id = r.id AND t.position <> r.rn - 1`

// CompactEventPartnerLinks closes the gap a delete left (positions 0..n-1).
func (q *Queries) CompactEventPartnerLinks(ctx context.Context, eventID uuid.UUID) error {
	_, err := q.db.Exec(ctx, compactEventPartnerLinks, eventID)
	return err
}

const reorderEventPartnerLinks = `-- name: ReorderEventPartnerLinks :execrows
UPDATE event_partner_links t
SET position = (v.ord - 1)::smallint, updated_at = now()
FROM unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE t.id = v.id AND t.event_id = $1`

// ReorderEventPartnerLinks sets the positions of the listed ids to their
// index in ids, in ONE statement (the unique constraint is checked at its
// end). It returns the number of rows moved.
func (q *Queries) ReorderEventPartnerLinks(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, reorderEventPartnerLinks, eventID, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ── 1-2b map point ───────────────────────────────────────────────────────

// EventMapPointRow is the one map point of an event.
type EventMapPointRow struct {
	EventID   uuid.UUID
	OrgID     uuid.UUID
	Latitude  float64
	Longitude float64
	Label     *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const mapPointColumns = `event_id, org_id, latitude::float8, longitude::float8, label, created_at, updated_at`

func scanMapPoint(row interface{ Scan(...any) error }) (EventMapPointRow, error) {
	var r EventMapPointRow
	err := row.Scan(&r.EventID, &r.OrgID, &r.Latitude, &r.Longitude, &r.Label, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const getEventMapPoint = `-- name: GetEventMapPoint :one
SELECT ` + mapPointColumns + `
FROM event_map_points
WHERE event_id = $1`

// GetEventMapPoint reads the event's map point; pgx.ErrNoRows when none.
func (q *Queries) GetEventMapPoint(ctx context.Context, eventID uuid.UUID) (EventMapPointRow, error) {
	return scanMapPoint(q.db.QueryRow(ctx, getEventMapPoint, eventID))
}

const upsertEventMapPoint = `-- name: UpsertEventMapPoint :one
INSERT INTO event_map_points (event_id, org_id, latitude, longitude, label)
VALUES ($1, $2, $3::numeric, $4::numeric, $5)
ON CONFLICT (event_id) DO UPDATE
SET latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude,
    label = EXCLUDED.label, updated_at = now()
RETURNING ` + mapPointColumns

// UpsertEventMapPoint creates or replaces the event's map point. The
// coordinates travel as decimal strings so numeric(9,6) rounds them itself.
func (q *Queries) UpsertEventMapPoint(ctx context.Context, eventID, orgID uuid.UUID, latitude, longitude string, label *string) (EventMapPointRow, error) {
	return scanMapPoint(q.db.QueryRow(ctx, upsertEventMapPoint, eventID, orgID, latitude, longitude, label))
}

const deleteEventMapPoint = `-- name: DeleteEventMapPoint :execrows
DELETE FROM event_map_points WHERE event_id = $1`

// DeleteEventMapPoint removes the point and reports whether one existed.
func (q *Queries) DeleteEventMapPoint(ctx context.Context, eventID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, deleteEventMapPoint, eventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ── 1-2b photo report ────────────────────────────────────────────────────

// EventReportPhotoRow is one photo of an event's photo report.
type EventReportPhotoRow struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	EventID   uuid.UUID
	MediaID   uuid.UUID
	Position  int16
	Caption   *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const reportPhotoColumns = `id, org_id, event_id, media_id, position, caption, created_at, updated_at`

func scanReportPhoto(row interface{ Scan(...any) error }) (EventReportPhotoRow, error) {
	var r EventReportPhotoRow
	err := row.Scan(&r.ID, &r.OrgID, &r.EventID, &r.MediaID, &r.Position, &r.Caption, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const isEventReportMedia = `-- name: IsEventReportMedia :one
SELECT EXISTS (
    SELECT 1 FROM media_objects
    WHERE id = $1 AND org_id = $2 AND owner_type = 'event_report' AND deleted_at IS NULL
)`

// IsEventReportMedia reports whether the media object is an active
// event_report object of the organization.
func (q *Queries) IsEventReportMedia(ctx context.Context, mediaID, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, isEventReportMedia, mediaID, orgID).Scan(&ok)
	return ok, err
}

func (q *Queries) queryReportPhotos(ctx context.Context, sql string, args ...any) ([]EventReportPhotoRow, error) {
	rows, err := q.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventReportPhotoRow{}
	for rows.Next() {
		r, err := scanReportPhoto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listEventReportPhotos = `-- name: ListEventReportPhotos :many
SELECT ` + reportPhotoColumns + `
FROM event_report_photos
WHERE event_id = $1
ORDER BY position`

// ListEventReportPhotos is the organizer's view: every photo, always.
func (q *Queries) ListEventReportPhotos(ctx context.Context, eventID uuid.UUID) ([]EventReportPhotoRow, error) {
	return q.queryReportPhotos(ctx, listEventReportPhotos, eventID)
}

const listEventReportPhotosPublic = `-- name: ListEventReportPhotosPublic :many
SELECT p.id, p.org_id, p.event_id, p.media_id, p.position, p.caption, p.created_at, p.updated_at
FROM event_report_photos p
JOIN events e ON e.id = p.event_id AND e.deleted_at IS NULL
WHERE p.event_id = $1
  AND e.last_session_at IS NOT NULL
  AND e.last_session_at <= $2
ORDER BY p.position`

// ListEventReportPhotosPublic is the buyers' view: the photo report stays
// hidden until the event is over. events.last_session_at is the end of the
// last live (not deleted, not cancelled) session, so the report opens when
// the event has ended, follows a session that moves, and stays hidden for an
// event with no live session. now is passed in so the rule is testable.
func (q *Queries) ListEventReportPhotosPublic(ctx context.Context, eventID uuid.UUID, now time.Time) ([]EventReportPhotoRow, error) {
	return q.queryReportPhotos(ctx, listEventReportPhotosPublic, eventID, now)
}

const getEventReportPhoto = `-- name: GetEventReportPhoto :one
SELECT ` + reportPhotoColumns + `
FROM event_report_photos
WHERE id = $1 AND event_id = $2`

// GetEventReportPhoto reads one photo of the event; pgx.ErrNoRows when none.
func (q *Queries) GetEventReportPhoto(ctx context.Context, id, eventID uuid.UUID) (EventReportPhotoRow, error) {
	return scanReportPhoto(q.db.QueryRow(ctx, getEventReportPhoto, id, eventID))
}

const insertEventReportPhoto = `-- name: InsertEventReportPhoto :one
INSERT INTO event_report_photos (org_id, event_id, media_id, caption, position)
SELECT $1::uuid, $2::uuid, $3::uuid, $4::text, p.pos::smallint
FROM generate_series(0, $5::int - 1) AS p(pos)
WHERE NOT EXISTS (
    SELECT 1 FROM event_report_photos x WHERE x.event_id = $2::uuid AND x.position = p.pos
)
ORDER BY p.pos
LIMIT 1
RETURNING ` + reportPhotoColumns

// InsertEventReportPhoto stores a photo at the lowest free position below
// limit. pgx.ErrNoRows means the report is full; 23505 names either
// event_report_photos_position_uq (a lost race) or event_report_photos_media_uq
// (the same media twice).
func (q *Queries) InsertEventReportPhoto(ctx context.Context, orgID, eventID, mediaID uuid.UUID, caption *string, limit int32) (EventReportPhotoRow, error) {
	return scanReportPhoto(q.db.QueryRow(ctx, insertEventReportPhoto, orgID, eventID, mediaID, caption, limit))
}

const updateEventReportPhotoCaption = `-- name: UpdateEventReportPhotoCaption :one
UPDATE event_report_photos
SET caption = $3, updated_at = now()
WHERE id = $1 AND event_id = $2
RETURNING ` + reportPhotoColumns

// UpdateEventReportPhotoCaption sets (or, with nil, clears) a caption.
func (q *Queries) UpdateEventReportPhotoCaption(ctx context.Context, id, eventID uuid.UUID, caption *string) (EventReportPhotoRow, error) {
	return scanReportPhoto(q.db.QueryRow(ctx, updateEventReportPhotoCaption, id, eventID, caption))
}

const deleteEventReportPhoto = `-- name: DeleteEventReportPhoto :execrows
DELETE FROM event_report_photos WHERE id = $1 AND event_id = $2`

// DeleteEventReportPhoto removes a photo and reports whether one was removed.
func (q *Queries) DeleteEventReportPhoto(ctx context.Context, id, eventID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, deleteEventReportPhoto, id, eventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const compactEventReportPhotos = `-- name: CompactEventReportPhotos :exec
UPDATE event_report_photos t
SET position = r.rn - 1, updated_at = now()
FROM (
    SELECT id, (row_number() OVER (ORDER BY position))::int AS rn
    FROM event_report_photos WHERE event_id = $1
) r
WHERE t.id = r.id AND t.position <> r.rn - 1`

// CompactEventReportPhotos closes the gap a delete left (positions 0..n-1).
func (q *Queries) CompactEventReportPhotos(ctx context.Context, eventID uuid.UUID) error {
	_, err := q.db.Exec(ctx, compactEventReportPhotos, eventID)
	return err
}

const reorderEventReportPhotos = `-- name: ReorderEventReportPhotos :execrows
UPDATE event_report_photos t
SET position = (v.ord - 1)::smallint, updated_at = now()
FROM unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE t.id = v.id AND t.event_id = $1`

// ReorderEventReportPhotos sets the positions of the listed ids to their
// index in ids, in ONE statement. It returns the number of rows moved.
func (q *Queries) ReorderEventReportPhotos(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, reorderEventReportPhotos, eventID, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

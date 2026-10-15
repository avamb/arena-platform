// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: event_materials.sql (1-2a block)
//
// Gallery photos and video links of an event (migration 0153, tasks 1-2a).
// Both tables cap their rows by position (CHECK + UNIQUE (event_id,
// position) DEFERRABLE), which cannot be an ON CONFLICT arbiter: an insert
// picks the lowest free position itself and pgx.ErrNoRows means "full". Every
// write for one event runs under LockEventMaterials.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EventGalleryItemRow is one event_gallery_items row.
type EventGalleryItemRow struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	EventID   uuid.UUID `json:"event_id"`
	MediaID   uuid.UUID `json:"media_id"`
	Position  int16     `json:"position"`
	Caption   *string   `json:"caption"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const eventGalleryItemColumns = `id, org_id, event_id, media_id, position, caption, created_at, updated_at`

func scanEventGalleryItemRow(row interface{ Scan(dest ...any) error }) (EventGalleryItemRow, error) {
	var g EventGalleryItemRow
	err := row.Scan(&g.ID, &g.OrgID, &g.EventID, &g.MediaID, &g.Position, &g.Caption, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

// EventVideoRow is one event_videos row.
type EventVideoRow struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	EventID   uuid.UUID `json:"event_id"`
	Provider  string    `json:"provider"`
	VideoID   *string   `json:"video_id"`
	URL       string    `json:"url"`
	Position  int16     `json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const eventVideoColumns = `id, org_id, event_id, provider, video_id, url, position, created_at, updated_at`

func scanEventVideoRow(row interface{ Scan(dest ...any) error }) (EventVideoRow, error) {
	var v EventVideoRow
	err := row.Scan(&v.ID, &v.OrgID, &v.EventID, &v.Provider, &v.VideoID, &v.URL, &v.Position, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// MediaObjectOwnership is what a material needs to know about a media object.
type MediaObjectOwnership struct {
	OrgID     *uuid.UUID
	OwnerType string
	DeletedAt *time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// LockEventMaterials
// ─────────────────────────────────────────────────────────────────────────────

const lockEventMaterials = `-- name: LockEventMaterials :exec
SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0))`

// LockEventMaterials serializes the writers of one kind of material of one
// event (kind is "gallery" or "videos") until the transaction ends, so a
// free-position pick, a delete with its compaction and a reorder never
// interleave. It must be the first statement of such a transaction.
func (q *Queries) LockEventMaterials(ctx context.Context, eventID uuid.UUID, kind string) error {
	_, err := q.db.Exec(ctx, lockEventMaterials, eventID.String(), kind)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetMediaObjectOwnership
// ─────────────────────────────────────────────────────────────────────────────

const getMediaObjectOwnership = `-- name: GetMediaObjectOwnership :one
SELECT org_id, owner_type, deleted_at
FROM   media_objects
WHERE  id = $1`

// GetMediaObjectOwnership reads the organization, owner type and deletion
// mark of a media object. Returns pgx.ErrNoRows for an unknown id.
func (q *Queries) GetMediaObjectOwnership(ctx context.Context, id uuid.UUID) (MediaObjectOwnership, error) {
	var m MediaObjectOwnership
	err := q.db.QueryRow(ctx, getMediaObjectOwnership, id).Scan(&m.OrgID, &m.OwnerType, &m.DeletedAt)
	return m, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Gallery
// ─────────────────────────────────────────────────────────────────────────────

const listEventGalleryItems = `-- name: ListEventGalleryItems :many
SELECT ` + eventGalleryItemColumns + `
FROM   event_gallery_items
WHERE  event_id = $1
ORDER  BY position`

// ListEventGalleryItems returns the event's gallery photos in display order.
func (q *Queries) ListEventGalleryItems(ctx context.Context, eventID uuid.UUID) ([]EventGalleryItemRow, error) {
	rows, err := q.db.Query(ctx, listEventGalleryItems, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventGalleryItemRow{}
	for rows.Next() {
		g, err := scanEventGalleryItemRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const getEventGalleryItem = `-- name: GetEventGalleryItem :one
SELECT ` + eventGalleryItemColumns + `
FROM   event_gallery_items
WHERE  id = $1
  AND  event_id = $2`

// GetEventGalleryItem fetches one gallery photo of the event. Returns
// pgx.ErrNoRows for an unknown id or one that belongs to another event.
func (q *Queries) GetEventGalleryItem(ctx context.Context, id, eventID uuid.UUID) (EventGalleryItemRow, error) {
	return scanEventGalleryItemRow(q.db.QueryRow(ctx, getEventGalleryItem, id, eventID))
}

const insertEventGalleryItem = `-- name: InsertEventGalleryItem :one
INSERT INTO event_gallery_items (org_id, event_id, media_id, position, caption)
SELECT $1::uuid, $2::uuid, $3::uuid, s.pos::smallint, $4::text
FROM   generate_series(0, $5::int) AS s(pos)
WHERE  NOT EXISTS (SELECT 1 FROM event_gallery_items g WHERE g.event_id = $2::uuid AND g.position = s.pos)
ORDER  BY s.pos
LIMIT  1
RETURNING ` + eventGalleryItemColumns

// InsertEventGalleryItem adds a photo at the lowest free position 0..maxPosition.
// It returns pgx.ErrNoRows when every position is taken (the gallery is full).
// A media id that is already in the gallery fails with 23505 on
// event_gallery_items_media_uq.
func (q *Queries) InsertEventGalleryItem(ctx context.Context, orgID, eventID, mediaID uuid.UUID, caption *string, maxPosition int32) (EventGalleryItemRow, error) {
	return scanEventGalleryItemRow(q.db.QueryRow(ctx, insertEventGalleryItem, orgID, eventID, mediaID, caption, maxPosition))
}

const updateEventGalleryItemCaption = `-- name: UpdateEventGalleryItemCaption :one
UPDATE event_gallery_items
SET    caption    = $3,
       updated_at = now()
WHERE  id = $1
  AND  event_id = $2
RETURNING ` + eventGalleryItemColumns

// UpdateEventGalleryItemCaption sets or clears (nil) a photo's caption.
func (q *Queries) UpdateEventGalleryItemCaption(ctx context.Context, id, eventID uuid.UUID, caption *string) (EventGalleryItemRow, error) {
	return scanEventGalleryItemRow(q.db.QueryRow(ctx, updateEventGalleryItemCaption, id, eventID, caption))
}

const deleteEventGalleryItem = `-- name: DeleteEventGalleryItem :one
DELETE FROM event_gallery_items
WHERE  id = $1
  AND  event_id = $2
RETURNING ` + eventGalleryItemColumns

// DeleteEventGalleryItem removes a photo and returns the deleted row. The
// media object itself is left to the media GC.
func (q *Queries) DeleteEventGalleryItem(ctx context.Context, id, eventID uuid.UUID) (EventGalleryItemRow, error) {
	return scanEventGalleryItemRow(q.db.QueryRow(ctx, deleteEventGalleryItem, id, eventID))
}

const compactEventGalleryPositions = `-- name: CompactEventGalleryPositions :exec
UPDATE event_gallery_items g
SET    position = (r.rn - 1)::smallint
FROM  (SELECT id, row_number() OVER (ORDER BY position) AS rn
       FROM   event_gallery_items
       WHERE  event_id = $1) r
WHERE  g.id = r.id
  AND  g.position <> (r.rn - 1)::smallint`

// CompactEventGalleryPositions closes the hole a delete left, in ONE statement,
// so the next insert appends at the end.
func (q *Queries) CompactEventGalleryPositions(ctx context.Context, eventID uuid.UUID) error {
	_, err := q.db.Exec(ctx, compactEventGalleryPositions, eventID)
	return err
}

const reorderEventGalleryItems = `-- name: ReorderEventGalleryItems :execrows
UPDATE event_gallery_items g
SET    position   = (v.ord - 1)::smallint,
       updated_at = now()
FROM   unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE  g.id = v.id
  AND  g.event_id = $1`

// ReorderEventGalleryItems gives the photos the positions of their place in
// ids, in ONE statement, and returns how many rows were updated (the caller
// compares it with len(ids)).
func (q *Queries) ReorderEventGalleryItems(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, reorderEventGalleryItems, eventID, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Videos
// ─────────────────────────────────────────────────────────────────────────────

const listEventVideos = `-- name: ListEventVideos :many
SELECT ` + eventVideoColumns + `
FROM   event_videos
WHERE  event_id = $1
ORDER  BY position`

// ListEventVideos returns the event's video links in display order.
func (q *Queries) ListEventVideos(ctx context.Context, eventID uuid.UUID) ([]EventVideoRow, error) {
	rows, err := q.db.Query(ctx, listEventVideos, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventVideoRow{}
	for rows.Next() {
		v, err := scanEventVideoRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const insertEventVideo = `-- name: InsertEventVideo :one
INSERT INTO event_videos (org_id, event_id, provider, video_id, url, position)
SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::text, s.pos::smallint
FROM   generate_series(0, $6::int) AS s(pos)
WHERE  NOT EXISTS (SELECT 1 FROM event_videos v WHERE v.event_id = $2::uuid AND v.position = s.pos)
ORDER  BY s.pos
LIMIT  1
RETURNING ` + eventVideoColumns

// InsertEventVideo adds a video link at the lowest free position
// 0..maxPosition. It returns pgx.ErrNoRows when every position is taken.
func (q *Queries) InsertEventVideo(ctx context.Context, orgID, eventID uuid.UUID, provider string, videoID *string, url string, maxPosition int32) (EventVideoRow, error) {
	return scanEventVideoRow(q.db.QueryRow(ctx, insertEventVideo, orgID, eventID, provider, videoID, url, maxPosition))
}

const deleteEventVideo = `-- name: DeleteEventVideo :one
DELETE FROM event_videos
WHERE  id = $1
  AND  event_id = $2
RETURNING ` + eventVideoColumns

// DeleteEventVideo removes a video link and returns the deleted row.
func (q *Queries) DeleteEventVideo(ctx context.Context, id, eventID uuid.UUID) (EventVideoRow, error) {
	return scanEventVideoRow(q.db.QueryRow(ctx, deleteEventVideo, id, eventID))
}

const compactEventVideoPositions = `-- name: CompactEventVideoPositions :exec
UPDATE event_videos g
SET    position = (r.rn - 1)::smallint
FROM  (SELECT id, row_number() OVER (ORDER BY position) AS rn
       FROM   event_videos
       WHERE  event_id = $1) r
WHERE  g.id = r.id
  AND  g.position <> (r.rn - 1)::smallint`

// CompactEventVideoPositions closes the hole a delete left, in ONE statement.
func (q *Queries) CompactEventVideoPositions(ctx context.Context, eventID uuid.UUID) error {
	_, err := q.db.Exec(ctx, compactEventVideoPositions, eventID)
	return err
}

const reorderEventVideos = `-- name: ReorderEventVideos :execrows
UPDATE event_videos g
SET    position   = (v.ord - 1)::smallint,
       updated_at = now()
FROM   unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE  g.id = v.id
  AND  g.event_id = $1`

// ReorderEventVideos gives the videos the positions of their place in ids, in
// ONE statement, and returns how many rows were updated.
func (q *Queries) ReorderEventVideos(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, reorderEventVideos, eventID, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

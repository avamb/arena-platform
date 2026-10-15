// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: event_materials_copy.sql
//
// Copy of an event's page materials to another event of the same organization
// (task 1-2c).

package gen

import (
	"context"

	"github.com/google/uuid"
)

// EventMaterialsCountsRow counts the rows of the four copyable lists of an event.
type EventMaterialsCountsRow struct {
	Gallery      int32
	Videos       int32
	PartnerLinks int32
	MapPoints    int32
}

const countEventMaterials = `-- name: CountEventMaterials :one
SELECT
    (SELECT count(*) FROM event_gallery_items WHERE event_id = $1)::int  AS gallery,
    (SELECT count(*) FROM event_videos        WHERE event_id = $1)::int  AS videos,
    (SELECT count(*) FROM event_partner_links WHERE event_id = $1)::int  AS partner_links,
    (SELECT count(*) FROM event_map_points    WHERE event_id = $1)::int  AS map_points`

// CountEventMaterials counts an event's gallery photos, videos, partner links
// and map point (the photo report is not counted: it is never copied).
func (q *Queries) CountEventMaterials(ctx context.Context, eventID uuid.UUID) (EventMaterialsCountsRow, error) {
	var r EventMaterialsCountsRow
	err := q.db.QueryRow(ctx, countEventMaterials, eventID).Scan(&r.Gallery, &r.Videos, &r.PartnerLinks, &r.MapPoints)
	return r, err
}

const copyEventGalleryItems = `-- name: CopyEventGalleryItems :execrows
INSERT INTO event_gallery_items (org_id, event_id, media_id, position, caption)
SELECT $1::uuid, $2::uuid, s.media_id, (s.rn - 1)::smallint, s.caption
FROM (
    SELECT g.media_id, g.caption, row_number() OVER (ORDER BY g.position) AS rn
    FROM event_gallery_items g
    JOIN media_objects m ON m.id = g.media_id AND m.deleted_at IS NULL
    WHERE g.event_id = $3::uuid AND g.org_id = $1::uuid
) s`

// CopyEventGalleryItems copies the live photos of sourceEventID to eventID with
// the same media ids, renumbering positions from 0. It returns the rows added.
func (q *Queries) CopyEventGalleryItems(ctx context.Context, orgID, eventID, sourceEventID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, copyEventGalleryItems, orgID, eventID, sourceEventID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const copyEventVideos = `-- name: CopyEventVideos :execrows
INSERT INTO event_videos (org_id, event_id, provider, video_id, url, position)
SELECT $1::uuid, $2::uuid, s.provider, s.video_id, s.url, (s.rn - 1)::smallint
FROM (
    SELECT v.provider, v.video_id, v.url, row_number() OVER (ORDER BY v.position) AS rn
    FROM event_videos v
    WHERE v.event_id = $3::uuid AND v.org_id = $1::uuid
) s`

// CopyEventVideos copies the video links of sourceEventID to eventID.
func (q *Queries) CopyEventVideos(ctx context.Context, orgID, eventID, sourceEventID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, copyEventVideos, orgID, eventID, sourceEventID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const copyEventPartnerLinks = `-- name: CopyEventPartnerLinks :execrows
INSERT INTO event_partner_links (org_id, event_id, url, seller_name, position)
SELECT $1::uuid, $2::uuid, s.url, s.seller_name, (s.rn - 1)::smallint
FROM (
    SELECT p.url, p.seller_name, row_number() OVER (ORDER BY p.position) AS rn
    FROM event_partner_links p
    WHERE p.event_id = $3::uuid AND p.org_id = $1::uuid
) s`

// CopyEventPartnerLinks copies the partner links of sourceEventID to eventID.
func (q *Queries) CopyEventPartnerLinks(ctx context.Context, orgID, eventID, sourceEventID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, copyEventPartnerLinks, orgID, eventID, sourceEventID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const copyEventMapPoint = `-- name: CopyEventMapPoint :execrows
INSERT INTO event_map_points (event_id, org_id, latitude, longitude, label)
SELECT $2::uuid, $1::uuid, mp.latitude, mp.longitude, mp.label
FROM event_map_points mp
WHERE mp.event_id = $3::uuid AND mp.org_id = $1::uuid
ON CONFLICT (event_id) DO NOTHING`

// CopyEventMapPoint copies the map point of sourceEventID to eventID (0 or 1 row).
func (q *Queries) CopyEventMapPoint(ctx context.Context, orgID, eventID, sourceEventID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, copyEventMapPoint, orgID, eventID, sourceEventID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

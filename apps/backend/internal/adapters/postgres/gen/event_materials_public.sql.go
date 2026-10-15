// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: event_materials_public.sql
//
// The buyers' view of an event's page materials (task 1-2c).

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EventPublicMaterialsRow holds the five JSON columns of GetEventPublicMaterials.
// Every list is a JSON array ('[]' when empty, never NULL); MapPoint is a JSON
// object, or nil when the event has no map point.
type EventPublicMaterialsRow struct {
	Gallery      []byte
	Videos       []byte
	PartnerLinks []byte
	MapPoint     []byte
	ReportPhotos []byte
}

const getEventPublicMaterials = `-- name: GetEventPublicMaterials :one
SELECT
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
                   'media_id', g.media_id, 'caption', g.caption,
                   'width', m.width, 'height', m.height) ORDER BY g.position)
        FROM event_gallery_items g
        JOIN media_objects m ON m.id = g.media_id AND m.deleted_at IS NULL
        WHERE g.event_id = e.id
    ), '[]'::jsonb) AS gallery,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
                   'provider', v.provider, 'video_id', v.video_id, 'url', v.url) ORDER BY v.position)
        FROM event_videos v
        WHERE v.event_id = e.id
    ), '[]'::jsonb) AS videos,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
                   'url', p.url, 'seller_name', p.seller_name) ORDER BY p.position)
        FROM event_partner_links p
        WHERE p.event_id = e.id
    ), '[]'::jsonb) AS partner_links,
    (
        SELECT jsonb_build_object(
                   'latitude', mp.latitude::float8, 'longitude', mp.longitude::float8, 'label', mp.label)
        FROM event_map_points mp
        WHERE mp.event_id = e.id
    ) AS map_point,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
                   'media_id', r.media_id, 'caption', r.caption,
                   'width', m.width, 'height', m.height) ORDER BY r.position)
        FROM event_report_photos r
        JOIN media_objects m ON m.id = r.media_id AND m.deleted_at IS NULL
        WHERE r.event_id = e.id
          AND e.last_session_at IS NOT NULL
          AND e.last_session_at <= $2
    ), '[]'::jsonb) AS report_photos
FROM events e
WHERE e.id = $1
  AND e.deleted_at IS NULL
  AND e.status = 'published'`

// GetEventPublicMaterials reads the materials of a live published event in one
// round trip. pgx.ErrNoRows means the event is not live and published. now is
// the clock of the photo-report rule (hidden until the event's last session
// has ended).
func (q *Queries) GetEventPublicMaterials(ctx context.Context, eventID uuid.UUID, now time.Time) (EventPublicMaterialsRow, error) {
	var r EventPublicMaterialsRow
	err := q.db.QueryRow(ctx, getEventPublicMaterials, eventID, now).
		Scan(&r.Gallery, &r.Videos, &r.PartnerLinks, &r.MapPoint, &r.ReportPhotos)
	return r, err
}

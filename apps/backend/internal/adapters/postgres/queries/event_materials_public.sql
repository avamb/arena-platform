-- event_materials_public.sql — task 1-2c: the buyers' view of an event's page
-- materials (tables of migration 0153), in ONE round trip for both public
-- surfaces (the hosted page and the widget feed). Hand-maintained wrapper in
-- gen/event_materials_public.sql.go.

-- name: GetEventPublicMaterials :one
-- One row of five JSON columns, or ErrNoRows when the event is not a live
-- published event (deleted, draft, cancelled, archived, unknown): the caller
-- then shows no materials. Photos whose media object was deleted are skipped.
-- The photo report follows the rule of ListEventReportPhotosPublic: it is
-- hidden until the event is over ($2 is "now", passed in so the rule is
-- testable). events.last_session_at is the END of the last live session, so
-- the report opens when the event has ended, follows a session that moves,
-- and stays hidden for an event without a live session.
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
  AND e.status = 'published';

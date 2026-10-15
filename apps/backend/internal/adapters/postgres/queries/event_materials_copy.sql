-- event_materials_copy.sql — task 1-2c: copy the page materials of one event to
-- another event of the SAME organization (gallery, videos, partner links and
-- the map point; the photo report is about a past show and is never copied).
-- Hand-maintained wrappers in gen/event_materials_copy.sql.go. The handler
-- runs all of it in one transaction under the target's four materials locks
-- (LockEventMaterials / LockEventExtras), so a copy never interleaves with an
-- edit of the target's lists.
--
-- Gallery rows keep the SAME media_id: media_objects.owner_id is never set to
-- an event by the API and nothing deletes a media object when a gallery row
-- goes, so two events may share one picture; removing it from one event does
-- not touch the other. Positions are renumbered 0..n-1 (a source photo whose
-- media object was deleted is skipped).

-- name: CountEventMaterials :one
SELECT
    (SELECT count(*) FROM event_gallery_items WHERE event_id = $1)::int  AS gallery,
    (SELECT count(*) FROM event_videos        WHERE event_id = $1)::int  AS videos,
    (SELECT count(*) FROM event_partner_links WHERE event_id = $1)::int  AS partner_links,
    (SELECT count(*) FROM event_map_points    WHERE event_id = $1)::int  AS map_points;

-- name: CopyEventGalleryItems :execrows
INSERT INTO event_gallery_items (org_id, event_id, media_id, position, caption)
SELECT $1::uuid, $2::uuid, s.media_id, (s.rn - 1)::smallint, s.caption
FROM (
    SELECT g.media_id, g.caption, row_number() OVER (ORDER BY g.position) AS rn
    FROM event_gallery_items g
    JOIN media_objects m ON m.id = g.media_id AND m.deleted_at IS NULL
    WHERE g.event_id = $3::uuid AND g.org_id = $1::uuid
) s;

-- name: CopyEventVideos :execrows
INSERT INTO event_videos (org_id, event_id, provider, video_id, url, position)
SELECT $1::uuid, $2::uuid, s.provider, s.video_id, s.url, (s.rn - 1)::smallint
FROM (
    SELECT v.provider, v.video_id, v.url, row_number() OVER (ORDER BY v.position) AS rn
    FROM event_videos v
    WHERE v.event_id = $3::uuid AND v.org_id = $1::uuid
) s;

-- name: CopyEventPartnerLinks :execrows
INSERT INTO event_partner_links (org_id, event_id, url, seller_name, position)
SELECT $1::uuid, $2::uuid, s.url, s.seller_name, (s.rn - 1)::smallint
FROM (
    SELECT p.url, p.seller_name, row_number() OVER (ORDER BY p.position) AS rn
    FROM event_partner_links p
    WHERE p.event_id = $3::uuid AND p.org_id = $1::uuid
) s;

-- name: CopyEventMapPoint :execrows
INSERT INTO event_map_points (event_id, org_id, latitude, longitude, label)
SELECT $2::uuid, $1::uuid, mp.latitude, mp.longitude, mp.label
FROM event_map_points mp
WHERE mp.event_id = $3::uuid AND mp.org_id = $1::uuid
ON CONFLICT (event_id) DO NOTHING;

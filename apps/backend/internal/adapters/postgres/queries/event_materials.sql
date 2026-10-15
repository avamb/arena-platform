-- event_materials.sql — page materials of an event (migration 0153).
-- Wrappers are hand-maintained in gen/event_materials_*.sql.go.

-- ─── 1-2a: gallery photos and video links ────────────────────────────────────
-- Both tables cap their rows with position CHECK + UNIQUE (event_id, position)
-- DEFERRABLE, which cannot be an ON CONFLICT arbiter: an insert picks the
-- lowest free position itself and an empty result means "full". A handler runs
-- every write for one event under LockEventMaterials so that choice cannot
-- race. A reorder is ONE statement (the unique constraint is checked at the
-- end of a statement, so positions may swap freely inside it).

-- name: LockEventMaterials :exec
SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0));

-- name: GetMediaObjectOwnership :one
SELECT org_id, owner_type, deleted_at
FROM   media_objects
WHERE  id = $1;

-- name: ListEventGalleryItems :many
SELECT id, org_id, event_id, media_id, position, caption, created_at, updated_at
FROM   event_gallery_items
WHERE  event_id = $1
ORDER  BY position;

-- name: GetEventGalleryItem :one
SELECT id, org_id, event_id, media_id, position, caption, created_at, updated_at
FROM   event_gallery_items
WHERE  id = $1
  AND  event_id = $2;

-- name: InsertEventGalleryItem :one
-- $5 is the highest allowed position (eventmaterials.MaxGalleryPhotos - 1).
INSERT INTO event_gallery_items (org_id, event_id, media_id, position, caption)
SELECT $1::uuid, $2::uuid, $3::uuid, s.pos::smallint, $4::text
FROM   generate_series(0, $5::int) AS s(pos)
WHERE  NOT EXISTS (SELECT 1 FROM event_gallery_items g WHERE g.event_id = $2::uuid AND g.position = s.pos)
ORDER  BY s.pos
LIMIT  1
RETURNING id, org_id, event_id, media_id, position, caption, created_at, updated_at;

-- name: UpdateEventGalleryItemCaption :one
UPDATE event_gallery_items
SET    caption    = $3,
       updated_at = now()
WHERE  id = $1
  AND  event_id = $2
RETURNING id, org_id, event_id, media_id, position, caption, created_at, updated_at;

-- name: DeleteEventGalleryItem :one
DELETE FROM event_gallery_items
WHERE  id = $1
  AND  event_id = $2
RETURNING id, org_id, event_id, media_id, position, caption, created_at, updated_at;

-- name: CompactEventGalleryPositions :exec
UPDATE event_gallery_items g
SET    position = (r.rn - 1)::smallint
FROM  (SELECT id, row_number() OVER (ORDER BY position) AS rn
       FROM   event_gallery_items
       WHERE  event_id = $1) r
WHERE  g.id = r.id
  AND  g.position <> (r.rn - 1)::smallint;

-- name: ReorderEventGalleryItems :execrows
UPDATE event_gallery_items g
SET    position   = (v.ord - 1)::smallint,
       updated_at = now()
FROM   unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE  g.id = v.id
  AND  g.event_id = $1;

-- name: ListEventVideos :many
SELECT id, org_id, event_id, provider, video_id, url, position, created_at, updated_at
FROM   event_videos
WHERE  event_id = $1
ORDER  BY position;

-- name: InsertEventVideo :one
-- $6 is the highest allowed position (eventmaterials.MaxVideos - 1).
INSERT INTO event_videos (org_id, event_id, provider, video_id, url, position)
SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::text, s.pos::smallint
FROM   generate_series(0, $6::int) AS s(pos)
WHERE  NOT EXISTS (SELECT 1 FROM event_videos v WHERE v.event_id = $2::uuid AND v.position = s.pos)
ORDER  BY s.pos
LIMIT  1
RETURNING id, org_id, event_id, provider, video_id, url, position, created_at, updated_at;

-- name: DeleteEventVideo :one
DELETE FROM event_videos
WHERE  id = $1
  AND  event_id = $2
RETURNING id, org_id, event_id, provider, video_id, url, position, created_at, updated_at;

-- name: CompactEventVideoPositions :exec
UPDATE event_videos g
SET    position = (r.rn - 1)::smallint
FROM  (SELECT id, row_number() OVER (ORDER BY position) AS rn
       FROM   event_videos
       WHERE  event_id = $1) r
WHERE  g.id = r.id
  AND  g.position <> (r.rn - 1)::smallint;

-- name: ReorderEventVideos :execrows
UPDATE event_videos g
SET    position   = (v.ord - 1)::smallint,
       updated_at = now()
FROM   unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE  g.id = v.id
  AND  g.event_id = $1;
-- ─── end 1-2a ────────────────────────────────────────────────────────────────

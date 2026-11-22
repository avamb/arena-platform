-- event_series.sql — tours of an organization (migration 0218, spec 63 Л-2).
-- Wrappers are hand-maintained in gen/event_series.sql.go.

-- name: CreateEventSeries :one
INSERT INTO event_series (org_id, slug, name, description_html, poster_media_id,
                          hero_media_id, hero_video_url, partners_text_html,
                          hide_children_in_lists, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, org_id, slug, name, description_html, poster_media_id, hero_media_id,
          hero_video_url, partners_text_html, hide_children_in_lists, position,
          archived_at, created_at, updated_at;

-- name: GetEventSeries :one
SELECT id, org_id, slug, name, description_html, poster_media_id, hero_media_id,
       hero_video_url, partners_text_html, hide_children_in_lists, position,
       archived_at, created_at, updated_at
FROM   event_series
WHERE  id = $1
  AND  org_id = $2;

-- name: ListEventSeries :many
SELECT id, org_id, slug, name, description_html, poster_media_id, hero_media_id,
       hero_video_url, partners_text_html, hide_children_in_lists, position,
       archived_at, created_at, updated_at
FROM   event_series
WHERE  org_id = $1
  AND  ($2::boolean OR archived_at IS NULL)
ORDER  BY position, lower(name), id;

-- name: UpdateEventSeries :one
UPDATE event_series
SET    slug                   = $3,
       name                   = $4,
       description_html       = $5,
       poster_media_id        = $6,
       hero_media_id          = $7,
       hero_video_url         = $8,
       partners_text_html     = $9,
       hide_children_in_lists = $10,
       position               = $11,
       updated_at             = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING id, org_id, slug, name, description_html, poster_media_id, hero_media_id,
          hero_video_url, partners_text_html, hide_children_in_lists, position,
          archived_at, created_at, updated_at;

-- name: ArchiveEventSeries :one
UPDATE event_series
SET    archived_at = COALESCE(archived_at, now()),
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING id, org_id, slug, name, description_html, poster_media_id, hero_media_id,
          hero_video_url, partners_text_html, hide_children_in_lists, position,
          archived_at, created_at, updated_at;

-- name: SeriesSlugTaken :one
-- A tour slug shares its namespace with the active event slugs of the
-- organization; $3 (nullable) is the tour being edited, which may keep its own.
SELECT EXISTS (
    SELECT 1 FROM events
    WHERE  org_id = $1
      AND  lower(slug) = lower($2)
      AND  deleted_at IS NULL
) OR EXISTS (
    SELECT 1 FROM event_series
    WHERE  org_id = $1
      AND  lower(slug) = lower($2)
      AND  ($3::uuid IS NULL OR id <> $3::uuid)
);

-- name: SetEventSeries :execrows
-- $3 NULL unlinks the event. A tour of another organization fails the
-- composite foreign key events_series_fk (23503).
UPDATE events
SET    series_id  = $3,
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: ListSeriesIDsForEvents :many
SELECT id, series_id
FROM   events
WHERE  id = ANY($1::uuid[])
  AND  series_id IS NOT NULL;

-- name: ListEventIDsForSeries :many
SELECT id
FROM   events
WHERE  series_id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
ORDER  BY created_at, id;

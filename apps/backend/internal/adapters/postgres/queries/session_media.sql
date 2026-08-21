-- session_media.sql — sqlc query definitions for the session_media_items
-- table (AB-47b, migration 0085). This table stores an ordered per-session
-- gallery of up to N poster media_objects + video URL links. The single
-- cover (sessions.poster_media_id / events.poster_media_id) added in AB-47
-- is untouched; this table is additive.
--
-- Poster cap (5) lives in the handler, NOT in a DB CHECK, so raising it
-- later doesn't need a migration. Gallery poster rows reuse the existing
-- media_objects.owner_type = 'session_poster' allowlist entry (no widening).

-- name: ListSessionMediaItems :many
-- ListSessionMediaItems returns every row for the session ordered by
-- position ASC. Rows survive session soft-delete via ON DELETE CASCADE but
-- the handler gates fetches on the session existing.
SELECT id, session_id, kind, media_id, video_url, position, created_at, updated_at
FROM   session_media_items
WHERE  session_id = $1
ORDER BY position ASC, id ASC;

-- name: DeleteSessionMediaItems :exec
-- DeleteSessionMediaItems wipes every row for the session. Called at the
-- top of ReplaceSessionMediaItems inside a transaction.
DELETE FROM session_media_items
WHERE  session_id = $1;

-- name: InsertSessionMediaItem :one
-- InsertSessionMediaItem inserts a single gallery row. The kind_payload
-- CHECK constraint on the table refuses the write when kind/media_id/
-- video_url are inconsistent, so the handler need not double-validate at
-- the DB layer.
INSERT INTO session_media_items (session_id, kind, media_id, video_url, position)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, session_id, kind, media_id, video_url, position, created_at, updated_at;

-- name: SessionExistsActive :one
-- SessionExistsActive returns TRUE when the session is present and not
-- soft-deleted. Used by the gallery handler before running the replace
-- transaction so a request for an unknown session gets 404 instead of a
-- silent no-op write.
SELECT EXISTS (
    SELECT 1 FROM sessions
    WHERE  id = $1
      AND  deleted_at IS NULL
) AS exists;

-- name: GetMediaObjectOwnerType :one
-- GetMediaObjectOwnerType returns the owner_type of an ACTIVE media object.
-- Used by the gallery PUT handler to enforce
-- session_media_items.kind='poster' rows point at a media_objects row of
-- owner_type='session_poster' (422 media_owner_type_mismatch otherwise).
-- Returns pgx.ErrNoRows when the media object does not exist or is
-- soft-deleted.
SELECT owner_type
FROM   media_objects
WHERE  id = $1
  AND  deleted_at IS NULL;

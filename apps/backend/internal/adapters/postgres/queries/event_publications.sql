-- event_publications.sql — sqlc query definitions for event_publications (feature #151).
-- Publish / unpublish events to/from agent feed channels.

-- name: PublishEvent :one
-- Insert a publication row. ON CONFLICT DO NOTHING makes the operation
-- idempotent: re-publishing the same event to the same feed is a no-op that
-- still returns the existing row (the second SELECT handles that case).
INSERT INTO event_publications (event_id, feed_token_id, city_id)
VALUES ($1, $2, $3)
ON CONFLICT (event_id, feed_token_id) DO UPDATE
    SET published_at = event_publications.published_at  -- no-op update to trigger RETURNING
RETURNING id, event_id, feed_token_id, city_id, published_at;

-- name: UnpublishEvent :exec
-- Remove a single publication entry (hard delete — no soft-delete for publications).
-- Returns ErrNoRows when the entry does not exist (idempotent caller can ignore).
DELETE FROM event_publications
WHERE  event_id      = $1
  AND  feed_token_id = $2;

-- name: ListPublicationsByEvent :many
-- List all feed tokens an event is currently published to.
SELECT id, event_id, feed_token_id, city_id, published_at
FROM   event_publications
WHERE  event_id = $1
ORDER  BY published_at ASC, id ASC;

-- name: ListPublicationsByFeedToken :many
-- List all events published to a given feed token (the consumer-side view).
SELECT id, event_id, feed_token_id, city_id, published_at
FROM   event_publications
WHERE  feed_token_id = $1
ORDER  BY published_at ASC, id ASC;

-- name: GetPublication :one
-- Get a single publication by (event_id, feed_token_id).
SELECT id, event_id, feed_token_id, city_id, published_at
FROM   event_publications
WHERE  event_id      = $1
  AND  feed_token_id = $2;

-- public_feed.sql — sqlc query definitions for the public feed API (feature #152).
-- These queries power the unauthenticated public feed endpoints:
--   GET /v1/public/feeds/{token}/events
--   GET /v1/public/feeds/{token}/events/{event_id}
-- All queries join events → event_publications → agent_feed_tokens to enforce
-- that only events actively published to an active (non-revoked) token are visible.

-- name: ListPublishedEventsByFeedToken :many
-- ListPublishedEventsByFeedToken returns published events for a feed token.
-- Optional filters: city_id (via publication scope), date_from, date_to.
-- Pagination via LIMIT/OFFSET. Only active tokens and published, non-deleted events.
SELECT
    e.id, e.org_id, e.venue_id, e.name, e.description, e.status,
    e.start_at, e.end_at, e.visibility, e.image_url,
    e.created_at, e.updated_at, e.deleted_at
FROM events e
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
WHERE ft.token    = $1
  AND ft.is_active = true
  AND e.status     = 'published'
  AND e.deleted_at IS NULL
  AND ($2::uuid IS NULL OR ep.city_id = $2::uuid)
  AND ($3::timestamptz IS NULL OR e.start_at >= $3::timestamptz)
  AND ($4::timestamptz IS NULL OR e.end_at   <= $4::timestamptz)
ORDER BY e.start_at ASC, e.id ASC
LIMIT  $5
OFFSET $6;

-- name: CountPublishedEventsByFeedToken :one
-- CountPublishedEventsByFeedToken returns the total count of published events for
-- a feed token, with the same optional filters as ListPublishedEventsByFeedToken.
-- Used for pagination metadata (total, total_pages).
SELECT COUNT(*)::int
FROM events e
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
WHERE ft.token    = $1
  AND ft.is_active = true
  AND e.status     = 'published'
  AND e.deleted_at IS NULL
  AND ($2::uuid IS NULL OR ep.city_id = $2::uuid)
  AND ($3::timestamptz IS NULL OR e.start_at >= $3::timestamptz)
  AND ($4::timestamptz IS NULL OR e.end_at   <= $4::timestamptz);

-- name: GetPublishedEventByFeedToken :one
-- GetPublishedEventByFeedToken fetches a single published event that is
-- actively published to the given feed token. Returns pgx.ErrNoRows when:
--   * the token is unknown or revoked
--   * the event does not exist or is not published
--   * the event has not been published to this token
SELECT
    e.id, e.org_id, e.venue_id, e.name, e.description, e.status,
    e.start_at, e.end_at, e.visibility, e.image_url,
    e.created_at, e.updated_at, e.deleted_at
FROM events e
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
WHERE ft.token    = $1
  AND ft.is_active = true
  AND e.id         = $2
  AND e.status     = 'published'
  AND e.deleted_at IS NULL;

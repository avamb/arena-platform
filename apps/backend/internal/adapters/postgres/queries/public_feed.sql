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

-- name: GetPublicCheckoutContext :one
-- GetPublicCheckoutContext validates that a session belongs to an event published
-- on the given feed token and returns the checkout context needed to create a
-- reservation + checkout session (org_id, sales_channel_id).
-- Returns pgx.ErrNoRows when:
--   * the token is unknown or revoked (is_active = false)
--   * the session is not found or is soft-deleted
--   * the session's event is not published to this feed token
-- This implements the ADR-013 policy: the feed token is the credential for
-- public (unauthenticated) checkout flows.
SELECT
    s.id                  AS session_id,
    s.event_id            AS event_id,
    e.org_id              AS org_id,
    ft.sales_channel_id   AS sales_channel_id
FROM sessions s
JOIN events e ON e.id = s.event_id
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
WHERE ft.token     = $1
  AND ft.is_active  = true
  AND s.id          = $2
  AND e.status      = 'published'
  AND e.deleted_at  IS NULL
  AND s.deleted_at  IS NULL;

-- name: GetFeedTokenBuyerFlags :one
-- GetFeedTokenBuyerFlags returns the buyer-field collection flags for the
-- sales channel linked to the given active feed token.  Widgets call this
-- (indirectly, via the public feed session payload) to build the buyer form.
-- Returns pgx.ErrNoRows when the token is unknown, revoked, or its linked
-- sales channel has been soft-deleted.
-- Feature #321 WID-0d.
SELECT
    sc.collect_name,
    sc.collect_phone
FROM agent_feed_tokens ft
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE ft.token     = $1
  AND ft.is_active  = true
  AND sc.deleted_at IS NULL;

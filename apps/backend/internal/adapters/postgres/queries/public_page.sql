-- public_page.sql — sqlc query definitions for the hosted sales page resolver
-- (tickets.arenasoldout.com/{org_slug}/{event_slug}).
--
-- One round trip resolves the whole chain: active org by slug -> non-deleted
-- published event by (org, slug) -> an active publication of that event on a
-- channel of the SAME org whose settings.hosted_page.enabled = true and which
-- is itself active -> the newest active, non-revoked feed token of that
-- channel. Any link missing yields zero rows, which the handler turns into
-- one indistinguishable 404 (it never reveals which step failed).

-- name: GetHostedPageResolution :one
-- GetHostedPageResolution resolves the hosted-page context for an
-- (org_slug, event_slug) pair. Returns pgx.ErrNoRows when:
--   * the org slug is unknown or the org is soft-deleted
--   * the event slug is unknown for that org, the event is soft-deleted, or
--     its status is not 'published'
--   * no channel of the same org has settings.hosted_page.enabled = true
--   * that channel has been soft-deleted
--   * the channel has no active (non-revoked) feed token
-- When several qualifying (channel, token) pairs exist, the newest active
-- token wins (ORDER BY ft.created_at DESC LIMIT 1).
SELECT
    o.id, o.slug, o.name, o.logo_media_id, o.default_locale,
    e.id, e.slug, e.name, e.description, e.short_description,
    e.image_url, e.poster_media_id, e.age_rating,
    e.first_session_at, e.last_session_at,
    ft.token
FROM organizations o
JOIN events e ON e.org_id = o.id
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE o.slug       = $1
  AND o.deleted_at IS NULL
  AND e.slug       = $2
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND sc.org_id    = o.id
  AND sc.deleted_at IS NULL
  AND sc.settings #>> '{hosted_page,enabled}' = 'true'
  AND ft.is_active = true
ORDER BY ft.created_at DESC
LIMIT 1;

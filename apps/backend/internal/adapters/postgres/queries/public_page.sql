-- public_page.sql — sqlc query definitions for the hosted sales page resolver
-- (tickets.arenasoldout.com/{org_slug}/{event_slug}) and the promoter landing
-- page (tickets.arenasoldout.com/{org_slug}).
--
-- One round trip resolves the whole chain: active org by slug -> non-deleted
-- published event by (org, slug) -> an active publication of that event on a
-- channel of the SAME org whose settings.hosted_page.enabled = true and which
-- is itself active -> the newest active, non-revoked feed token of that
-- channel. Any link missing yields zero rows, which the handler turns into
-- one indistinguishable 404 (it never reveals which step failed).
--
-- Slug comparisons are case-insensitive (lower() on both sides): org slugs
-- are normalized lowercase at write time (hiam/orgs.go, hiam/admin_orgs.go)
-- but event slugs are NOT (hcatalog/events.go's metadata PATCH stores
-- req.Slug verbatim) — an organizer typing "MasterClassTeatro" in a browser
-- must still resolve, so both columns are lower()'d defensively rather than
-- relying on write-time normalization alone.

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
-- token wins (ORDER BY ft.created_at DESC LIMIT 1). first_session_timezone
-- is the IANA zone name (venues.timezone) of the event's earliest active,
-- non-cancelled session's venue — a cheap correlated subquery, NULL when the
-- event has no sessions yet or the venue has no timezone set.
SELECT
    o.id, o.slug, o.name, o.logo_media_id, o.default_locale,
    e.id, e.slug, e.name, e.description, e.short_description,
    e.image_url, e.poster_media_id, e.age_rating,
    e.first_session_at, e.last_session_at,
    (
        SELECT v.timezone
        FROM   sessions s
        JOIN   venues v ON v.id = s.venue_id
        WHERE  s.event_id   = e.id
          AND  s.deleted_at IS NULL
          AND  s.status    <> 'cancelled'
        ORDER BY s.start_at ASC
        LIMIT 1
    ) AS first_session_timezone,
    ft.token
FROM organizations o
JOIN events e ON e.org_id = o.id
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE lower(o.slug) = lower($1)
  AND o.deleted_at IS NULL
  AND lower(e.slug) = lower($2)
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND sc.org_id    = o.id
  AND sc.deleted_at IS NULL
  AND sc.settings #>> '{hosted_page,enabled}' = 'true'
  AND ft.is_active = true
ORDER BY ft.created_at DESC
LIMIT 1;

-- name: GetHostedPromoterPageOrg :one
-- GetHostedPromoterPageOrg resolves the org-branding slice of the promoter
-- landing page (tickets.arenasoldout.com/{org_slug}) and reports, in the
-- same round trip, whether the org has at least one hosted-page-eligible
-- channel (has_hosted_channel: not soft-deleted, settings.hosted_page.enabled
-- = true, with an active feed token) — the same eligibility gate
-- GetHostedPageResolution applies per event, evaluated once here so an org
-- with zero currently-visible events but a properly configured channel still
-- resolves 200 with an empty list, while an org with no such channel at all
-- (or an unknown slug) resolves pgx.ErrNoRows / has_hosted_channel=false,
-- both of which the handler maps to 404 page.not_found.
SELECT
    o.id, o.slug, o.name, o.logo_media_id, o.default_locale,
    EXISTS (
        SELECT 1
        FROM   sales_channels sc
        JOIN   agent_feed_tokens ft ON ft.sales_channel_id = sc.id
        WHERE  sc.org_id      = o.id
          AND  sc.deleted_at  IS NULL
          AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
          AND  ft.is_active   = true
    ) AS has_hosted_channel
FROM organizations o
WHERE lower(o.slug) = lower($1)
  AND o.deleted_at IS NULL;

-- name: ListHostedPromoterPageEvents :many
-- ListHostedPromoterPageEvents lists every event of the org that would
-- individually resolve on GET /v1/public/pages/{org_slug}/{event_slug}:
-- non-deleted, status 'published', has a slug, published through an active
-- feed token of a same-org channel with settings.hosted_page.enabled = true.
-- DISTINCT ON (e.id) collapses an event published through several
-- qualifying tokens/channels to one row. An event whose last_session_at has
-- already passed is omitted entirely; an event with no sessions yet (NULL
-- first/last_session_at) is kept. The outer ORDER BY sorts upcoming-first
-- (first_session_at ascending, NULLs last) across the whole result, capped
-- at 200. first_session_timezone mirrors GetHostedPageResolution's subquery.
-- venue_names is intentionally NOT computed here — callers hydrate it via
-- the existing ListEventVenueNames batch query (same pattern
-- HandlePublicPage already uses), so the venue-name aggregation logic lives
-- in exactly one place.
SELECT id, slug, name, short_description, image_url, poster_media_id,
       age_rating, first_session_at, last_session_at, first_session_timezone
FROM (
    SELECT DISTINCT ON (e.id)
        e.id, e.slug, e.name, e.short_description, e.image_url,
        e.poster_media_id, e.age_rating, e.first_session_at, e.last_session_at,
        (
            SELECT v.timezone
            FROM   sessions s
            JOIN   venues v ON v.id = s.venue_id
            WHERE  s.event_id   = e.id
              AND  s.deleted_at IS NULL
              AND  s.status    <> 'cancelled'
            ORDER BY s.start_at ASC
            LIMIT 1
        ) AS first_session_timezone
    FROM events e
    JOIN event_publications ep ON ep.event_id = e.id
    JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
    JOIN sales_channels sc ON sc.id = ft.sales_channel_id
    WHERE e.org_id      = $1
      AND e.deleted_at  IS NULL
      AND e.status      = 'published'
      AND e.slug        IS NOT NULL
      AND sc.org_id     = e.org_id
      AND sc.deleted_at IS NULL
      AND sc.settings #>> '{hosted_page,enabled}' = 'true'
      AND ft.is_active  = true
      AND (e.last_session_at IS NULL OR e.last_session_at >= now())
    ORDER BY e.id
) matched
ORDER BY first_session_at ASC NULLS LAST, id ASC
LIMIT 200;

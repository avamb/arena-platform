-- event_page_stats.sql — the hosted page's statistics beacon (HP-2a, task 2-3).
-- Table event_page_stats comes from migration 0153.
--
-- The beacon writes ONE counter row per (event, day, kind, utm_source,
-- utm_campaign): no IP, no User-Agent, no country, no hour. The numbers are an
-- organizer's guide, not proof for an ad account.

-- name: ResolvePageTrackTarget :one
-- ResolvePageTrackTarget finds the event a page address names, with the SAME
-- eligibility as GetHostedPageResolution (an organization's slug and the
-- event's slug, case-insensitive; the event published and not deleted; an
-- active publication on a same-org channel with settings.hosted_page.enabled
-- = true and an active feed token), but without the page's heavy columns: the
-- beacon fires on every page load. timezone is the IANA zone of the venue of
-- the earliest active, non-cancelled session (NULL when there is none): the
-- day a hit belongs to follows it.
SELECT e.id, e.org_id,
       (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone
FROM organizations o
JOIN events e ON e.org_id = o.id
WHERE lower(o.slug) = lower($1)
  AND o.deleted_at IS NULL
  AND lower(e.slug) = lower($2)
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND EXISTS (
      SELECT 1
      FROM   event_publications ep
      JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
      JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
      WHERE  ep.event_id = e.id
        AND  sc.org_id   = o.id
        AND  sc.deleted_at IS NULL
        AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
        AND  ft.is_active = true
  )
LIMIT 1;

-- name: ResolvePageTrackTargetByPromoter :one
-- ResolvePageTrackTargetByPromoter is the same for a promoter's page
-- (GetHostedPageResolutionByPromoter): the event must be linked to the active
-- promoter whose slug is $1.
SELECT e.id, e.org_id,
       (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone
FROM org_promoters p
JOIN organizations o ON o.id = p.org_id
JOIN event_promoters epr ON epr.promoter_id = p.id
JOIN events e ON e.id = epr.event_id AND e.org_id = o.id
WHERE lower(p.slug) = lower($1)
  AND p.archived_at IS NULL
  AND o.deleted_at IS NULL
  AND lower(e.slug) = lower($2)
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND EXISTS (
      SELECT 1
      FROM   event_publications ep
      JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
      JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
      WHERE  ep.event_id = e.id
        AND  sc.org_id   = o.id
        AND  sc.deleted_at IS NULL
        AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
        AND  ft.is_active = true
  )
LIMIT 1;

-- name: RecordEventPageHit :exec
-- RecordEventPageHit adds one hit ($7 hits, $8 suspect hits: exactly one of
-- them is 1) to the counter row of (event, day, kind, utm pair). A pair that
-- is new for the event on that day is kept only while the day holds fewer
-- than $9 different pairs; past that the hit is counted under empty utm
-- values, so a caller inventing campaigns cannot grow the table. The cap is
-- read in the same statement and is approximate under concurrency (a burst may
-- overshoot by the number of racing requests) — a bound, not a quota.
WITH pair AS (
    SELECT (
               ($5 = '' AND $6 = '')
               OR EXISTS (
                   SELECT 1 FROM event_page_stats
                   WHERE event_id = $1 AND day = $3 AND utm_source = $5 AND utm_campaign = $6
               )
               OR (
                   SELECT count(DISTINCT (utm_source, utm_campaign))
                   FROM   event_page_stats
                   WHERE  event_id = $1 AND day = $3
                     AND (utm_source <> '' OR utm_campaign <> '')
               ) < $9
           ) AS keep
)
INSERT INTO event_page_stats (event_id, org_id, day, kind, utm_source, utm_campaign, hits, suspect_hits)
SELECT $1, $2, $3, $4,
       CASE WHEN keep THEN $5 ELSE '' END,
       CASE WHEN keep THEN $6 ELSE '' END,
       $7, $8
FROM pair
ON CONFLICT (event_id, day, kind, utm_source, utm_campaign) DO UPDATE
   SET hits         = event_page_stats.hits         + EXCLUDED.hits,
       suspect_hits = event_page_stats.suspect_hits + EXCLUDED.suspect_hits;

-- name: GetEventPageStatsHeader :one
-- GetEventPageStatsHeader returns the venue zone of the event's earliest active
-- session, scoped to the organization: pgx.ErrNoRows means the event is not in
-- that organization (or is deleted).
SELECT (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone,
       e.slug
FROM events e
WHERE e.id = $1 AND e.org_id = $2 AND e.deleted_at IS NULL;

-- name: ListEventPageStatsDays :many
-- ListEventPageStatsDays sums the counters over the utm values, one row per
-- (day, kind) that has data in [$2, $3].
SELECT day, kind, sum(hits)::bigint, sum(suspect_hits)::bigint
FROM   event_page_stats
WHERE  event_id = $1 AND day BETWEEN $2 AND $3
GROUP BY day, kind
ORDER BY day, kind;

-- name: ListEventPageStatsSources :many
-- ListEventPageStatsSources lists the utm pairs of the page_view counters in
-- [$2, $3], biggest first (pairs with the same number by name), at most $4.
-- Hits that carried no utm are the pair ('', '').
SELECT utm_source, utm_campaign, sum(hits)::bigint, sum(suspect_hits)::bigint
FROM   event_page_stats
WHERE  event_id = $1 AND kind = 'page_view' AND day BETWEEN $2 AND $3
GROUP BY utm_source, utm_campaign
ORDER BY sum(hits) DESC, utm_source, utm_campaign
LIMIT $4;

-- name: ListEventPageStatsRows :many
-- ListEventPageStatsRows is the raw table in [$2, $3] for the CSV, oldest day
-- first. A day holds at most 3 kinds x (200 utm pairs + 1) rows.
SELECT day, kind, utm_source, utm_campaign, hits, suspect_hits
FROM   event_page_stats
WHERE  event_id = $1 AND day BETWEEN $2 AND $3
ORDER BY day, kind, utm_source, utm_campaign
LIMIT $4;

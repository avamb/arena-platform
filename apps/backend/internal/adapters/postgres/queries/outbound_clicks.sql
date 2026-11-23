-- name: RecordOutboundClick :exec
-- RecordOutboundClick adds one click on a partner box office (the seller name,
-- at most 64 characters) to the event's counter of the day (spec 63, L-10,
-- migration 0219). No visitor data is kept.
INSERT INTO outbound_clicks_daily (org_id, event_id, day, seller, hits)
VALUES ($1, $2, $3, $4, 1)
ON CONFLICT (event_id, day, seller) DO UPDATE
   SET hits = outbound_clicks_daily.hits + 1;

-- name: ListOutboundClickRows :many
-- ListOutboundClickRows reads the clicks of an event over [from, to], one row
-- per day and box office.
SELECT day, seller, hits
FROM   outbound_clicks_daily
WHERE  event_id = $1 AND org_id = $2 AND day BETWEEN $3 AND $4
ORDER  BY day, seller;

-- name: ListOutboundClickSources :many
-- ListOutboundClickSources sums the clicks of an event over [from, to] by utm source.
SELECT utm_source, sum(hits)::bigint
FROM   sales_funnel_daily
WHERE  event_id = $1 AND org_id = $2 AND day BETWEEN $3 AND $4 AND step = 'outbound_click'
GROUP  BY utm_source
HAVING sum(hits) > 0
ORDER  BY sum(hits) DESC, utm_source
LIMIT  50;

-- name: CountExternalSiteVisits :one
-- CountExternalSiteVisits sums the shown-event hits of the static site over [from, to].
SELECT coalesce(sum(hits), 0)::bigint
FROM   sales_funnel_daily
WHERE  event_id = $1 AND org_id = $2 AND day BETWEEN $3 AND $4
  AND  step = 'view' AND surface = 'external_site';

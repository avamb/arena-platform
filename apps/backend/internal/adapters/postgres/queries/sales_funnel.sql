-- sales_funnel.sql — the buyer sales funnel of an event (spec 50, stage A7).
-- Table sales_funnel_daily comes from migration 0189.
--
-- The public beacon writes ONE counter row per (event, day, step, surface,
-- session, utm_source, utm_campaign): no IP, no User-Agent, no country, no
-- hour. The organizer's read adds the server-side steps (checkout_start,
-- payment_open, paid) straight from orders / checkout_sessions /
-- payment_intents. The numbers are an organizer's guide, not proof for an
-- ad account.

-- name: ResolveFunnelTargetByFeedToken :one
-- ResolveFunnelTargetByFeedToken finds the event a widget names: the event
-- must be published and not deleted, and actively published to the ACTIVE feed
-- token $1 (the same eligibility as GetPublishedEventByFeedToken). timezone is
-- the IANA zone of the venue of the earliest active, non-cancelled session
-- (NULL when there is none): the day a hit belongs to follows it.
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
FROM events e
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
WHERE ft.token     = $1
  AND ft.is_active = true
  AND e.id         = $2
  AND e.status     = 'published'
  AND e.deleted_at IS NULL
LIMIT 1;

-- name: FunnelSessionBelongsToEvent :one
-- FunnelSessionBelongsToEvent says whether session $2 is a live session of
-- event $1. A beacon that names any other session is not counted.
SELECT EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = $2 AND s.event_id = $1 AND s.deleted_at IS NULL
);

-- name: RecordSalesFunnelHit :exec
-- RecordSalesFunnelHit adds one hit ($8 hits, $9 suspect hits: exactly one of
-- them is 1) to the counter row of (event, day, step, surface, session, utm
-- pair). A pair that is new for the event on that day is kept only while the
-- day holds fewer than $10 different pairs; past that the hit is counted under
-- empty utm values, so a caller inventing campaigns cannot grow the table.
-- The cap is read in the same statement and is approximate under concurrency
-- (a burst may overshoot by the number of racing requests) — a bound, not a
-- quota. $3 is NULL for the steps taken before a session is chosen.
WITH pair AS (
    SELECT (
               ($6 = '' AND $7 = '')
               OR EXISTS (
                   SELECT 1 FROM sales_funnel_daily
                   WHERE event_id = $1 AND day = $4 AND utm_source = $6 AND utm_campaign = $7
               )
               OR (
                   SELECT count(DISTINCT (utm_source, utm_campaign))
                   FROM   sales_funnel_daily
                   WHERE  event_id = $1 AND day = $4
                     AND (utm_source <> '' OR utm_campaign <> '')
               ) < $10
           ) AS keep
)
INSERT INTO sales_funnel_daily (org_id, event_id, session_id, day, step, surface, utm_source, utm_campaign, hits, suspect_hits)
SELECT $2, $1, $3, $4, $5, $11,
       CASE WHEN keep THEN $6 ELSE '' END,
       CASE WHEN keep THEN $7 ELSE '' END,
       $8, $9
FROM pair
ON CONFLICT (event_id, day, step, surface, session_key, utm_source, utm_campaign) DO UPDATE
   SET hits         = sales_funnel_daily.hits         + EXCLUDED.hits,
       suspect_hits = sales_funnel_daily.suspect_hits + EXCLUDED.suspect_hits;

-- name: ListSalesFunnelClientRows :many
-- ListSalesFunnelClientRows sums the client steps of event $1 over [$2, $3],
-- one row per (step, utm pair). With a session ($4 not NULL) only the steps
-- taken inside that session are read: view is an event-level step that
-- carries no session, so it is left out.
SELECT step, utm_source, utm_campaign, sum(hits)::bigint, sum(suspect_hits)::bigint
FROM   sales_funnel_daily
WHERE  event_id = $1 AND day BETWEEN $2 AND $3
  AND  ($4::uuid IS NULL OR (step <> 'view' AND session_id = $4::uuid))
GROUP BY step, utm_source, utm_campaign;

-- name: ListEventFunnelSessions :many
-- ListEventFunnelSessions is ListEventSummarySessions without the money
-- columns: the live sessions of event $1 with the zone of THEIR venue, for the
-- funnel screen's session picker (a date is read in its own venue's zone).
SELECT s.id, s.start_at, s.status, v.timezone
FROM      sessions s
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.event_id = $1
  AND  s.deleted_at IS NULL
ORDER  BY s.start_at, s.id;

-- name: ListFunnelServerRows :many
-- ListFunnelServerRows counts the server-side steps of the event's orders on
-- channels that sell through the widget, the hosted page or the API (a channel
-- with a gateway credential is read by ListFunnelGatewayOrders). One row per
-- (utm pair of the checkout's attribution, sales link, currency). The steps
-- count BUYERS, not attempts: a buyer who retries checkout/start twice is one
-- buyer, otherwise a share of the previous step could pass 100 %. A buyer is
-- (customer, or the e-mail on the order, or the order itself) x session x day
-- in the venue's zone $5.
--   started       a checkout took its hold
--   payment_open  a payment page was opened (a payment intent exists), or the
--                 order was paid without one (a free order)
--   paid          the order was ever paid (paid, partially_refunded, refunded)
--   revenue       the total of the paid orders MINUS their succeeded refunds,
--                 in minor units (a refund reaches its order by the order, the
--                 ticket it names or the payment intent, like the session
--                 summary)
-- Invitations (source 'complimentary') are not sales and are left out. The
-- created_at bounds on both sides are slack by a day each way so that
-- orders_org_created_idx can be used; the zone-aware date test is the exact one.
WITH x AS (
    SELECT COALESCE(cs.attribution ->> 'utm_source', '')   AS utm_source,
           COALESCE(cs.attribution ->> 'utm_campaign', '') AS utm_campaign,
           o.sales_link_id,
           o.currency::text AS currency,
           COALESCE(o.customer_id::text, lower(btrim(o.buyer_email)), o.id::text)
               || '|' || o.session_id::text
               || '|' || ((o.created_at AT TIME ZONE $5::text)::date)::text AS buyer,
           (o.status IN ('paid', 'partially_refunded', 'refunded')) AS paid,
           EXISTS (SELECT 1 FROM payment_intents pi
                   WHERE pi.checkout_session_id = o.checkout_session_id) AS has_intent,
           o.total - rf.amount AS net
    FROM   orders o
    JOIN   checkout_sessions cs ON cs.id = o.checkout_session_id
    LEFT JOIN LATERAL (
        SELECT COALESCE(sum(r.amount), 0)::bigint AS amount
        FROM  (
                  SELECT r1.id FROM refunds r1 WHERE r1.order_id = o.id
                  UNION
                  SELECT r2.id FROM refunds r2 JOIN tickets t ON t.id = r2.ticket_id WHERE t.order_id = o.id
                  UNION
                  SELECT r3.id FROM refunds r3 JOIN payment_intents p3 ON p3.id = r3.payment_intent_id
                  WHERE  p3.checkout_session_id = o.checkout_session_id
              ) cand
        JOIN   refunds r ON r.id = cand.id
        WHERE  r.state = 'succeeded'
    ) rf ON true
    WHERE  o.event_id = $1 AND o.org_id = $2
      AND  o.source <> 'complimentary'
      AND  o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
      AND  (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
      AND  ($6::uuid IS NULL OR o.session_id = $6::uuid)
      AND  NOT EXISTS (
               SELECT 1 FROM sales_channels sc
               WHERE  sc.id = o.channel_id
                 AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
                       OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
           )
)
SELECT x.utm_source, x.utm_campaign, x.sales_link_id,
       sl.code  AS sales_link_code,
       sl.label AS sales_link_label,
       x.currency,
       count(DISTINCT x.buyer)::bigint                                  AS started,
       (count(DISTINCT x.buyer) FILTER (WHERE x.paid OR x.has_intent))::bigint AS payment_open,
       (count(DISTINCT x.buyer) FILTER (WHERE x.paid))::bigint          AS paid,
       COALESCE(sum(x.net) FILTER (WHERE x.paid), 0)::bigint            AS revenue
FROM   x
LEFT JOIN sales_links sl ON sl.id = x.sales_link_id
GROUP BY 1, 2, 3, 4, 5, 6;

-- name: GetFunnelServerTotals :one
-- GetFunnelServerTotals is the same buyers as ListFunnelServerRows counted
-- ONCE over the whole window: summing the grouped rows would count a buyer
-- again for every source, link and currency they appear under.
WITH x AS (
    SELECT COALESCE(o.customer_id::text, lower(btrim(o.buyer_email)), o.id::text)
               || '|' || o.session_id::text
               || '|' || ((o.created_at AT TIME ZONE $5::text)::date)::text AS buyer,
           (o.status IN ('paid', 'partially_refunded', 'refunded')) AS paid,
           EXISTS (SELECT 1 FROM payment_intents pi
                   WHERE pi.checkout_session_id = o.checkout_session_id) AS has_intent
    FROM   orders o
    WHERE  o.event_id = $1 AND o.org_id = $2
      AND  o.source <> 'complimentary'
      AND  o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
      AND  (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
      AND  ($6::uuid IS NULL OR o.session_id = $6::uuid)
      AND  NOT EXISTS (
               SELECT 1 FROM sales_channels sc
               WHERE  sc.id = o.channel_id
                 AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
                       OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
           )
)
SELECT count(DISTINCT buyer)::bigint,
       (count(DISTINCT buyer) FILTER (WHERE paid OR has_intent))::bigint,
       (count(DISTINCT buyer) FILTER (WHERE paid))::bigint
FROM   x;

-- name: CountFunnelGatewayCarts :one
-- CountFunnelGatewayCarts counts the carts (reservations) taken for event $1
-- through channels that carry a gateway credential (WordPress plugins on the
-- Bil24 protocol), created in [$3, $4] by the clock of zone $5. These sites
-- have no client steps, so their funnel starts here.
SELECT count(*)::bigint
FROM   reservations r
JOIN   sessions s        ON s.id = r.session_id
JOIN   sales_channels sc ON sc.id = r.channel_id
WHERE  s.event_id = $1 AND r.org_id = $2
  AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
        OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
  AND  r.created_at >= $3::date - 1 AND r.created_at < $4::date + 2
  AND  (r.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
  AND  ($6::uuid IS NULL OR r.session_id = $6::uuid);

-- name: ListFunnelGatewayOrders :many
-- ListFunnelGatewayOrders is the order side of the gateway channels: one row
-- per currency with the orders created (CREATE_ORDER_EXT), those ever paid and
-- the revenue of the paid ones net of their succeeded refunds, in minor units.
-- Same window as CountFunnelGatewayCarts.
SELECT o.currency::text AS currency,
       count(*)::bigint AS started,
       (count(*) FILTER (WHERE o.status IN ('paid', 'partially_refunded', 'refunded')))::bigint AS paid,
       COALESCE(sum(o.total - rf.amount) FILTER (WHERE o.status IN ('paid', 'partially_refunded', 'refunded')), 0)::bigint AS revenue
FROM   orders o
JOIN   sales_channels sc ON sc.id = o.channel_id
LEFT JOIN LATERAL (
    SELECT COALESCE(sum(r.amount), 0)::bigint AS amount
    FROM  (
              SELECT r1.id FROM refunds r1 WHERE r1.order_id = o.id
              UNION
              SELECT r2.id FROM refunds r2 JOIN tickets t ON t.id = r2.ticket_id WHERE t.order_id = o.id
              UNION
              SELECT r3.id FROM refunds r3 JOIN payment_intents p3 ON p3.id = r3.payment_intent_id
              WHERE  p3.checkout_session_id = o.checkout_session_id
          ) cand
    JOIN   refunds r ON r.id = cand.id
    WHERE  r.state = 'succeeded'
) rf ON true
WHERE  o.event_id = $1 AND o.org_id = $2
  AND  o.source <> 'complimentary'
  AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
        OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
  AND  o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
  AND  (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
  AND  ($6::uuid IS NULL OR o.session_id = $6::uuid)
GROUP BY o.currency;

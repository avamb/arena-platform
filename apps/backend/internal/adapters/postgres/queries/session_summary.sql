-- session_summary.sql — read-only aggregates behind the one-screen session
-- overview (GET /v1/organizations/{org_id}/sessions/{session_id}/summary) and
-- the event overview (GET .../events/{event_id}/summary, EC-06).
--
-- Every aggregate takes the SET of session ids it runs over — one id for the
-- session summary, every session of the event for the event summary — and
-- tags each row with its session_id. horders folds the rows with ONE
-- assembly for both screens, so the two can never drift. No query returns
-- buyer data.

-- name: GetSessionSummaryHeader :one
SELECT s.id, s.event_id, e.org_id, e.name AS event_name, s.start_at,
       s.status, s.capacity_total, s.seating_plan_version_id,
       v.name AS venue_name, v.timezone AS venue_timezone
FROM      sessions s
JOIN      events   e ON e.id = s.event_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.id = $1
  AND  e.org_id = $2
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- name: GetEventSummaryHeader :one
SELECT e.id, e.org_id, e.name, e.status, e.first_session_at, e.last_session_at
FROM   events e
WHERE  e.id = $1
  AND  e.org_id = $2
  AND  e.deleted_at IS NULL;

-- name: ListEventSummarySessions :many
-- Every session of the event that still exists, cancelled ones included: a
-- cancelled session may carry refunded orders the event total must show.
SELECT s.id, s.start_at, s.end_at, s.status, s.capacity_total,
       s.seating_plan_version_id, v.name AS venue_name, v.timezone AS venue_timezone
FROM      sessions s
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.event_id = $1
  AND  s.deleted_at IS NULL
ORDER  BY s.start_at, s.id;

-- name: ListSessionSummaryPlaces :many
-- One row per (session, category, kind). sold_upstream is the part of `sold`
-- that was sold in the system the session was imported from: a 'sold' row
-- with no reservation behind it has no ticket and no order in arena.
SELECT ss.session_id, ss.tier_id, ss.kind,
       count(*) FILTER (WHERE ss.status = 'available')   AS available,
       count(*) FILTER (WHERE ss.status = 'held')        AS held,
       count(*) FILTER (WHERE ss.status = 'sold')        AS sold,
       count(*) FILTER (WHERE ss.status = 'sold' AND ss.reservation_id IS NULL) AS sold_upstream,
       count(*) FILTER (WHERE ss.status = 'unavailable') AS unavailable
FROM   session_seats ss
WHERE  ss.session_id = ANY($1::uuid[])
GROUP  BY ss.session_id, ss.tier_id, ss.kind;

-- name: ListSessionSummaryTiers :many
-- Money per category comes from order_items.total — what the buyer paid for
-- that unit after its share of the discount and of the service charge —
-- never from today's list price.
SELECT tt.session_id, tt.id, tt.name, tt.price_amount, tt.currency, tt.capacity,
       tt.is_open, tt.sort_order,
       COALESCE(sales.items, 0)   AS paid_items,
       COALESCE(sales.revenue, 0)::bigint AS paid_revenue
FROM   ticket_tiers tt
LEFT JOIN (
    SELECT oi.tier_id, count(*) AS items, sum(oi.total) AS revenue
    FROM   order_items oi
    JOIN   orders o ON o.id = oi.order_id
    WHERE  o.session_id = ANY($1::uuid[])
      AND  o.status IN ('paid', 'partially_refunded', 'refunded')
    GROUP  BY oi.tier_id
) sales ON sales.tier_id = tt.id
WHERE  tt.session_id = ANY($1::uuid[])
  AND  tt.deleted_at IS NULL
ORDER  BY tt.session_id, tt.sort_order, tt.name;

-- name: ListSessionSummaryOrders :many
SELECT o.session_id, o.status, o.source, o.currency,
       count(*)                     AS orders,
       COALESCE(sum(o.subtotal), 0)::bigint AS subtotal,
       COALESCE(sum(o.discount), 0)::bigint AS discount,
       COALESCE(sum(o.charge), 0)::bigint   AS charge,
       COALESCE(sum(o.total), 0)::bigint    AS total
FROM   orders o
WHERE  o.session_id = ANY($1::uuid[])
GROUP  BY o.session_id, o.status, o.source, o.currency;

-- name: ListSessionSummaryTickets :many
-- A session without tickets has no row; the assembly reads zeros for it.
SELECT t.session_id,
       count(*) FILTER (WHERE t.status = 'active')      AS active,
       count(*) FILTER (WHERE t.status = 'cancelled')   AS cancelled,
       count(*) FILTER (WHERE t.status = 'transferred') AS transferred,
       count(*) FILTER (WHERE t.status = 'active' AND t.used_at IS NOT NULL) AS used,
       count(*) FILTER (WHERE t.status = 'active' AND t.complimentary_issuance_id IS NOT NULL) AS complimentary
FROM   tickets t
WHERE  t.session_id = ANY($1::uuid[])
GROUP  BY t.session_id;

-- name: ListSessionSummaryRefunds :many
-- A refund reaches its session through the order it names, the ticket it
-- names, or — a provider refund older than migration 0102 — the checkout
-- session of its payment intent.
SELECT COALESCE(ro.session_id, rt.session_id, po.session_id) AS session_id,
       r.settlement, r.state, r.currency,
       count(*)                   AS refunds,
       COALESCE(sum(r.amount), 0)::bigint AS amount
FROM   refunds r
LEFT JOIN orders          ro ON ro.id = r.order_id
LEFT JOIN tickets         rt ON rt.id = r.ticket_id
LEFT JOIN payment_intents pi ON pi.id = r.payment_intent_id
LEFT JOIN orders          po ON po.checkout_session_id = pi.checkout_session_id
WHERE  COALESCE(ro.session_id, rt.session_id, po.session_id) = ANY($1::uuid[])
GROUP  BY COALESCE(ro.session_id, rt.session_id, po.session_id), r.settlement, r.state, r.currency;

-- name: ListSessionSummaryPromos :many
-- Which promo codes the paid orders of these sessions used and what they
-- cost. `orders` counts the paid orders carrying the code; `redemptions`
-- counts the promo_code_redemptions rows recorded for them (one per order
-- since migration 0108, so it is at most `orders` — a widget order paid
-- before 2026-09-22 has none).
SELECT o.session_id, p.id AS promo_code_id, p.code, o.currency,
       count(*)::bigint                     AS orders,
       count(r.id)::bigint                  AS redemptions,
       COALESCE(sum(o.discount), 0)::bigint AS discount
FROM   orders o
JOIN   promo_codes p ON p.id = o.promo_code_id
LEFT JOIN promo_code_redemptions r ON r.order_id = o.id AND r.promo_code_id = p.id
WHERE  o.session_id = ANY($1::uuid[])
  AND  o.status IN ('paid', 'partially_refunded', 'refunded')
GROUP  BY o.session_id, p.id, p.code, o.currency;

-- name: ListSessionSummaryComplimentary :many
-- Invitations, whichever way they were issued: orders written with
-- source='complimentary' (the gateway's CREATE_ORDER_EXT `complimentary`
-- flag) and tickets of the admin complimentary flow
-- (tickets.complimentary_issuance_id). One row per requested session.
SELECT sid.id AS session_id,
       (SELECT count(*)
          FROM   orders o
          WHERE  o.session_id = sid.id
            AND  o.source = 'complimentary'
            AND  o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders,
       (SELECT count(*)
          FROM   tickets t
          LEFT JOIN orders o ON o.id = t.order_id
          WHERE  t.session_id = sid.id
            AND  t.status = 'active'
            AND  (t.complimentary_issuance_id IS NOT NULL OR o.source = 'complimentary'))::bigint AS tickets
FROM   unnest($1::uuid[]) AS sid(id);

-- export.sql — the organizer's CSV exports (EC-07, spec 35 §5.8):
-- sales per ticket, the promo code usage. Every list query is a keyset page
-- (tickets by system_ticket_id, redemptions by their uuidv7 id) so a file is
-- streamed batch by batch and never loaded whole; every header query is
-- scoped to the organization so a foreign row is a 404.

-- name: GetExportEventHeader :one
SELECT e.id, e.org_id, e.name, e.slug
FROM   events e
WHERE  e.id = $1
  AND  e.org_id = $2
  AND  e.deleted_at IS NULL;

-- name: GetExportSessionHeader :one
SELECT s.id, s.event_id, e.org_id, e.name AS event_name, e.slug AS event_slug,
       s.start_at, v.timezone AS venue_timezone
FROM      sessions s
JOIN      events   e ON e.id = s.event_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.id = $1
  AND  e.org_id = $2
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- name: CountExportSalesRows :one
-- One of $1 (session) / $2 (event) is set; the other is NULL.
SELECT count(*)
FROM   tickets  t
JOIN   sessions s ON s.id = t.session_id
WHERE  ($1::uuid IS NULL OR t.session_id = $1::uuid)
  AND  ($2::uuid IS NULL OR s.event_id   = $2::uuid)
  AND  s.deleted_at IS NULL;

-- name: ListExportSalesRows :many
-- One row per ticket. The order is joined through tickets.order_id (NULL on
-- a pre-0092 ticket: the checkout session then supplies the channel and the
-- promo code, the buyer columns stay empty). The price is what the buyer
-- paid for THAT unit (order_items.total), never today's list price. The
-- barcode is the stored EAN-13 credential; the caller derives the legacy
-- PlatformCode when it is missing, exactly as orderexport does.
SELECT t.id, t.system_ticket_id, t.status AS ticket_status, t.used_at, t.issued_at,
       s.start_at AS session_start, v.timezone AS venue_timezone,
       o.system_id AS order_number, o.status AS order_status, o.created_at AS order_created_at,
       o.buyer_name, o.buyer_email, o.buyer_phone,
       COALESCE(o.currency, tt.currency) AS currency,
       tt.name AS tier_name,
       oi.total AS item_total,
       tc.payload AS barcode,
       sc.name AS channel_name,
       pc.code AS promo_code
FROM      tickets t
JOIN      sessions s            ON s.id = t.session_id
LEFT JOIN venues v              ON v.id = s.venue_id
LEFT JOIN orders o              ON o.id = t.order_id
LEFT JOIN checkout_sessions cs  ON cs.id = t.checkout_session_id
LEFT JOIN ticket_tiers tt       ON tt.id = t.tier_id
LEFT JOIN LATERAL (
    SELECT oi.total FROM order_items oi
    WHERE  oi.ticket_id = t.id
    ORDER  BY oi.ordinal
    LIMIT  1
) oi ON true
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
LEFT JOIN sales_channels sc     ON sc.id = COALESCE(o.channel_id, cs.channel_id)
LEFT JOIN promo_code_redemptions pr ON pr.order_id = o.id
LEFT JOIN promo_codes pc        ON pc.id = COALESCE(pr.promo_code_id, cs.promo_code_id)
WHERE  ($1::uuid IS NULL OR t.session_id = $1::uuid)
  AND  ($2::uuid IS NULL OR s.event_id   = $2::uuid)
  AND  s.deleted_at IS NULL
  AND  t.system_ticket_id > $3
ORDER  BY t.system_ticket_id
LIMIT  $4;

-- name: CountExportPromoRedemptions :one
SELECT count(*) FROM promo_code_redemptions r WHERE r.promo_code_id = $1;

-- name: ListExportPromoRedemptions :many
-- The order (and through it the buyer, the currency and the venue's zone)
-- is optional: a redemption written before migration 0108 names no order.
SELECT r.id, r.redeemed_at, r.discount_amount,
       o.system_id AS order_number, o.status AS order_status, o.currency,
       o.buyer_name, o.buyer_email,
       v.timezone AS venue_timezone
FROM      promo_code_redemptions r
LEFT JOIN orders   o ON o.id = r.order_id
LEFT JOIN sessions s ON s.id = o.session_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  r.promo_code_id = $1
  AND  r.id > $2
ORDER  BY r.id
LIMIT  $3;

-- org_summary.sql — the organization-wide counterpart of session_summary.sql
-- (EC-V1, spec 62 §12.2) and the accounting workbook (EC-V2). The money and
-- promo aggregates are NOT repeated here: horders feeds the session ids found
-- by ListOrgSummarySessions to ListSessionSummaryOrders / Refunds / Promos /
-- TestOrders and folds them with the same foldMoney as every other summary.
-- Only what those queries cannot say per currency lives below: ticket counts,
-- the price x category breakdown and the per-ticket accounting rows.

-- name: ListOrgSummarySessions :many
-- The organization's live sessions whose start falls in [from, to), oldest
-- first. from / to / event are optional (NULL = no bound). The caller asks
-- for one row more than it shows to learn whether the list was cut.
SELECT s.id, s.event_id, e.name AS event_name, s.start_at, s.status,
       v.name AS venue_name, v.timezone AS venue_timezone
FROM      sessions s
JOIN      events   e ON e.id = s.event_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  e.org_id = $1
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL
  AND  ($2::timestamptz IS NULL OR s.start_at >= $2::timestamptz)
  AND  ($3::timestamptz IS NULL OR s.start_at <  $3::timestamptz)
  AND  ($4::uuid IS NULL OR e.id = $4::uuid)
ORDER  BY s.start_at, s.id
LIMIT  $5;

-- name: ListSessionSummaryTicketCards :many
-- Tickets per (session, currency), test orders left out. The currency is the
-- order's, else the category's (an invitation issued without an order).
-- sold = tickets that were not invitations, whatever became of them since;
-- refunded = those among them cancelled with money returned (either
-- settlement); active = valid tickets of every kind.
SELECT t.session_id,
       COALESCE(o.currency, tt.currency, '') AS currency,
       count(*) FILTER (WHERE NOT f.comp)::bigint AS sold,
       count(*) FILTER (WHERE f.comp)::bigint     AS complimentary,
       count(*) FILTER (WHERE NOT f.comp AND t.status = 'cancelled' AND f.refunded)::bigint AS refunded,
       count(*) FILTER (WHERE t.status = 'active')::bigint AS active
FROM      tickets t
LEFT JOIN orders       o  ON o.id  = t.order_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
CROSS JOIN LATERAL (
    SELECT (t.complimentary_issuance_id IS NOT NULL OR o.source = 'complimentary') AS comp,
           (t.refund_id IS NOT NULL OR t.refund_price IS NOT NULL
            OR EXISTS (SELECT 1 FROM refunds r WHERE r.ticket_id = t.id AND r.state = 'succeeded')) AS refunded
) f
WHERE  t.session_id = ANY($1::uuid[])
  AND  (o.id IS NULL OR (NOT o.is_test AND o.status IN ('paid', 'partially_refunded', 'refunded')))
GROUP  BY t.session_id, COALESCE(o.currency, tt.currency, '');

-- name: ListSessionSummaryBreakdown :many
-- "Price x category": the units of the paid orders grouped by category, the
-- price the unit was sold at (order_items.unit_price, before discount and
-- service charge) and the promo code on the order. revenue is what the buyer
-- paid for those units (order_items.total), so the rows add up to the money
-- card. Invitations are not here, they have their own count.
SELECT o.session_id, o.currency, oi.tier_id, tt.name AS tier_name, oi.unit_price,
       COALESCE(pc.code, '') AS promo_code,
       count(*)::bigint                       AS tickets,
       COALESCE(sum(oi.total), 0)::bigint     AS revenue,
       COALESCE(sum(oi.discount), 0)::bigint  AS discount
FROM      order_items oi
JOIN      orders       o  ON o.id  = oi.order_id
JOIN      ticket_tiers tt ON tt.id = oi.tier_id
LEFT JOIN promo_codes  pc ON pc.id = o.promo_code_id
WHERE  o.session_id = ANY($1::uuid[])
  AND  o.status IN ('paid', 'partially_refunded', 'refunded')
  AND  NOT o.is_test
  AND  o.source <> 'complimentary'
GROUP  BY o.session_id, o.currency, oi.tier_id, tt.name, oi.unit_price, COALESCE(pc.code, '')
ORDER  BY o.session_id, tt.name, oi.unit_price, COALESCE(pc.code, '');

-- name: CountAccountingTickets :one
SELECT count(*)
FROM      tickets t
LEFT JOIN orders  o ON o.id = t.order_id
WHERE  t.session_id = ANY($1::uuid[])
  AND  (o.id IS NULL OR (NOT o.is_test AND o.status IN ('paid', 'partially_refunded', 'refunded')));

-- name: ListAccountingTickets :many
-- One row per ticket of the given sessions, test orders left out, keyset by
-- system_ticket_id. The price is what the buyer paid for THAT unit
-- (order_items.total). The refund is the ticket's succeeded refund: its own
-- amount (tickets.refund_price, else the refund row naming the ticket), its
-- settlement and a reference (the provider's id; for an external refund, the
-- reason the selling site gave).
SELECT t.system_ticket_id, t.status AS ticket_status, t.issued_at,
       s.start_at AS session_start, v.timezone AS venue_timezone, e.name AS event_name,
       o.system_id AS order_number, o.buyer_name, o.buyer_email, o.buyer_phone,
       COALESCE(o.currency, tt.currency, '') AS currency,
       tt.name AS tier_name,
       oi.total AS item_total,
       (t.complimentary_issuance_id IS NOT NULL OR o.source = 'complimentary') AS complimentary,
       COALESCE(t.refund_price, rf.ticket_amount) AS refund_amount,
       rf.settlement AS refund_settlement,
       rf.ref AS refund_ref
FROM      tickets t
JOIN      sessions s     ON s.id = t.session_id
JOIN      events   e     ON e.id = s.event_id
LEFT JOIN venues   v     ON v.id = s.venue_id
LEFT JOIN orders   o     ON o.id = t.order_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN LATERAL (
    SELECT oi.total FROM order_items oi
    WHERE  oi.ticket_id = t.id
    ORDER  BY oi.ordinal
    LIMIT  1
) oi ON true
LEFT JOIN LATERAL (
    SELECT r.settlement,
           CASE WHEN r.ticket_id = t.id THEN r.amount END AS ticket_amount,
           COALESCE(NULLIF(r.provider_refund_id, ''),
                    CASE WHEN r.settlement = 'external' THEN NULLIF(r.reason, '') END) AS ref
    FROM   refunds r
    WHERE  r.state = 'succeeded'
      AND  r.origin <> 'referral_cashback'
      AND  (r.id = t.refund_id OR r.ticket_id = t.id)
    ORDER  BY r.succeeded_at DESC NULLS LAST, r.id
    LIMIT  1
) rf ON true
WHERE  t.session_id = ANY($1::uuid[])
  AND  (o.id IS NULL OR (NOT o.is_test AND o.status IN ('paid', 'partially_refunded', 'refunded')))
  AND  t.system_ticket_id > $2
ORDER  BY t.system_ticket_id
LIMIT  $3;

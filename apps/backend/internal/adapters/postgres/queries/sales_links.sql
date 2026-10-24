-- sales_links.sql — tracked sales links and QR codes (spec 43, task QL-1).
-- Tables sales_links and sales_link_hits, columns orders.sales_link_id and
-- orders.attribution come from migration 0167.
--
-- Every query that returns a link row returns the same 21 columns, the last
-- one being orders_count: the number of orders that came through the link and
-- were ever paid (paid, partially_refunded, refunded).

-- name: InsertSalesLinkIfCodeFree :one
-- A lost code (somebody holds it already) is zero rows and no error: the
-- caller draws a new code and tries again.
INSERT INTO sales_links (org_id, code, short_host, target_kind, event_id, session_id,
                         promoter_id, url, label, placement_kind, utm, created_by, created_via)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE($11::jsonb, '{}'::jsonb), $12, $13)
ON CONFLICT (code) DO NOTHING
RETURNING sales_links.id, sales_links.org_id, sales_links.code, sales_links.short_host,
          sales_links.target_kind, sales_links.event_id, sales_links.session_id,
          sales_links.promoter_id, sales_links.url, sales_links.label,
          sales_links.placement_kind, sales_links.utm, sales_links.status,
          sales_links.scan_count, sales_links.suspect_scan_count, sales_links.last_scanned_at,
          sales_links.created_by, sales_links.created_via, sales_links.created_at,
          sales_links.updated_at,
          (SELECT count(*) FROM orders o WHERE o.sales_link_id = sales_links.id
                   AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders_count;

-- name: GetSalesLinkByCode :one
SELECT sales_links.id, sales_links.org_id, sales_links.code, sales_links.short_host,
       sales_links.target_kind, sales_links.event_id, sales_links.session_id,
       sales_links.promoter_id, sales_links.url, sales_links.label,
       sales_links.placement_kind, sales_links.utm, sales_links.status,
       sales_links.scan_count, sales_links.suspect_scan_count, sales_links.last_scanned_at,
       sales_links.created_by, sales_links.created_via, sales_links.created_at,
       sales_links.updated_at,
       (SELECT count(*) FROM orders o WHERE o.sales_link_id = sales_links.id
                AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders_count
FROM sales_links WHERE code = lower($1);

-- name: GetSalesLinkByID :one
SELECT sales_links.id, sales_links.org_id, sales_links.code, sales_links.short_host,
       sales_links.target_kind, sales_links.event_id, sales_links.session_id,
       sales_links.promoter_id, sales_links.url, sales_links.label,
       sales_links.placement_kind, sales_links.utm, sales_links.status,
       sales_links.scan_count, sales_links.suspect_scan_count, sales_links.last_scanned_at,
       sales_links.created_by, sales_links.created_via, sales_links.created_at,
       sales_links.updated_at,
       (SELECT count(*) FROM orders o WHERE o.sales_link_id = sales_links.id
                AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders_count
FROM sales_links WHERE id = $1 AND org_id = $2;

-- name: ListSalesLinks :many
-- Newest first. The cursor is the (created_at, id) of the last row of the
-- previous page. $4 is '' for every status, 'active' or 'archived'.
SELECT sales_links.id, sales_links.org_id, sales_links.code, sales_links.short_host,
       sales_links.target_kind, sales_links.event_id, sales_links.session_id,
       sales_links.promoter_id, sales_links.url, sales_links.label,
       sales_links.placement_kind, sales_links.utm, sales_links.status,
       sales_links.scan_count, sales_links.suspect_scan_count, sales_links.last_scanned_at,
       sales_links.created_by, sales_links.created_via, sales_links.created_at,
       sales_links.updated_at,
       (SELECT count(*) FROM orders o WHERE o.sales_link_id = sales_links.id
                AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders_count
FROM sales_links
WHERE org_id = $1
  AND ($2::uuid IS NULL OR event_id = $2)
  AND ($3::uuid IS NULL OR session_id = $3)
  AND ($4::text = '' OR status = $4)
  AND ($5::timestamptz IS NULL OR (created_at, id) < ($5, $6::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $7;

-- name: UpdateSalesLink :one
-- Tri-state: a column changes only when its set_<field> flag ($3, $5, ...) is
-- true. The target group (kind and the four columns it chooses between) is
-- written together under one flag.
UPDATE sales_links
SET label          = CASE WHEN $3::boolean  THEN $4::text  ELSE label          END,
    placement_kind = CASE WHEN $5::boolean  THEN $6::text  ELSE placement_kind END,
    utm            = CASE WHEN $7::boolean  THEN COALESCE($8::jsonb, '{}'::jsonb) ELSE utm END,
    status         = CASE WHEN $9::boolean  THEN $10::text ELSE status         END,
    target_kind    = CASE WHEN $11::boolean THEN $12::text ELSE target_kind    END,
    event_id       = CASE WHEN $11::boolean THEN $13::uuid ELSE event_id       END,
    session_id     = CASE WHEN $11::boolean THEN $14::uuid ELSE session_id     END,
    promoter_id    = CASE WHEN $11::boolean THEN $15::uuid ELSE promoter_id    END,
    url            = CASE WHEN $11::boolean THEN $16::text ELSE url            END,
    updated_at     = now()
WHERE id = $1 AND org_id = $2
RETURNING sales_links.id, sales_links.org_id, sales_links.code, sales_links.short_host,
          sales_links.target_kind, sales_links.event_id, sales_links.session_id,
          sales_links.promoter_id, sales_links.url, sales_links.label,
          sales_links.placement_kind, sales_links.utm, sales_links.status,
          sales_links.scan_count, sales_links.suspect_scan_count, sales_links.last_scanned_at,
          sales_links.created_by, sales_links.created_via, sales_links.created_at,
          sales_links.updated_at,
          (SELECT count(*) FROM orders o WHERE o.sales_link_id = sales_links.id
                   AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders_count;

-- name: BumpSalesLinkScan :exec
UPDATE sales_links
SET scan_count         = scan_count + CASE WHEN $2::boolean THEN 0 ELSE 1 END,
    suspect_scan_count = suspect_scan_count + CASE WHEN $2::boolean THEN 1 ELSE 0 END,
    last_scanned_at    = now()
WHERE id = $1;

-- name: UpsertSalesLinkHit :exec
INSERT INTO sales_link_hits (sales_link_id, day, country, hits, suspect_hits)
VALUES ($1, $2::date, $3, CASE WHEN $4::boolean THEN 0 ELSE 1 END, CASE WHEN $4::boolean THEN 1 ELSE 0 END)
ON CONFLICT (sales_link_id, day, country) DO UPDATE
   SET hits         = sales_link_hits.hits         + EXCLUDED.hits,
       suspect_hits = sales_link_hits.suspect_hits + EXCLUDED.suspect_hits;

-- name: SalesLinkOrdersAggregate :many
-- Per currency: orders that were ever paid, their tickets, and the revenue =
-- sum of orders.total minus the SUCCEEDED refunds of those orders. A refund
-- reaches exactly ONE order: order_id, else its ticket's order, else the order
-- of its payment intent (the COALESCE order of ListSessionSummaryRefunds). The
-- link must belong to the organization or the answer is empty.
WITH link_orders AS (
    SELECT o.id, o.currency, o.total, o.checkout_session_id
    FROM   orders o
    JOIN   sales_links sl ON sl.id = o.sales_link_id AND sl.org_id = $2
    WHERE  o.sales_link_id = $1
      AND  o.org_id        = $2
      AND  o.status IN ('paid', 'partially_refunded', 'refunded')
)
SELECT lo.currency,
       count(*)::bigint AS orders,
       COALESCE(sum(tk.cnt), 0)::bigint AS tickets,
       (COALESCE(sum(lo.total), 0) - COALESCE(sum(rf.amount), 0))::bigint AS revenue_minor
FROM   link_orders lo
LEFT JOIN LATERAL (
    SELECT count(*) AS cnt FROM tickets t WHERE t.order_id = lo.id
) tk ON true
LEFT JOIN LATERAL (
    SELECT COALESCE(sum(r.amount), 0) AS amount
    FROM   refunds r
    WHERE  r.state  = 'succeeded'
      AND  r.org_id = $2
      AND  COALESCE(r.order_id,
                    (SELECT t.order_id FROM tickets t WHERE t.id = r.ticket_id),
                    (SELECT po.id FROM payment_intents pi
                     JOIN   orders po ON po.checkout_session_id = pi.checkout_session_id
                     WHERE  pi.id = r.payment_intent_id)) = lo.id
) rf ON true
GROUP  BY lo.currency
ORDER  BY lo.currency;

-- name: SalesLinkHitsByDay :many
-- from and to are calendar days, both inclusive.
SELECT h.day, sum(h.hits)::bigint AS hits, sum(h.suspect_hits)::bigint AS suspect_hits
FROM   sales_link_hits h
JOIN   sales_links sl ON sl.id = h.sales_link_id
WHERE  h.sales_link_id = $1
  AND  sl.org_id       = $2
  AND  h.day >= $3::date AND h.day <= $4::date
GROUP  BY h.day
ORDER  BY h.day;

-- name: SalesLinkHitsByCountry :many
SELECT h.country, sum(h.hits)::bigint AS hits, sum(h.suspect_hits)::bigint AS suspect_hits
FROM   sales_link_hits h
JOIN   sales_links sl ON sl.id = h.sales_link_id
WHERE  h.sales_link_id = $1
  AND  sl.org_id       = $2
  AND  h.day >= $3::date AND h.day <= $4::date
GROUP  BY h.country
ORDER  BY sum(h.hits) DESC, h.country;

-- orders.sql — sqlc query definitions for the order aggregate (orders,
-- order_items, order_events) introduced by migration 0092 (W1-A6a,
-- feature #486, spec §3.3).
--
-- Model overview: an order is 1:1 with the checkout_sessions/reservations
-- rows that produced it. order_items has one row PER UNIT (ticket or GA
-- unit), never per category — that is what GET_CART.seatList and
-- CREATE_ORDER_EXT.ticketList need on the wire. order_events is an
-- append-only audit trail. Money columns are bigint minor units; total is
-- schema-checked as subtotal - discount + charge.
--
-- The ordering package (CreateOrderFromCheckout / MarkPaid / Cancel /
-- Expire / ReconcileLines, epic #456 step 2) is a later sub-feature; this
-- file only provides the typed data-access primitives it and the future
-- orders HTTP handlers will compose.

-- name: InsertOrder :one
-- Creates a new order row. system_id defaults to the next value of
-- compatibility_system_id_seq (>= 1e9), matching customers/tickets so
-- Bil24 wire responses can surface it as orderId. The DB CHECK
-- (total = subtotal - discount + charge) guards the money invariant even
-- if a caller passes inconsistent values.
INSERT INTO orders (
    org_id, channel_id, event_id, session_id, customer_id,
    checkout_session_id, reservation_id, external_ref, source, status,
    currency, subtotal, discount, charge, total, charge_percent_bp,
    promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
    expires_at, metadata
)
VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10,
    $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21,
    $22, $23
)
RETURNING id, system_id, org_id, channel_id, event_id, session_id, customer_id,
          checkout_session_id, reservation_id, external_ref, source, status,
          currency, subtotal, discount, charge, total, charge_percent_bp,
          promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
          paid_at, cancelled_at, expires_at, metadata, created_at, updated_at;

-- name: GetOrderByID :one
-- Loads an order scoped to its organization. Returns pgx.ErrNoRows when
-- absent, belonging to another org, or the id is unknown.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  id = $1
  AND  org_id = $2;

-- name: LockOrderForUpdate :one
-- Payment-window contract (owner decision 2026-09-13, spec §7.9 replacement):
-- PAY_ORDER takes a row-level lock on the order FIRST, before deciding
-- whether to pay, cancel, or expire it, so that concurrent PAY_ORDER calls
-- for the SAME order (a WordPress retry storm, or several requests landing
-- right at the payment-window boundary) serialize on this lock instead of
-- racing a read-then-write decision — MarkPaid's own UpdateOrderStatus has
-- no status guard at the SQL level, so without this lock a slow transaction
-- could blindly overwrite a status a faster, concurrent transaction already
-- moved on. Returns pgx.ErrNoRows when the order does not exist or belongs
-- to another org. MUST be called inside a transaction; the lock is held
-- until commit/rollback.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  id = $1
  AND  org_id = $2
FOR UPDATE;

-- name: GetOrderBySystemID :one
-- Loads an order by the bigint system_id exposed to Bil24 clients as
-- orderId (GET_ORDER_INFO, spec §7.8). Returns pgx.ErrNoRows when absent.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  system_id = $1;

-- name: GetOrderByCheckoutSession :one
-- Loads the order minted from a checkout session (orders is 1:1 with
-- checkout_sessions). This is the lookup both ticket issuance and the
-- payment webhook need: they hold a checkout_session_id, not an order id
-- (W1-A6c, feature #488, spec §7.9 step 5 / §14.1). Returns pgx.ErrNoRows
-- for checkout sessions that never produced an order — a legitimate state
-- for pre-#488 sessions, so callers degrade instead of failing.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  checkout_session_id = $1;

-- name: GetOrderByReservationID :one
-- Loads the order holding a given reservation (orders is 1:1 with the
-- reservation that backs its hold). CANCEL_RESERVATION (spec §7.12, feature
-- #496) is keyed on the wire `reservationId` returned by RESERVATION rather
-- than an order id, so this is its lookup path. Returns pgx.ErrNoRows for a
-- reservation that never became an order (or does not exist), which the
-- caller maps to the spec's "unknown id -> 0" contract.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  reservation_id = $1;

-- name: FindOpenOrderByCustomerSession :one
-- Resolves the single pending_payment order for a (customer, event
-- session) pair, mirroring the partial unique index
-- orders_one_pending_per_customer_session_uq. Used by CREATE_ORDER_EXT's
-- "one open order per customer+session" rule (spec §3.3/§7.7) to decide
-- between reusing and creating an order. Returns pgx.ErrNoRows when there
-- is no open order.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  customer_id = $1
  AND  session_id = $2
  AND  status = 'pending_payment';

-- name: ListOrdersByOrg :many
-- Lists orders for an organization, most recent first, optionally
-- filtered by status, event session, a created_at range, and
-- fuzzy-matched against buyer_name / buyer_email / buyer_phone via pg_trgm
-- similarity (empty search = no filtering; the orders_buyer_*_trgm gin
-- indexes back this predicate). Pass an empty string for statusFilter/
-- search, and nil for sessionID/from/to, to skip the corresponding filter
-- (W1-A6d, feature #489, spec §14.2).
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  org_id = $1
  AND  ($2 = '' OR status = $2)
  AND  ($3 = '' OR buyer_name  % $3 OR buyer_email % $3 OR buyer_phone % $3)
  AND  ($4::uuid IS NULL OR session_id = $4)
  AND  ($5::timestamptz IS NULL OR created_at >= $5)
  AND  ($6::timestamptz IS NULL OR created_at <= $6)
ORDER  BY created_at DESC, id DESC
LIMIT  $7 OFFSET $8;

-- name: UpdateOrderStatus :one
-- Transitions status and stamps the matching lifecycle timestamp
-- (paidAt/cancelledAt are both nullable; pass nil to leave the existing
-- value untouched, non-nil to set it). Used by the ordering package's
-- MarkPaid / Cancel / Expire. Returns pgx.ErrNoRows when the order does
-- not exist or belongs to another org.
UPDATE orders
SET    status       = $3,
       paid_at      = COALESCE($4, paid_at),
       cancelled_at = COALESCE($5, cancelled_at),
       updated_at   = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING id, system_id, org_id, channel_id, event_id, session_id, customer_id,
          checkout_session_id, reservation_id, external_ref, source, status,
          currency, subtotal, discount, charge, total, charge_percent_bp,
          promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
          paid_at, cancelled_at, expires_at, metadata, created_at, updated_at;

-- name: SetOrderPaymentMethod :exec
-- Records how the order was paid (spec §7.9: the Bil24 compat gateway's
-- PAY_ORDER stores the WooCommerce payment method verbatim). Separate from
-- UpdateOrderStatus so the ordering aggregate's transition guard stays the
-- single owner of the status column.
UPDATE orders
SET    payment_method = $3,
       updated_at     = now()
WHERE  id = $1
  AND  org_id = $2;

-- name: InsertOrderItem :one
-- Adds one unit (ticket or GA unit) to an order. session_seat_id is null
-- for GA units minted without a seat row; ticket_id is null until
-- IssueTicketsForCheckout backfills it via UpdateOrderItemTicket.
INSERT INTO order_items (
    order_id, ordinal, kind, tier_id, session_seat_id, ticket_id,
    unit_price, discount, charge, total
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, order_id, ordinal, kind, tier_id, session_seat_id, ticket_id,
          unit_price, discount, charge, total;

-- name: ListOrderItemsByOrder :many
-- Enumerates every line of an order in wire order (spec §3.3: order_items
-- is one row per unit, so this feeds GET_ORDER_INFO.ticketList /
-- GET_TICKETS_BY_ORDER directly).
SELECT id, order_id, ordinal, kind, tier_id, session_seat_id, ticket_id,
       unit_price, discount, charge, total
FROM   order_items
WHERE  order_id = $1
ORDER  BY ordinal ASC;

-- name: UpdateOrderItemTicket :exec
-- Backfills ticket_id on an order item once IssueTicketsForCheckout mints
-- the ticket row for that unit.
UPDATE order_items
SET    ticket_id = $2
WHERE  id = $1;

-- name: InsertOrderEvent :one
-- Appends one audit-trail row. type is a free-form string (spec §3.3:
-- created|lines_reconciled|paid|amount_mismatch|hold_expired|
-- hold_reacquired|cancelled|ticket_refunded|note); actor is
-- 'gateway:<channel display_number>' | 'user:<uuid>' | 'system'.
INSERT INTO order_events (order_id, type, actor, payload)
VALUES ($1, $2, $3, $4)
RETURNING id, order_id, type, actor, payload, created_at;

-- name: ListOrderEventsByOrder :many
-- Enumerates the audit trail of an order, oldest first (the order-drawer
-- timeline reads this in this order).
SELECT id, order_id, type, actor, payload, created_at
FROM   order_events
WHERE  order_id = $1
ORDER  BY created_at ASC;

-- name: ListExpirableOrders :many
-- W1-A6b (feature #487): the order.expire_sweep worker job's candidate set —
-- pending_payment orders whose hold deadline has passed and whose checkout
-- session never produced a succeeded payment intent (spec §14.1). The
-- NOT EXISTS guard is what keeps a payment that landed in the gap between
-- expires_at and the sweep tick from being expired underneath the buyer:
-- those orders are left alone for MarkPaid to pick up.
SELECT o.id, o.system_id, o.org_id, o.channel_id, o.event_id, o.session_id,
       o.customer_id, o.checkout_session_id, o.reservation_id, o.external_ref,
       o.source, o.status, o.currency, o.subtotal, o.discount, o.charge,
       o.total, o.charge_percent_bp, o.promo_code_id, o.buyer_name,
       o.buyer_email, o.buyer_phone, o.payment_method, o.paid_at,
       o.cancelled_at, o.expires_at, o.metadata, o.created_at, o.updated_at
FROM   orders o
WHERE  o.status = 'pending_payment'
  AND  o.expires_at IS NOT NULL
  AND  o.expires_at < $1
  AND  NOT EXISTS (
           SELECT 1 FROM payment_intents pi
           WHERE  pi.checkout_session_id = o.checkout_session_id
             AND  pi.state = 'succeeded'
       )
ORDER  BY o.expires_at ASC
LIMIT  $2;

-- name: ListOrdersByCustomerAndOrg :many
-- W1-A4d (feature #482): the customer card's "org orders" tab — every order
-- this customer placed within one org, most recent first. Unlike
-- ListOrdersByOrg this is an exact customer_id match, not a trgm search.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  org_id = $1
  AND  customer_id = $2
ORDER  BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4;

-- name: ExpireOrderIfStillPending :one
-- Flips one order to 'expired', but only while it is still pending_payment.
-- The status guard makes the sweep safe to run next to a payment webhook:
-- whichever transaction commits second sees zero rows and backs off rather
-- than clobbering a paid order. Returns pgx.ErrNoRows when the order moved
-- on already.
UPDATE orders
SET    status     = 'expired',
       updated_at = now()
WHERE  id     = $1
  AND  status = 'pending_payment'
RETURNING id, system_id, org_id, channel_id, event_id, session_id, customer_id,
          checkout_session_id, reservation_id, external_ref, source, status,
          currency, subtotal, discount, charge, total, charge_percent_bp,
          promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
          paid_at, cancelled_at, expires_at, metadata, created_at, updated_at;

-- name: SearchOrdersByOrg :many
-- EC-04 (spec 35 §5.5): the event-center order search. One page of an
-- organization's orders, newest first, joined with the event name and the
-- session start / venue zone so a client can print "№ · status · total ·
-- buyer · date" from the list alone. Filters: status (exact), tab
-- ('' | 'paid' | 'unpaid' — see the handler's tab mapping), session_id,
-- event_id, a created_at range, and ONE search predicate chosen by the
-- handler's classifier from the single `q` parameter. The search matchers
-- are OR-ed, so the classifier may populate several at once (a bare
-- 10-digit number is tried as orders.system_id AND as a phone):
--   $8  barcode          exact EAN-13 of a ticket of the order, through
--                        barcodes.external_ref (any authority) or the
--                        stored ean13 ticket credential;
--   $9  legacy ticket id the system_ticket_id decoded from a legacy
--                        deterministic code (ean13.PlatformCode) for a
--                        ticket that has no stored credential;
--   $10 system id        orders.system_id exact;
--   $11 email            lower-cased exact buyer_email, or an email
--                        identity of the order's customer;
--   $12 phone digits     digits-only phone (leading 00/+ stripped) against
--                        the buyer_phone digits, a 9+ digit suffix of them
--                        (a national number without its country code), or
--                        a phone identity of the customer;
--   $13 text             the pg_trgm similarity over buyer name/email/phone.
-- Every matcher empty = no search filter.
SELECT o.id, o.system_id, o.org_id, o.channel_id, o.event_id, o.session_id, o.customer_id,
       o.checkout_session_id, o.reservation_id, o.external_ref, o.source, o.status,
       o.currency, o.subtotal, o.discount, o.charge, o.total, o.charge_percent_bp,
       o.promo_code_id, o.buyer_name, o.buyer_email, o.buyer_phone, o.payment_method,
       o.paid_at, o.cancelled_at, o.expires_at, o.metadata, o.created_at, o.updated_at,
       e.name AS event_name, s.start_at AS session_start_at, v.timezone AS session_timezone
FROM   orders o
JOIN   events   e ON e.id = o.event_id
JOIN   sessions s ON s.id = o.session_id
JOIN   venues   v ON v.id = s.venue_id
WHERE  o.org_id = $1
  AND  ($2 = '' OR o.status = $2)
  AND  ($3 = ''
        OR ($3 = 'paid'   AND o.status IN ('paid', 'partially_refunded', 'refunded'))
        OR ($3 = 'unpaid' AND o.status IN ('pending_payment', 'expired', 'cancelled', 'abandoned', 'manual_review')))
  AND  ($4::uuid IS NULL OR o.session_id = $4)
  AND  ($5::uuid IS NULL OR o.event_id = $5)
  AND  ($6::timestamptz IS NULL OR o.created_at >= $6)
  AND  ($7::timestamptz IS NULL OR o.created_at <= $7)
  AND  (
        ($8 = '' AND $9::bigint IS NULL AND $10::bigint IS NULL AND $11 = '' AND $12 = '' AND $13 = '')
     OR ($8 <> '' AND o.id IN (
            SELECT oi.order_id FROM barcodes b
            JOIN   order_items oi ON oi.ticket_id = b.ticket_id
            WHERE  b.external_ref = $8
            UNION
            SELECT oi.order_id FROM ticket_credentials tc
            JOIN   order_items oi ON oi.ticket_id = tc.ticket_id
            WHERE  tc.type = 'ean13' AND tc.payload = $8))
     OR ($9::bigint IS NOT NULL AND o.id IN (
            SELECT oi.order_id FROM tickets t
            JOIN   order_items oi ON oi.ticket_id = t.id
            WHERE  t.system_ticket_id = $9
              AND  NOT EXISTS (SELECT 1 FROM ticket_credentials tc
                               WHERE tc.ticket_id = t.id AND tc.type = 'ean13')))
     OR ($10::bigint IS NOT NULL AND o.system_id = $10)
     OR ($11 <> '' AND (lower(o.buyer_email) = $11 OR o.customer_id IN (
            SELECT ci.customer_id FROM customer_identities ci
            WHERE  ci.kind = 'email' AND ci.value_normalized = $11)))
     OR ($12 <> '' AND (
            regexp_replace(regexp_replace(COALESCE(o.buyer_phone, ''), '\D', '', 'g'), '^00', '') = $12
         OR (length($12) >= 9 AND regexp_replace(COALESCE(o.buyer_phone, ''), '\D', '', 'g') LIKE '%' || $12)
         OR o.customer_id IN (
            SELECT ci.customer_id FROM customer_identities ci
            WHERE  ci.kind = 'phone' AND regexp_replace(ci.value_normalized, '\D', '', 'g') = $12)))
     OR ($13 <> '' AND (o.buyer_name % $13 OR o.buyer_email % $13 OR o.buyer_phone % $13))
  )
ORDER  BY o.created_at DESC, o.id DESC
LIMIT  $14 OFFSET $15;

-- name: CountOrdersByOrg :one
-- The total behind one SearchOrdersByOrg page: identical FROM/WHERE
-- ($1..$13), no ORDER/LIMIT. The two share the WHERE text in the Go
-- wrapper so the filters cannot drift apart.
SELECT count(*)
FROM   orders o
JOIN   events   e ON e.id = o.event_id
JOIN   sessions s ON s.id = o.session_id
JOIN   venues   v ON v.id = s.venue_id
WHERE  o.org_id = $1
  AND  ($2 = '' OR o.status = $2)
  AND  ($3 = ''
        OR ($3 = 'paid'   AND o.status IN ('paid', 'partially_refunded', 'refunded'))
        OR ($3 = 'unpaid' AND o.status IN ('pending_payment', 'expired', 'cancelled', 'abandoned', 'manual_review')))
  AND  ($4::uuid IS NULL OR o.session_id = $4)
  AND  ($5::uuid IS NULL OR o.event_id = $5)
  AND  ($6::timestamptz IS NULL OR o.created_at >= $6)
  AND  ($7::timestamptz IS NULL OR o.created_at <= $7)
  AND  (
        ($8 = '' AND $9::bigint IS NULL AND $10::bigint IS NULL AND $11 = '' AND $12 = '' AND $13 = '')
     OR ($8 <> '' AND o.id IN (
            SELECT oi.order_id FROM barcodes b
            JOIN   order_items oi ON oi.ticket_id = b.ticket_id
            WHERE  b.external_ref = $8
            UNION
            SELECT oi.order_id FROM ticket_credentials tc
            JOIN   order_items oi ON oi.ticket_id = tc.ticket_id
            WHERE  tc.type = 'ean13' AND tc.payload = $8))
     OR ($9::bigint IS NOT NULL AND o.id IN (
            SELECT oi.order_id FROM tickets t
            JOIN   order_items oi ON oi.ticket_id = t.id
            WHERE  t.system_ticket_id = $9
              AND  NOT EXISTS (SELECT 1 FROM ticket_credentials tc
                               WHERE tc.ticket_id = t.id AND tc.type = 'ean13')))
     OR ($10::bigint IS NOT NULL AND o.system_id = $10)
     OR ($11 <> '' AND (lower(o.buyer_email) = $11 OR o.customer_id IN (
            SELECT ci.customer_id FROM customer_identities ci
            WHERE  ci.kind = 'email' AND ci.value_normalized = $11)))
     OR ($12 <> '' AND (
            regexp_replace(regexp_replace(COALESCE(o.buyer_phone, ''), '\D', '', 'g'), '^00', '') = $12
         OR (length($12) >= 9 AND regexp_replace(COALESCE(o.buyer_phone, ''), '\D', '', 'g') LIKE '%' || $12)
         OR o.customer_id IN (
            SELECT ci.customer_id FROM customer_identities ci
            WHERE  ci.kind = 'phone' AND regexp_replace(ci.value_normalized, '\D', '', 'g') = $12)))
     OR ($13 <> '' AND (o.buyer_name % $13 OR o.buyer_email % $13 OR o.buyer_phone % $13))
  );

-- name: ListOrderTicketDetails :many
-- EC-05 (spec 35 §5.6): every ticket of an order with what the order card
-- prints — the line price (order_items.total, what the buyer paid for THAT
-- unit), the category name, the EAN-13 (stored credential; the handler
-- falls back to ean13.PlatformCode for a pre-#502 ticket, as orderexport
-- does), the entry time (the latest scanned_at of the ticket's barcodes,
-- any authority) and the ticket's delivery_jobs row (at most one per
-- ticket since migration 0067). Items whose ticket is not issued yet are
-- not listed here; ListOrderItemsByOrder still carries them.
SELECT oi.id AS item_id, oi.ordinal, oi.total AS price, oi.tier_id, tt.name AS tier_name,
       t.id AS ticket_id, t.status, t.holder_email, t.seat_sector, t.seat_row, t.seat_number,
       t.issued_at, t.cancelled_at, t.system_ticket_id,
       tc.payload AS barcode_str,
       sc.scanned_at AS used_at,
       dj.status AS delivery_status, dj.sent_at AS delivery_sent_at, dj.last_error AS delivery_error
FROM   order_items oi
JOIN   tickets t ON t.id = oi.ticket_id
LEFT JOIN ticket_tiers tt ON tt.id = oi.tier_id
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
LEFT JOIN LATERAL (
    SELECT b.scanned_at FROM barcodes b
    WHERE  b.ticket_id = t.id AND b.scanned_at IS NOT NULL
    ORDER  BY b.scanned_at DESC
    LIMIT  1) sc ON true
LEFT JOIN delivery_jobs dj ON dj.ticket_id = t.id
WHERE  oi.order_id = $1
ORDER  BY oi.ordinal ASC;

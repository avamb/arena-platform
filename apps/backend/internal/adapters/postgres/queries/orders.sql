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
-- filtered by status and fuzzy-matched against buyer_name / buyer_email /
-- buyer_phone via pg_trgm similarity (empty search = no filtering; the
-- orders_buyer_*_trgm gin indexes back this predicate). Pass an empty
-- string for statusFilter to skip the status filter.
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  org_id = $1
  AND  ($2 = '' OR status = $2)
  AND  ($3 = '' OR buyer_name  % $3 OR buyer_email % $3 OR buyer_phone % $3)
ORDER  BY created_at DESC, id DESC
LIMIT  $4 OFFSET $5;

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

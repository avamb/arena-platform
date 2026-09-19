-- superadmin.sql — sqlc source queries for the platform superadmin console (feature #166).
--
-- Cross-tenant read-only queries gated to the platform_superadmin role.
-- All queries support optional org_id and status/state filters and mandatory
-- limit/offset pagination.
--
-- This file is the sqlc input; the generated output lives in
-- ../gen/superadmin.sql.go. Regenerate with: make sqlc-generate.

-- name: ListAllCheckoutSessions :many
-- Returns checkout sessions across all organizations.
-- Pass NULL for orgID to return sessions from all orgs.
-- Pass NULL for stateFilter to return sessions in any state.
SELECT id, org_id, channel_id, reservation_id, user_id, state,
       subtotal, discount, platform_fee, provider_fee, tax, total, currency,
       promo_code_id, payment_intent_id, payment_provider,
       completed_at, abandoned_at, expired_at, created_at, updated_at,
       checkout_token, buyer_locale
FROM   checkout_sessions
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR state  = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4;

-- name: ListAllTickets :many
-- Returns tickets across all organizations (via the owning checkout session).
-- Pass NULL for orgID to return tickets from all orgs.
-- Pass NULL for statusFilter to return tickets in any status.
SELECT t.id, t.checkout_session_id, t.session_id, t.tier_id,
       t.holder_email, t.status, t.issued_at, t.created_at, t.updated_at,
       t.seat_key, t.seat_sector, t.seat_row, t.seat_number, t.ordinal,
       t.cancelled_at, t.cancellation_reason, t.refund_mode, t.refund_id,
       t.refund_date, t.refund_price, t.review_hold, t.review_hold_reason
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
WHERE  ($1::uuid IS NULL OR cs.org_id = $1)
  AND  ($2::text  IS NULL OR t.status = $2)
ORDER BY t.issued_at DESC, t.id DESC
LIMIT  $3 OFFSET $4;

-- name: ListAllOrders :many
-- Returns orders across all organizations (W1-A6d, feature #489, spec §14.2:
-- GET /v1/admin/orders now reads the `orders` aggregate table instead of
-- checkout_sessions). Pass NULL for orgID to return orders from all orgs.
-- Pass NULL for stateFilter to return orders in any status.
-- Pass NULL for search to skip it; otherwise it matches the order number
-- (system_id) or the site's reference exactly, or a substring of the
-- buyer's email, name or phone, case-insensitively. Each row carries its
-- organization and event names so support never has to resolve UUIDs.
SELECT o.id, o.system_id, o.org_id, o.channel_id, o.event_id, o.session_id, o.customer_id,
       o.checkout_session_id, o.reservation_id, o.external_ref, o.source, o.status,
       o.currency, o.subtotal, o.discount, o.charge, o.total, o.charge_percent_bp,
       o.promo_code_id, o.buyer_name, o.buyer_email, o.buyer_phone, o.payment_method,
       o.paid_at, o.cancelled_at, o.expires_at, o.metadata, o.created_at, o.updated_at,
       COALESCE(org.name, '') AS org_name, COALESCE(ev.name, '') AS event_name
FROM   orders o
LEFT JOIN organizations org ON org.id = o.org_id
LEFT JOIN events        ev  ON ev.id  = o.event_id
WHERE  ($1::uuid IS NULL OR o.org_id = $1)
  AND  ($2::text  IS NULL OR o.status = $2)
  AND  ($3::text  IS NULL
        OR o.system_id::text = $3
        OR o.external_ref = $3
        OR strpos(lower(COALESCE(o.buyer_email, '')), lower($3)) > 0
        OR strpos(lower(COALESCE(o.buyer_name,  '')), lower($3)) > 0
        OR strpos(COALESCE(o.buyer_phone, ''), $3) > 0)
ORDER BY o.created_at DESC, o.id DESC
LIMIT  $4 OFFSET $5;

-- name: ListAllRefunds :many
-- Returns refunds across all organizations.
-- Pass NULL for orgID to return refunds from all orgs.
-- Pass NULL for stateFilter to return refunds in any state.
SELECT id, payment_intent_id, org_id, amount, currency, reason, requested_by,
       state, provider_refund_id, failure_reason,
       requested_at, approved_at, succeeded_at, failed_at, created_at, updated_at, settlement, order_id, ticket_id
FROM   refunds
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR state  = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4;

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
       completed_at, abandoned_at, expired_at, created_at, updated_at
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
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR status = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4;

-- name: ListAllRefunds :many
-- Returns refunds across all organizations.
-- Pass NULL for orgID to return refunds from all orgs.
-- Pass NULL for stateFilter to return refunds in any state.
SELECT id, payment_intent_id, org_id, amount, currency, reason, requested_by,
       state, provider_refund_id, failure_reason,
       requested_at, approved_at, succeeded_at, failed_at, created_at, updated_at
FROM   refunds
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR state  = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4;

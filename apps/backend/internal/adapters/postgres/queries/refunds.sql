-- refunds.sql — sqlc source queries for the refund state machine (feature #138).
--
-- This file is the sqlc input; the generated output lives in
-- ../gen/refunds.sql.go. Regenerate with: make sqlc-generate.

-- name: InsertRefund :one
INSERT INTO refunds (
    payment_intent_id, org_id, amount, currency, reason, requested_by
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, payment_intent_id, org_id, amount, currency, reason, requested_by,
          state, provider_refund_id, failure_reason,
          requested_at, approved_at, succeeded_at, failed_at,
          created_at, updated_at;

-- name: GetRefundByID :one
SELECT id, payment_intent_id, org_id, amount, currency, reason, requested_by,
       state, provider_refund_id, failure_reason,
       requested_at, approved_at, succeeded_at, failed_at,
       created_at, updated_at
FROM   refunds
WHERE  id = $1;

-- name: ListRefundsByPaymentIntent :many
SELECT id, payment_intent_id, org_id, amount, currency, reason, requested_by,
       state, provider_refund_id, failure_reason,
       requested_at, approved_at, succeeded_at, failed_at,
       created_at, updated_at
FROM   refunds
WHERE  payment_intent_id = $1
ORDER BY created_at DESC, id DESC;

-- name: UpdateRefundState :one
UPDATE refunds
SET    state              = $2,
       updated_at         = now(),
       provider_refund_id = COALESCE($3, provider_refund_id),
       failure_reason     = COALESCE($4, failure_reason),
       approved_at        = CASE WHEN $2 = 'approved'  THEN now() ELSE approved_at  END,
       succeeded_at       = CASE WHEN $2 = 'succeeded' THEN now() ELSE succeeded_at END,
       failed_at          = CASE WHEN $2 IN ('failed', 'rejected') THEN now() ELSE failed_at END
WHERE  id = $1
RETURNING id, payment_intent_id, org_id, amount, currency, reason, requested_by,
          state, provider_refund_id, failure_reason,
          requested_at, approved_at, succeeded_at, failed_at,
          created_at, updated_at;

-- name: InsertRefundEvent :one
INSERT INTO refund_events (
    refund_id, provider_refund_id, event_type, event_payload, resulting_state
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (provider_refund_id, event_type) DO NOTHING
RETURNING id, refund_id, provider_refund_id, event_type, event_payload,
          resulting_state, processed_at;

-- name: GetRefundEvent :one
SELECT id, refund_id, provider_refund_id, event_type, event_payload,
       resulting_state, processed_at
FROM   refund_events
WHERE  provider_refund_id = $1
  AND  event_type         = $2;

-- name: CancelTicketsByCheckoutSession :exec
UPDATE tickets
SET    status     = 'cancelled',
       updated_at = now()
WHERE  checkout_session_id = $1
  AND  status = 'active';

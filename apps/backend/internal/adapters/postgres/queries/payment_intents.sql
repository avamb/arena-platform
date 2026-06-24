-- payment_intents.sql — typed sqlc queries for the payment_intents state machine
-- (feature #137).

-- name: InsertPaymentIntent :one
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
    state, sca_redirect_url, client_secret
)
VALUES (
    $1, $2, $3, $4, $5, $6,
    COALESCE(NULLIF($7, ''), 'created'), $8, $9
)
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, created_at, updated_at;

-- name: GetPaymentIntentByID :one
SELECT id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
       state, sca_redirect_url, client_secret, failure_code, failure_message,
       authorized_at, succeeded_at, failed_at, created_at, updated_at
FROM   payment_intents
WHERE  id = $1;

-- name: GetPaymentIntentByProviderID :one
SELECT id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
       state, sca_redirect_url, client_secret, failure_code, failure_message,
       authorized_at, succeeded_at, failed_at, created_at, updated_at
FROM   payment_intents
WHERE  provider_payment_id = $1;

-- name: ListPaymentIntentsByCheckout :many
SELECT id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
       state, sca_redirect_url, client_secret, failure_code, failure_message,
       authorized_at, succeeded_at, failed_at, created_at, updated_at
FROM   payment_intents
WHERE  checkout_session_id = $1
ORDER BY created_at DESC, id DESC;

-- name: UpdatePaymentIntentState :one
-- Advance a payment intent to a new state.
-- Optional SCA, error, and provider_payment_id fields are set only when the
-- corresponding parameter is non-NULL (CASE guards preserve the existing value
-- when the parameter is NULL).
UPDATE payment_intents
SET    state               = $2,
       updated_at          = now(),
       -- SCA fields — set when transitioning to requires_action.
       sca_redirect_url    = COALESCE($3, sca_redirect_url),
       client_secret       = COALESCE($4, client_secret),
       -- Terminal-state timestamps.
       authorized_at       = CASE WHEN $2 = 'authorized' THEN now() ELSE authorized_at END,
       succeeded_at        = CASE WHEN $2 = 'succeeded'  THEN now() ELSE succeeded_at  END,
       failed_at           = CASE WHEN $2 = 'failed'     THEN now() ELSE failed_at     END,
       -- Error details — written only when transitioning to failed.
       failure_code        = CASE WHEN $2 = 'failed' THEN COALESCE($5, failure_code)    ELSE failure_code    END,
       failure_message     = CASE WHEN $2 = 'failed' THEN COALESCE($6, failure_message) ELSE failure_message END,
       -- provider_payment_id — set on first update if not already populated.
       provider_payment_id = COALESCE(provider_payment_id, $7)
WHERE  id = $1
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, created_at, updated_at;

-- name: InsertPaymentIntentEvent :one
-- Record a processed provider webhook event for idempotency.
-- ON CONFLICT DO NOTHING means if the same (provider_payment_id, event_type)
-- has been processed before, no row is inserted (returns pgx.ErrNoRows to caller).
INSERT INTO payment_intent_events (
    payment_intent_id, provider_payment_id, event_type, event_payload, resulting_state
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (provider_payment_id, event_type) DO NOTHING
RETURNING id, payment_intent_id, provider_payment_id, event_type, event_payload,
          resulting_state, processed_at;

-- name: GetPaymentIntentEvent :one
SELECT id, payment_intent_id, provider_payment_id, event_type, event_payload,
       resulting_state, processed_at
FROM   payment_intent_events
WHERE  provider_payment_id = $1
  AND  event_type          = $2;

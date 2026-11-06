-- П-15 (spec 62 §5.4 п. 4-5, migration 0205): late payments and the
-- reconciliation of unfinished hosted payments. Hand-maintained wrapper:
-- gen/late_payments.sql.go.

-- name: ReopenExpiredOrderForLatePayment :execrows
UPDATE orders
   SET status = 'pending_payment', cancelled_at = NULL, updated_at = now()
 WHERE id = $1
   AND status = 'expired';

-- name: LatestLatePaymentOutcome :one
SELECT outcome FROM late_payments
 WHERE checkout_session_id = $1
 ORDER BY created_at DESC
 LIMIT 1;

-- name: GetLatePaymentLetter :one
SELECT lp.payment_intent_id, lp.org_id, lp.order_id, lp.late_case, lp.outcome,
       lp.amount, lp.currency, lp.buyer_notified_at,
       o.buyer_email, o.buyer_name, o.system_id, o.session_id,
       cs.buyer_locale, COALESCE(org.default_locale, '')
  FROM late_payments lp
  JOIN checkout_sessions cs ON cs.id = lp.checkout_session_id
  JOIN organizations org    ON org.id = lp.org_id
  LEFT JOIN orders o        ON o.id = lp.order_id
 WHERE lp.payment_intent_id = $1;

-- name: MarkLatePaymentNotified :execrows
UPDATE late_payments SET buyer_notified_at = now()
 WHERE payment_intent_id = $1 AND buyer_notified_at IS NULL;

-- name: UnmarkLatePaymentNotified :exec
UPDATE late_payments SET buyer_notified_at = NULL WHERE payment_intent_id = $1;

-- name: ListUnsettledHostedPayments :many
SELECT pi.id, pi.org_id, pi.provider, pi.provider_payment_id, cs.id
  FROM payment_intents pi
  JOIN checkout_sessions cs ON cs.id = pi.checkout_session_id
  JOIN reservations r      ON r.id = cs.reservation_id
 WHERE pi.provider = $1
   AND pi.state IN ('created', 'requires_action', 'processing', 'authorized')
   AND pi.hosted_checkout_url IS NOT NULL
   AND pi.provider_payment_id IS NOT NULL
   AND pi.created_at < $2
   AND (pi.provider_checked_at IS NULL OR pi.provider_checked_at < $3)
   AND cs.state IN ('pricing_confirmed', 'payment_started', 'expired', 'abandoned')
   AND r.expires_at > $4
 ORDER BY pi.provider_checked_at NULLS FIRST, pi.created_at
 LIMIT $5;

-- name: MarkPaymentIntentProviderChecked :exec
UPDATE payment_intents SET provider_checked_at = now() WHERE id = $1;

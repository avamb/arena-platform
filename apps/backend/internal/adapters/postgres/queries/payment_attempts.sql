-- П-19: the payment attempt of a hosted checkout recorded BEFORE the
-- provider call, and the read-back of attempts whose answer was lost.

-- name: InsertHostedPaymentAttempt :one
-- A hosted payment whose provider id is arena's own reference (AllPay
-- order_id = checkout session id), written before the provider is called.
-- hosted_checkout_url stays NULL until the provider answers.
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state
)
VALUES ($1, $2, $3, $4, $5, $6, 'created')
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, hosted_checkout_url, provider_charge_ref,
          created_at, updated_at;

-- name: SetPaymentIntentHostedURL :execrows
-- Fills the payment page of a recorded attempt, once.
UPDATE payment_intents
   SET hosted_checkout_url = $2, updated_at = now()
 WHERE id = $1
   AND hosted_checkout_url IS NULL
   AND state = 'created';

-- name: ListStalledHostedPaymentAttempts :many
-- Recorded attempts of one provider still without a payment page, older than
-- $2, whose checkout, order and hold are all still open.
SELECT pi.id, pi.org_id, pi.provider, pi.provider_payment_id, pi.amount, pi.currency,
       cs.id, cs.channel_id, cs.checkout_token, cs.buyer_locale, cs.return_url,
       o.id, o.buyer_email, o.buyer_name, o.buyer_phone, r.expires_at, e.name
  FROM payment_intents pi
  JOIN checkout_sessions cs ON cs.id = pi.checkout_session_id
  JOIN reservations r      ON r.id = cs.reservation_id
  JOIN orders o            ON o.checkout_session_id = cs.id
  JOIN events e            ON e.id = o.event_id
 WHERE pi.provider = $1
   AND pi.state = 'created'
   AND pi.hosted_checkout_url IS NULL
   AND pi.provider_payment_id IS NOT NULL
   AND pi.created_at < $2
   AND cs.state IN ('pricing_confirmed', 'payment_started')
   AND o.status = 'pending_payment'
   AND r.state IN ('draft', 'active')
   AND r.expires_at > now()
 ORDER BY pi.created_at
 LIMIT $3;

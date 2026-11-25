-- payment_embedded.sql — the embedded checkout (spec 65 §3) and its hosted
-- fallback. No migration: an embedded payment is a payment_intents row like a
-- hosted one, keyed by the cs_… session id, with no hosted page URL.

-- name: InsertEmbeddedPaymentIntent :one
-- Create the payment intent of an embedded checkout session. provider_payment_id
-- is the session id (Stripe cs_…); hosted_checkout_url stays NULL because the
-- buyer pays on arena's own page. The client secret is NEVER stored.
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state
)
VALUES ($1, $2, $3, $4, $5, $6, 'created')
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, hosted_checkout_url, provider_charge_ref,
          created_at, updated_at;

-- name: MarkPaymentIntentSuperseded :execrows
-- Mark an open payment intent as replaced by a newer one on the same checkout
-- (the hosted fallback of an embedded session). The STATE is left alone on
-- purpose: a superseded session that still gets paid must pay the order, so
-- the row must stay non-terminal and findable by its cs_… id.
UPDATE payment_intents
   SET failure_code = 'superseded',
       failure_message = 'replaced by a hosted payment page on the same hold',
       updated_at = now()
 WHERE id = $1
   AND state NOT IN ('succeeded', 'failed');

-- name: GetCheckoutPaymentContext :one
-- What a new payment page of an existing checkout needs: the checkout, its
-- order and buyer, the hold's expiry and the event name.
-- orders.checkout_session_id is not unique, so a checkout that ever carried
-- more than one order deterministically answers its NEWEST one.
SELECT cs.id, cs.org_id, cs.channel_id, cs.checkout_token, cs.state, cs.total, cs.currency,
       cs.buyer_locale, cs.return_url,
       o.id, o.status, o.buyer_email, o.buyer_name, o.buyer_phone,
       r.state, r.expires_at, e.name
  FROM checkout_sessions cs
  JOIN reservations r ON r.id = cs.reservation_id
  JOIN orders o       ON o.checkout_session_id = cs.id
  JOIN events e       ON e.id = o.event_id
 WHERE cs.id = $1
 ORDER BY o.created_at DESC, o.id DESC
 LIMIT 1;

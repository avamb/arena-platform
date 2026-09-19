-- +goose Up
-- =====================================================================
-- arena_new — Stripe-hosted Checkout Session support on payment_intents
--
-- The widget now takes payment through a Stripe-hosted Checkout Session
-- (redirect flow, no card UI in the widget). Two facts about such a
-- payment have no column yet:
--
--   hosted_checkout_url  the buyer-facing Stripe page (cs_… session's
--                        `url`). Stored so GET /v1/public/checkout/{token}
--                        can offer "continue to payment" while the order
--                        is still pending and the window is open, instead
--                        of forcing the buyer to start a new cart.
--   provider_charge_ref  the pi_… PaymentIntent id Stripe creates behind
--                        the Checkout Session. It is NOT known at creation
--                        time (checkout.session `payment_intent` is null
--                        until the buyer pays) and is learned from the
--                        checkout.session.completed webhook. Refunds go
--                        through the pi_ id, not the cs_ id, so it must be
--                        persisted the moment we see it.
--
-- payment_intents.provider_payment_id stays the cs_… Checkout Session id
-- for this flow: it is what every checkout.session.* webhook event
-- identifies the payment by, and it is unique per checkout.
-- =====================================================================

ALTER TABLE payment_intents
    ADD COLUMN hosted_checkout_url text,
    ADD COLUMN provider_charge_ref text;

COMMENT ON COLUMN payment_intents.hosted_checkout_url IS
    'Buyer-facing hosted payment page URL (Stripe Checkout Session url). NULL for non-hosted flows.';
COMMENT ON COLUMN payment_intents.provider_charge_ref IS
    'Provider charge/payment identifier behind a hosted checkout session (Stripe pi_…), learned from the webhook.';

-- +goose Down
ALTER TABLE payment_intents
    DROP COLUMN IF EXISTS provider_charge_ref,
    DROP COLUMN IF EXISTS hosted_checkout_url;

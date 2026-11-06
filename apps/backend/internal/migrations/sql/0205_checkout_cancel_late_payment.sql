-- 0205_checkout_cancel_late_payment.sql — the buyer's own side of an unpaid
-- order (spec 62 §5.4, П-2, П-3, П-15, П-20).
--
-- sales_channels.payment_window_seconds
--     The channel's own payment window (П-3): how long the buyer has on the
--     provider's page. NULL means the deployment-wide
--     WIDGET_PAYMENT_WINDOW_SECONDS. A column, not a settings key, so a PATCH
--     of the free-form settings can never wipe it (the reservation TTL lesson).
--     It shortens the window only for a provider without a floor of its own:
--     Stripe refuses a Checkout Session that expires sooner than 30 minutes,
--     so for Stripe the global window stays the floor.
--
-- checkout_sessions.tracking_consent_*
--     The cookie (tracking) consent of an unfinished order (§8.1, П-20): the
--     buyer's choice, the banner text version and when the buyer made it, plus
--     when arena received it. NULL granted means "not stated", which a
--     conversion sender reads as "no". The conversion job reads it from the
--     order at send time.
--
-- payment_intents.provider_checked_at
--     When the reconciliation job last asked the provider about an unfinished
--     hosted payment (П-15 §5.4 п. 5), so one payment is asked at most every
--     five minutes.
--
-- late_payments
--     One row per payment that arrived after its order stopped waiting for it
--     (П-15 §5.4 п. 4): which of the three cases it was and what arena did.
--     The row is also the idempotency key of the buyer's letter.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_channels
    ADD COLUMN payment_window_seconds integer,
    ADD CONSTRAINT sales_channels_payment_window_seconds_check
        CHECK (payment_window_seconds IS NULL OR payment_window_seconds BETWEEN 120 AND 86400);

COMMENT ON COLUMN sales_channels.payment_window_seconds IS
    'Payment window of the channel in seconds (spec 62 П-3); NULL = WIDGET_PAYMENT_WINDOW_SECONDS. Ignored below the provider''s own floor (Stripe: 30 min).';

ALTER TABLE checkout_sessions
    ADD COLUMN tracking_consent_granted     boolean,
    ADD COLUMN tracking_consent_version     text,
    ADD COLUMN tracking_consent_at          timestamptz,
    ADD COLUMN tracking_consent_categories  jsonb,
    ADD COLUMN tracking_consent_received_at timestamptz,
    ADD CONSTRAINT checkout_sessions_tracking_consent_version_check
        CHECK (tracking_consent_version IS NULL OR length(btrim(tracking_consent_version)) BETWEEN 1 AND 64),
    ADD CONSTRAINT checkout_sessions_tracking_consent_categories_check
        CHECK (tracking_consent_categories IS NULL OR jsonb_typeof(tracking_consent_categories) = 'object');

COMMENT ON COLUMN checkout_sessions.tracking_consent_granted IS
    'Cookie (tracking) consent of the buyer (spec 62 §8.1); NULL = not stated, read as no.';

ALTER TABLE payment_intents
    ADD COLUMN provider_checked_at timestamptz;

CREATE TABLE late_payments (
    payment_intent_id   uuid        PRIMARY KEY REFERENCES payment_intents(id),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    checkout_session_id uuid        NOT NULL REFERENCES checkout_sessions(id),
    order_id            uuid        REFERENCES orders(id),
    -- buyer_cancelled: the buyer said no first; delayed_notice: paid in time,
    -- the notification was late; after_window: paid after the window closed.
    late_case           text        NOT NULL,
    -- reacquired: same places taken again and the order paid;
    -- refund_requested: a full refund was sent to the provider;
    -- operator_review: the provider cannot refund through arena, a person must.
    outcome             text        NOT NULL,
    reason              text        NOT NULL DEFAULT '',
    refund_id           uuid        REFERENCES refunds(id),
    amount              bigint      NOT NULL,
    currency            text        NOT NULL,
    buyer_notified_at   timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT late_payments_case_check
        CHECK (late_case IN ('buyer_cancelled', 'delayed_notice', 'after_window')),
    CONSTRAINT late_payments_outcome_check
        CHECK (outcome IN ('reacquired', 'refund_requested', 'operator_review'))
);

CREATE INDEX late_payments_org_created_idx ON late_payments (org_id, created_at DESC);
CREATE INDEX late_payments_checkout_idx ON late_payments (checkout_session_id);

COMMENT ON TABLE late_payments IS
    'Payments that arrived after their order stopped waiting (spec 62 П-15): the case and what arena did about it.';

ALTER TABLE email_letters
    DROP CONSTRAINT email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded', 'shop_delivered',
                        'checkout_reminder', 'referral_cashback', 'referral_unavailable',
                        'late_payment'));

-- +goose Down
SET LOCAL lock_timeout = '5s';

DELETE FROM email_letters WHERE kind = 'late_payment';
ALTER TABLE email_letters
    DROP CONSTRAINT IF EXISTS email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded', 'shop_delivered',
                        'checkout_reminder', 'referral_cashback', 'referral_unavailable'));

DROP TABLE IF EXISTS late_payments;

ALTER TABLE payment_intents DROP COLUMN IF EXISTS provider_checked_at;

ALTER TABLE checkout_sessions
    DROP CONSTRAINT IF EXISTS checkout_sessions_tracking_consent_categories_check,
    DROP CONSTRAINT IF EXISTS checkout_sessions_tracking_consent_version_check,
    DROP COLUMN IF EXISTS tracking_consent_received_at,
    DROP COLUMN IF EXISTS tracking_consent_categories,
    DROP COLUMN IF EXISTS tracking_consent_at,
    DROP COLUMN IF EXISTS tracking_consent_version,
    DROP COLUMN IF EXISTS tracking_consent_granted;

ALTER TABLE sales_channels
    DROP CONSTRAINT IF EXISTS sales_channels_payment_window_seconds_check,
    DROP COLUMN IF EXISTS payment_window_seconds;

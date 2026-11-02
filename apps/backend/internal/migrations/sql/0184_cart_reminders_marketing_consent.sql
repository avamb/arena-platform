-- 0184_cart_reminders_marketing_consent.sql — the reminder about an unpaid
-- order and the discount for a marketing consent (spec 51-1, owner decisions
-- 2026-11-01/02).
--
-- org_growth_settings: per organization, whether arena reminds a buyer of an
-- unpaid widget order (default on), whether the checkout offers the marketing
-- consent tick (default off) and which of the organization's own promo codes
-- the tick earns. No row means the defaults.
--
-- checkout_sessions.return_url: the buyer's page the checkout started on, so
-- the reminder's link can bring the buyer back after the payment page died.
--
-- checkout_reminders: one row per order that was chosen for a reminder. The
-- primary key on order_id is what makes "never a second reminder" hold.
--
-- marketing_subscriptions: the current consent of one address to one
-- organization (pending until the address confirms, confirmed, withdrawn).
-- marketing_consents: the journal of every grant, confirmation and
-- withdrawal (GDPR art. 7(1) evidence), never updated or deleted by code.
-- The client address is kept open, like checkout_consents (owner decision
-- 2026-11-02); an erasure clears it.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE checkout_sessions
    ADD COLUMN return_url text CHECK (return_url IS NULL OR length(return_url) <= 2000);

COMMENT ON COLUMN checkout_sessions.return_url IS
    'The buyer page (origin + path, already checked by ReturnURLPolicy) the checkout started on; the cart reminder links back to it.';

CREATE TABLE org_growth_settings (
    org_id                  uuid        PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    cart_reminder_enabled   boolean     NOT NULL DEFAULT true,
    marketing_optin_enabled boolean     NOT NULL DEFAULT false,
    consent_promo_code_id   uuid        REFERENCES promo_codes(id) ON DELETE SET NULL,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    updated_by              uuid
);

COMMENT ON TABLE org_growth_settings IS
    'Spec 51-1: cart reminder switch, marketing consent tick and the promo code the tick earns. No row = defaults (reminder on, tick off).';

CREATE TABLE checkout_reminders (
    order_id            uuid        PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    org_id              uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    checkout_session_id uuid        NOT NULL,
    queued_at           timestamptz NOT NULL DEFAULT now(),
    sent_at             timestamptz,
    skipped_reason      text,
    CONSTRAINT checkout_reminders_outcome_check CHECK (sent_at IS NULL OR skipped_reason IS NULL)
);

COMMENT ON TABLE checkout_reminders IS
    'Spec 51-1: an unpaid widget order chosen for its one reminder. The primary key makes a second reminder impossible.';

-- The sweep looks for unpaid widget orders of the last hour.
CREATE INDEX orders_pending_public_created_idx
    ON orders (created_at)
    WHERE status = 'pending_payment' AND source = 'public_feed';

CREATE TABLE marketing_subscriptions (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email              text        NOT NULL,
    email_normalized   text        NOT NULL CHECK (length(email_normalized) BETWEEN 3 AND 254),
    buyer_name         text,
    locale             text        NOT NULL DEFAULT '',
    status             text        NOT NULL CHECK (status IN ('pending', 'confirmed', 'withdrawn')),
    text_version       text        NOT NULL,
    source             text        NOT NULL,
    page_url           text        NOT NULL DEFAULT '',
    granted_at         timestamptz NOT NULL,
    confirmed_at       timestamptz,
    withdrawn_at       timestamptz,
    confirm_token_hash text,
    confirm_sent_at    timestamptz,
    unsubscribe_token  text        NOT NULL UNIQUE,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT marketing_subscriptions_org_email_uq UNIQUE (org_id, email_normalized)
);

CREATE INDEX marketing_subscriptions_confirm_idx
    ON marketing_subscriptions (confirm_token_hash) WHERE confirm_token_hash IS NOT NULL;
CREATE INDEX marketing_subscriptions_org_status_idx
    ON marketing_subscriptions (org_id, status, granted_at DESC);

COMMENT ON TABLE marketing_subscriptions IS
    'Spec 51-1: the current marketing consent of one address to one organization. unsubscribe_token is a withdraw-only capability, kept in clear so the organizer export can print the link.';

CREATE TABLE marketing_consents (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    subscription_id     uuid        NOT NULL REFERENCES marketing_subscriptions(id) ON DELETE CASCADE,
    org_id              uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    action              text        NOT NULL CHECK (action IN ('granted', 'confirmed', 'withdrawn')),
    text_version        text        NOT NULL,
    locale              text        NOT NULL DEFAULT '',
    source              text        NOT NULL CHECK (source IN
                            ('widget', 'confirm_link', 'withdraw_link', 'organizer', 'erasure')),
    checkout_session_id uuid,
    client_ip           text,
    user_agent          text CHECK (user_agent IS NULL OR length(user_agent) <= 512),
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX marketing_consents_subscription_idx ON marketing_consents (subscription_id, created_at);

COMMENT ON TABLE marketing_consents IS
    'Spec 51-1: journal of marketing consent grants, confirmations and withdrawals (evidence). Never updated or deleted by code; erasure clears client_ip and user_agent.';

ALTER TABLE email_letters
    DROP CONSTRAINT email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded', 'checkout_reminder'));

-- +goose Down

DELETE FROM email_letters WHERE kind = 'checkout_reminder';

ALTER TABLE email_letters
    DROP CONSTRAINT IF EXISTS email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded'));

DROP TABLE IF EXISTS marketing_consents;
DROP TABLE IF EXISTS marketing_subscriptions;
DROP INDEX IF EXISTS orders_pending_public_created_idx;
DROP TABLE IF EXISTS checkout_reminders;
DROP TABLE IF EXISTS org_growth_settings;
ALTER TABLE checkout_sessions DROP COLUMN IF EXISTS return_url;

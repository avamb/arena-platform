-- 0195_referral_program.sql — "Refer a friend" (spec 52, steps RF-1/RF-2/RF-5).
--
-- referral_programs   one programme per event (or per shop): the friend's
--                     discount and the referrer's cashback, in percent. The
--                     percentages of a programme apply to NEW codes; the
--                     reward row copies them (cashback_pct, cap_pct), so what
--                     was promised stays what is paid.
-- promo_codes         a personal code is a promo_codes row of origin
--                     'referral' (referral_program_id + the referrer's order
--                     or shop order, unique, so a code is issued once per
--                     order). Lists of ordinary promo codes show only
--                     origin = 'manual'.
-- referral_rewards    one row per friend order that used a personal code: the
--                     cashback owed to the referrer and where it stands
--                     (due, sent, paid, capped, not_possible, failed,
--                     cancelled). friend_reversal records that the friend
--                     later got the money back or opened a dispute (the
--                     cashback has already left, the organizer is told).
-- refunds.origin      gains 'referral_cashback': the cashback is a partial,
--                     money-only refund of the referrer's own payment, made
--                     through the refund engine. billing.collect skips it.
--
-- The shop columns (referrer_shop_order_id, friend_shop_order_id, scope
-- 'shop') exist so the later shop step needs no second migration; nothing
-- writes them yet. No permission is added.
--
-- Numbered 0195 provisionally: renumber just before the merge (AGENTS.md).

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE referral_programs (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    scope               text        NOT NULL CHECK (scope IN ('event', 'shop')),
    event_id            uuid,
    status              text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'ended')),
    friend_discount_pct smallint    NOT NULL CHECK (friend_discount_pct BETWEEN 1 AND 100),
    cashback_pct        smallint    NOT NULL CHECK (cashback_pct BETWEEN 1 AND 100),
    cashback_cap_pct    smallint    NOT NULL DEFAULT 100 CHECK (cashback_cap_pct BETWEEN 1 AND 100),
    max_friends         smallint    CHECK (max_friends IS NULL OR max_friends > 0),
    starts_at           timestamptz,
    ends_at             timestamptz,
    terms_accepted_at   timestamptz NOT NULL,
    created_by          text,
    created_via         text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT referral_programs_scope_event_check CHECK ((scope = 'event') = (event_id IS NOT NULL)),
    CONSTRAINT referral_programs_period_check CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at),
    CONSTRAINT referral_programs_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE
);

-- One live programme per event and one per organization's shop.
CREATE UNIQUE INDEX referral_programs_event_live_uq
    ON referral_programs (event_id) WHERE scope = 'event' AND status <> 'ended';
CREATE UNIQUE INDEX referral_programs_shop_live_uq
    ON referral_programs (org_id) WHERE scope = 'shop' AND status <> 'ended';
CREATE INDEX referral_programs_org_idx ON referral_programs (org_id);

ALTER TABLE promo_codes
    ADD COLUMN origin                  text NOT NULL DEFAULT 'manual',
    ADD COLUMN referral_program_id     uuid REFERENCES referral_programs(id),
    ADD COLUMN referrer_order_id       uuid REFERENCES orders(id),
    ADD COLUMN referrer_shop_order_id  uuid REFERENCES shop_orders(id),
    ADD CONSTRAINT promo_codes_origin_check CHECK (origin IN ('manual', 'referral')),
    ADD CONSTRAINT promo_codes_referral_shape_check CHECK (
        (origin = 'referral') = (referral_program_id IS NOT NULL)
        AND (referrer_order_id IS NULL OR referrer_shop_order_id IS NULL)
        AND (origin = 'referral' OR (referrer_order_id IS NULL AND referrer_shop_order_id IS NULL)));

-- A personal code is issued once per referrer order.
CREATE UNIQUE INDEX promo_codes_referrer_order_uq
    ON promo_codes (referrer_order_id) WHERE referrer_order_id IS NOT NULL;
CREATE UNIQUE INDEX promo_codes_referrer_shop_order_uq
    ON promo_codes (referrer_shop_order_id) WHERE referrer_shop_order_id IS NOT NULL;
CREATE INDEX promo_codes_referral_program_idx
    ON promo_codes (referral_program_id) WHERE referral_program_id IS NOT NULL;

CREATE TABLE referral_rewards (
    id                     uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id                 uuid        NOT NULL REFERENCES organizations(id),
    program_id             uuid        NOT NULL REFERENCES referral_programs(id),
    promo_code_id          uuid        NOT NULL REFERENCES promo_codes(id),
    referrer_order_id      uuid        REFERENCES orders(id),
    referrer_shop_order_id uuid        REFERENCES shop_orders(id),
    friend_order_id        uuid        REFERENCES orders(id),
    friend_shop_order_id   uuid        REFERENCES shop_orders(id),
    amount_minor           bigint      NOT NULL CHECK (amount_minor >= 0),
    currency               text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    cashback_pct           smallint    NOT NULL CHECK (cashback_pct BETWEEN 1 AND 100),
    cap_pct                smallint    NOT NULL CHECK (cap_pct BETWEEN 1 AND 100),
    state                  text        NOT NULL CHECK (state IN
        ('due', 'sent', 'paid', 'capped', 'not_possible', 'failed', 'cancelled')),
    reason                 text,
    friend_reversal        text        CHECK (friend_reversal IN ('refunded', 'partially_refunded', 'disputed')),
    reversal_notified_at   timestamptz,
    letter_sent_at         timestamptz,
    attempts               integer     NOT NULL DEFAULT 0,
    next_attempt_at        timestamptz,
    refund_id              uuid        REFERENCES refunds(id),
    refund_batch_id        uuid        REFERENCES refund_batches(id),
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT referral_rewards_one_friend_check CHECK ((friend_order_id IS NULL) <> (friend_shop_order_id IS NULL)),
    CONSTRAINT referral_rewards_one_referrer_check CHECK ((referrer_order_id IS NULL) <> (referrer_shop_order_id IS NULL)),
    CONSTRAINT referral_rewards_friend_order_uq UNIQUE (friend_order_id),
    CONSTRAINT referral_rewards_friend_shop_order_uq UNIQUE (friend_shop_order_id)
);

CREATE INDEX referral_rewards_org_state_idx ON referral_rewards (org_id, state, created_at DESC);
CREATE INDEX referral_rewards_program_idx ON referral_rewards (program_id);
CREATE INDEX referral_rewards_promo_idx ON referral_rewards (promo_code_id);
CREATE INDEX referral_rewards_referrer_order_idx ON referral_rewards (referrer_order_id) WHERE referrer_order_id IS NOT NULL;
CREATE INDEX referral_rewards_due_idx ON referral_rewards (created_at) WHERE state = 'due';
CREATE INDEX referral_rewards_sent_idx ON referral_rewards (updated_at) WHERE state = 'sent';

ALTER TABLE refunds DROP CONSTRAINT refunds_origin_check;
ALTER TABLE refunds ADD CONSTRAINT refunds_origin_check
    CHECK (origin IN ('arena', 'provider_dashboard', 'referral_cashback'));

COMMENT ON TABLE referral_programs IS
    'Refer a friend programme of an event or a shop (spec 52): friend discount and referrer cashback, in percent.';
COMMENT ON TABLE referral_rewards IS
    'Cashback owed to a referrer for one friend order (spec 52); paid by a money-only refund of origin referral_cashback.';

-- +goose Down

-- A cashback refund cannot exist under the old CHECK: it becomes an ordinary
-- arena refund row (the money has already moved at the provider).
UPDATE refunds SET origin = 'arena' WHERE origin = 'referral_cashback';
ALTER TABLE refunds DROP CONSTRAINT refunds_origin_check;
ALTER TABLE refunds ADD CONSTRAINT refunds_origin_check
    CHECK (origin IN ('arena', 'provider_dashboard'));

DROP TABLE IF EXISTS referral_rewards;

DROP INDEX IF EXISTS promo_codes_referral_program_idx;
DROP INDEX IF EXISTS promo_codes_referrer_shop_order_uq;
DROP INDEX IF EXISTS promo_codes_referrer_order_uq;
ALTER TABLE promo_codes
    DROP CONSTRAINT IF EXISTS promo_codes_referral_shape_check,
    DROP CONSTRAINT IF EXISTS promo_codes_origin_check,
    DROP COLUMN IF EXISTS referrer_shop_order_id,
    DROP COLUMN IF EXISTS referrer_order_id,
    DROP COLUMN IF EXISTS referral_program_id,
    DROP COLUMN IF EXISTS origin;

DROP TABLE IF EXISTS referral_programs;

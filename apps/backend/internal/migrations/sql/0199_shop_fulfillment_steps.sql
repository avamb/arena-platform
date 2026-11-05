-- 0199_shop_fulfillment_steps.sql — delivery steps of a shop order and the
-- delivery group's buttons (spec 08_architecture/61 sections 7a.4 and 7b,
-- stage M5, owner decisions V-14 to V-19).
--
-- shop_orders.fulfillment_step is the parcel's progress, apart from the money
-- status: waiting (paid, waits for packing) -> packed -> handed_over (the old
-- "shipped": shipped_at, tracking_note, the shipped letter) -> delivered, and
-- failed from any of the first three (then handed_over again for another
-- try). A digital or service order (delivery_kind none) has no steps: NULL.
-- An order that is not paid yet also has NULL; the payment writes waiting.
-- A refund does not erase the step and a step does not touch the money.
--
-- shop_order_events is the history of the steps, one row per step change,
-- never edited: who (a platform user, or a Telegram account of the delivery
-- group: id and name), from where (via) and when.
--
-- sales_notification_cards remembers the Telegram message of the delivery
-- card per (order, delivery chat), so the card can be edited in place when the
-- step changes anywhere (the group, the event center, the bot) and when the
-- order is refunded. sales_notification_deliveries (0111) cannot hold it: its
-- key is one announcement, while one card goes to several chats.
--
-- sales_bot_prompts is the short state of the sales bot (arena-bot): a
-- question the bot asked in a delivery group with force_reply (the tracking
-- number, the text of an "other" reason), keyed by the group and the
-- question's message id, so the reply that quotes it is matched after a bot
-- restart too. Rows live 30 minutes.
--
-- sales_chat_connect_codes: a platform superadmin creates a one-time code
-- (30 minutes, only its SHA-256 is stored) for a chat with a name and the
-- kinds it hears; the operator writes "/connect <code>" in the group and the
-- sales bot binds the chat (V-14). org_id NULL is an operator chat.
--
-- sales_notification_subscriptions gains:
--   deleted_at      a chat removed in the admin screen (soft: the delivery
--                   history stays), never sent to again;
--   bot_removed_at  Telegram said the bot was removed from the group
--                   (my_chat_member); a new /connect clears it;
--   chat_title      the group's title when it was connected;
--   connected_at    when the chat was last bound by a code.
-- allowed stays the operator's on/off switch and last_error the last send
-- error: neither can say "removed" without an operator's re-enable silently
-- writing to a chat the bot is no longer in.
--
-- email_letters.kind gains shop_delivered: the "your order was delivered"
-- letter (V-17), sent always, at most once per order.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_orders ADD COLUMN fulfillment_step text;

ALTER TABLE shop_orders
    ADD CONSTRAINT shop_orders_fulfillment_step_check
        CHECK (fulfillment_step IS NULL
               OR fulfillment_step IN ('waiting', 'packed', 'handed_over', 'delivered', 'failed')),
    ADD CONSTRAINT shop_orders_fulfillment_none_check
        CHECK (delivery_kind <> 'none' OR fulfillment_step IS NULL);

-- Existing orders: shipped -> handed_over, paid and not shipped -> waiting. An
-- order parked in manual_review waits for a decision, not for packing: it gets
-- waiting when the decision pays it.
UPDATE shop_orders
SET    fulfillment_step = 'handed_over'
WHERE  shipped_at IS NOT NULL AND delivery_kind <> 'none';

UPDATE shop_orders
SET    fulfillment_step = 'waiting'
WHERE  shipped_at IS NULL AND paid_at IS NOT NULL AND delivery_kind <> 'none'
  AND  status <> 'manual_review';

CREATE INDEX shop_orders_fulfillment_idx
    ON shop_orders (org_id, fulfillment_step)
    WHERE fulfillment_step IS NOT NULL;

COMMENT ON COLUMN shop_orders.fulfillment_step IS
    'Delivery progress apart from the money status: waiting, packed, handed_over (shipped), delivered, failed; NULL for an unpaid order or one with nothing to ship (spec 61 section 7b.1).';

CREATE TABLE shop_order_events (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id            uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    order_id          uuid        NOT NULL REFERENCES shop_orders(id) ON DELETE CASCADE,
    step              text        NOT NULL
        CHECK (step IN ('waiting', 'packed', 'handed_over', 'delivered', 'failed')),
    from_step         text,
    tracking          text        NOT NULL DEFAULT '' CHECK (length(tracking) <= 2000),
    reason            text        NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    note              text        NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    actor_user_id     uuid        REFERENCES users(id) ON DELETE SET NULL,
    actor_telegram_id bigint,
    actor_name        text        NOT NULL DEFAULT '' CHECK (length(actor_name) <= 200),
    via               text        NOT NULL
        CHECK (via IN ('delivery_group', 'telegram_bot', 'admin_web', 'event_center_web', 'api', 'system')),
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX shop_order_events_order_idx ON shop_order_events (order_id, created_at);

COMMENT ON TABLE shop_order_events IS
    'History of the delivery steps of a shop order (spec 61 section 7b.1): one row per step change, never edited.';

CREATE TABLE sales_notification_cards (
    order_id        uuid        NOT NULL REFERENCES shop_orders(id) ON DELETE CASCADE,
    subscription_id uuid        NOT NULL REFERENCES sales_notification_subscriptions(id) ON DELETE CASCADE,
    chat_id         text        NOT NULL,
    message_id      bigint      NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, subscription_id)
);

CREATE INDEX sales_notification_cards_message_idx ON sales_notification_cards (chat_id, message_id);

COMMENT ON TABLE sales_notification_cards IS
    'The Telegram message of a shop order card in each delivery chat, edited in place when the step changes or the order is refunded.';

CREATE TABLE sales_bot_prompts (
    chat_id         text        NOT NULL,
    message_id      bigint      NOT NULL,
    card_message_id bigint      NOT NULL,
    order_id        uuid        NOT NULL REFERENCES shop_orders(id) ON DELETE CASCADE,
    step            text        NOT NULL CHECK (step IN ('handed_over', 'failed')),
    reason          text        NOT NULL DEFAULT '',
    locale          text        NOT NULL DEFAULT 'en',
    asked_by        bigint,
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, message_id)
);

CREATE INDEX sales_bot_prompts_order_idx ON sales_bot_prompts (chat_id, order_id);

COMMENT ON TABLE sales_bot_prompts IS
    'A question of the sales bot in a delivery group waiting for a reply (tracking number, other reason); 30 minutes.';

ALTER TABLE sales_notification_subscriptions
    ADD COLUMN deleted_at     timestamptz,
    ADD COLUMN bot_removed_at timestamptz,
    ADD COLUMN chat_title     text,
    ADD COLUMN connected_at   timestamptz;

COMMENT ON COLUMN sales_notification_subscriptions.deleted_at IS
    'Removed in the admin screen: never sent to again, the delivery history stays.';
COMMENT ON COLUMN sales_notification_subscriptions.bot_removed_at IS
    'Telegram reported the bot removed from the chat (my_chat_member); a new /connect clears it.';

CREATE TABLE sales_chat_connect_codes (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    code_hash       text        NOT NULL UNIQUE,
    org_id          uuid        REFERENCES organizations(id) ON DELETE CASCADE,
    name            text        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    kinds           text[]      NOT NULL
        CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop_sale', 'shop_delivery']::text[]),
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz,
    chat_id         text,
    subscription_id uuid        REFERENCES sales_notification_subscriptions(id) ON DELETE SET NULL,
    created_by      uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE sales_chat_connect_codes IS
    'One-time codes that bind a Telegram chat to the sales bot ("/connect <code>"), 30 minutes; only the SHA-256 of the code is stored.';

ALTER TABLE email_letters
    DROP CONSTRAINT email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded', 'shop_delivered',
                        'checkout_reminder', 'referral_cashback', 'referral_unavailable'));

-- +goose Down

SET LOCAL lock_timeout = '5s';

DELETE FROM email_letters WHERE kind = 'shop_delivered';
ALTER TABLE email_letters
    DROP CONSTRAINT IF EXISTS email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded', 'checkout_reminder',
                        'referral_cashback', 'referral_unavailable'));

DROP TABLE IF EXISTS sales_chat_connect_codes;

-- A chat removed in the admin screen or by Telegram had stopped hearing
-- anything; the old schema has no such state, so it is switched off.
UPDATE sales_notification_subscriptions
SET    allowed = false
WHERE  deleted_at IS NOT NULL OR bot_removed_at IS NOT NULL;

ALTER TABLE sales_notification_subscriptions
    DROP COLUMN IF EXISTS connected_at,
    DROP COLUMN IF EXISTS chat_title,
    DROP COLUMN IF EXISTS bot_removed_at,
    DROP COLUMN IF EXISTS deleted_at;

DROP TABLE IF EXISTS sales_bot_prompts;
DROP TABLE IF EXISTS sales_notification_cards;
DROP TABLE IF EXISTS shop_order_events;

DROP INDEX IF EXISTS shop_orders_fulfillment_idx;
ALTER TABLE shop_orders
    DROP CONSTRAINT IF EXISTS shop_orders_fulfillment_none_check,
    DROP CONSTRAINT IF EXISTS shop_orders_fulfillment_step_check;
ALTER TABLE shop_orders DROP COLUMN IF EXISTS fulfillment_step;

-- 0111_sales_notification_subscriptions.sql — per-organization Telegram
-- sales notifications.
--
-- Organizers used to get their own Telegram notifications from Bil24 (a
-- notification per organization: chat id + triggers "order paid" / "ticket
-- refunded" + an "allowed" switch); this table is the same thing for arena,
-- read by the Telegram leg of the outbox fan-out
-- (internal/platform/salesnotify), so a message follows the v1.order.paid /
-- v1.ticket.* event within seconds.
--
--   org_id NULL  — an operator subscription: every organization's sales.
--   org_id set   — only that organization's sales.
--
-- chat_id is text, not bigint: Telegram ids are signed 52-bit integers today
-- but the Bot API also accepts "@channelusername". When a group is upgraded
-- to a supergroup, Telegram answers the old id with migrate_to_chat_id and
-- the notifier rewrites chat_id itself.
--
-- sales_notification_deliveries remembers what was already announced
-- ('paid:<order id>', 'refund:<ticket id>'). The outbox fan-out retries a
-- whole event when ANY leg fails, and one refunded ticket can raise both
-- v1.ticket.refunded and v1.ticket.cancelled — neither may post twice.
--
-- No permissions: nothing here is reachable through the HTTP API yet.

-- +goose Up

CREATE TABLE sales_notification_subscriptions (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id             uuid        REFERENCES organizations(id) ON DELETE CASCADE,
    name               text        NOT NULL CHECK (length(trim(name)) > 0),
    chat_id            text        NOT NULL CHECK (length(trim(chat_id)) > 0),
    on_order_paid      boolean     NOT NULL DEFAULT true,
    on_ticket_refunded boolean     NOT NULL DEFAULT true,
    allowed            boolean     NOT NULL DEFAULT true,
    last_error         text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX sales_notification_subscriptions_org_chat_uq
    ON sales_notification_subscriptions (COALESCE(org_id::text, ''), chat_id);

COMMENT ON TABLE sales_notification_subscriptions IS
    'Telegram chats that receive sale/refund notifications; org_id NULL = operator (all organizations).';
COMMENT ON COLUMN sales_notification_subscriptions.last_error IS
    'Last delivery failure reported by Telegram (bot not in the chat, chat not found), NULL after a success.';

CREATE TABLE sales_notification_deliveries (
    key        text        PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE sales_notification_deliveries IS
    'One row per announced sale or refund (paid:<order_id> / refund:<ticket_id>) so an outbox retry never posts twice.';

-- +goose Down

DROP TABLE IF EXISTS sales_notification_deliveries;
DROP TABLE IF EXISTS sales_notification_subscriptions;

-- 0111_sales_notification_subscriptions.sql — per-organization Telegram
-- sales notifications.
--
-- The ops watchdog (0104) sends every sale to ONE operator chat. Organizers
-- used to get their own Telegram notifications from Bil24 (a notification
-- per organization: chat id + triggers "order paid" / "ticket refunded" +
-- an "allowed" switch); this table is the same thing for arena, read by the
-- sales.notify worker job (internal/platform/salesnotify).
--
--   org_id NULL  — an operator subscription: every organization's sales.
--   org_id set   — only that organization's sales.
--
-- chat_id is text, not bigint: Telegram ids are signed 52-bit integers today
-- but the Bot API also accepts "@channelusername". When a group is upgraded
-- to a supergroup, Telegram answers the old id with migrate_to_chat_id and
-- the job rewrites chat_id itself.
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

-- +goose Down

DROP TABLE IF EXISTS sales_notification_subscriptions;

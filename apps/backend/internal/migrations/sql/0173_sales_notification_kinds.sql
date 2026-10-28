-- 0173_sales_notification_kinds.sql — what a Telegram sales chat listens to.
--
-- sales_notification_subscriptions (0111) was about tickets only. The
-- merchandise shop (spec 08_architecture/47, lane L5) announces its own orders
-- to the same kind of chat — typically the organizer's logistics group — so a
-- subscription now says WHICH sales it wants:
--
--   tickets  the ticket sale / refund messages (the only kind until now)
--   shop     shop order paid / refunded messages
--
-- Both may be listed. Existing rows keep {tickets}, so nothing changes for
-- them. The unique index (org, chat) is untouched: one chat is one row, and
-- the array says what it hears. on_order_paid / on_ticket_refunded remain the
-- per-chat mute switches and apply to both kinds.
--
-- No permission: the table is still reachable only through SQL (see
-- docs/ops/shop_notifications_ru.md for the logistics group row).

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_notification_subscriptions
    ADD COLUMN kinds text[] NOT NULL DEFAULT '{tickets}';

ALTER TABLE sales_notification_subscriptions
    ADD CONSTRAINT sales_notification_subscriptions_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop']::text[]);

COMMENT ON COLUMN sales_notification_subscriptions.kinds IS
    'Which announcements the chat receives: tickets (sales and refunds of tickets), shop (merchandise shop orders).';

-- +goose Down

ALTER TABLE sales_notification_subscriptions
    DROP CONSTRAINT IF EXISTS sales_notification_subscriptions_kinds_check;

ALTER TABLE sales_notification_subscriptions
    DROP COLUMN IF EXISTS kinds;

-- 0175_shop_order_staff_note.sql — the internal note on a shop order.
--
-- A manager writes down an agreement with the buyer ("called, deliver after
-- 18:00") on the order card and the next manager sees it. The note is for
-- staff only: it never reaches the storefront, the buyer's letters or the
-- courier text. staff_note_by is the author's name at the time of the save
-- (a snapshot, so a later rename or a removed account changes nothing),
-- staff_note_at the time. An empty note has no author and no time.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_orders
    ADD COLUMN staff_note    text        NOT NULL DEFAULT '',
    ADD COLUMN staff_note_by text        NOT NULL DEFAULT '',
    ADD COLUMN staff_note_at timestamptz;

ALTER TABLE shop_orders
    ADD CONSTRAINT shop_orders_staff_note_check CHECK (
        length(staff_note) <= 2000 AND length(staff_note_by) <= 200);

COMMENT ON COLUMN shop_orders.staff_note IS
    'Internal note of the shop staff. Never shown to the buyer.';

-- +goose Down

ALTER TABLE shop_orders
    DROP CONSTRAINT IF EXISTS shop_orders_staff_note_check;

ALTER TABLE shop_orders
    DROP COLUMN IF EXISTS staff_note_at,
    DROP COLUMN IF EXISTS staff_note_by,
    DROP COLUMN IF EXISTS staff_note;

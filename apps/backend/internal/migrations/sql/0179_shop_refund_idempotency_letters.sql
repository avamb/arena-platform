-- 0179_shop_refund_idempotency_letters.sql — two things the shop audit of
-- 2026-10-30 found missing (SA-01, SA-11).
--
-- 1. A shop refund is repeatable by key. POST .../shop/orders/{id}/refund
--    now requires an Idempotency-Key. The key lives on the refunds row it
--    created (idempotency_key, unique per organization) together with the shop
--    order it was asked for (shop_order_id), so a repeated call answers the
--    first refund instead of writing a second one. Ticket refunds keep their
--    own refund_batches key and leave both columns NULL.
--
-- 2. The letters of a shop order are recorded in email_letters like every
--    other buyer letter. A shop order is not a row of orders, so the letter
--    names it in shop_order_id (order_id stays NULL) and the three shop
--    kinds are allowed.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE refunds
    ADD COLUMN shop_order_id   uuid REFERENCES shop_orders(id) ON DELETE SET NULL,
    ADD COLUMN idempotency_key text;

ALTER TABLE refunds
    ADD CONSTRAINT refunds_idempotency_key_check
        CHECK (idempotency_key IS NULL OR length(btrim(idempotency_key)) BETWEEN 1 AND 255);

CREATE UNIQUE INDEX refunds_org_idempotency_key_uq
    ON refunds (org_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX refunds_shop_order_idx
    ON refunds (shop_order_id)
    WHERE shop_order_id IS NOT NULL;

ALTER TABLE email_letters
    ADD COLUMN shop_order_id uuid REFERENCES shop_orders(id) ON DELETE SET NULL;

ALTER TABLE email_letters
    DROP CONSTRAINT email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund',
                        'shop_received', 'shop_shipped', 'shop_refunded'));

CREATE INDEX email_letters_shop_order_idx
    ON email_letters (shop_order_id, sent_at DESC)
    WHERE shop_order_id IS NOT NULL;

COMMENT ON COLUMN refunds.idempotency_key IS
    'Idempotency-Key of the shop refund call that created the row (NULL for every other refund).';
COMMENT ON COLUMN email_letters.shop_order_id IS
    'The shop order a shop_* letter is about (order_id is NULL for those letters).';

-- +goose Down

DROP INDEX IF EXISTS email_letters_shop_order_idx;

DELETE FROM email_letters WHERE kind IN ('shop_received', 'shop_shipped', 'shop_refunded');

ALTER TABLE email_letters
    DROP CONSTRAINT IF EXISTS email_letters_kind_check,
    ADD CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund'));

ALTER TABLE email_letters DROP COLUMN IF EXISTS shop_order_id;

DROP INDEX IF EXISTS refunds_shop_order_idx;
DROP INDEX IF EXISTS refunds_org_idempotency_key_uq;

ALTER TABLE refunds
    DROP CONSTRAINT IF EXISTS refunds_idempotency_key_check,
    DROP COLUMN IF EXISTS idempotency_key,
    DROP COLUMN IF EXISTS shop_order_id;

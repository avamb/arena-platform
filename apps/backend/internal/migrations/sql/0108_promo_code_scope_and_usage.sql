-- 0108_promo_code_scope_and_usage.sql — a promo code names the sessions it is
-- for, the currency it is worth, and every redemption names its order.
--
-- Owner decisions of 2026-09-22 (08_architecture/25_promo_codes_plan_ru.md §6):
--
--   * a code applies to a SESSION ("промокод применяется к сеансу"), not to a
--     whole event and not only to a category. applies_to_session_ids is that
--     scope; an empty array keeps the pre-0108 meaning — any session of the
--     organization. applies_to_tier_ids stays as an extra narrowing inside
--     the chosen sessions.
--   * a fixed_amount discount is a sum of money and money has a currency. An
--     organization that sells in CZK and EUR would otherwise take "500" off
--     both. NULL is the legacy value: a code created before this migration
--     applies to any currency, exactly as it did.
--   * the organizer reads a usage report per code. A redemption used to name
--     only the reservation, so "which order, which buyer, which site" needed a
--     join through a row that the expiry sweep may have rewritten. order_id,
--     customer_id and channel_id are copied onto the redemption at the moment
--     it is written; the backfill below derives them for the rows that exist.
--
-- One redemption per order: the partial unique index lets the two paths that
-- record usage — PAY_ORDER for a selling site, the payment webhook for the
-- widget — be replayed without counting a buyer twice.

-- +goose Up

ALTER TABLE promo_codes
    ADD COLUMN applies_to_session_ids uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$');

ALTER TABLE promo_code_redemptions
    ADD COLUMN order_id    uuid REFERENCES orders(id)         ON DELETE SET NULL,
    ADD COLUMN customer_id uuid REFERENCES customers(id)      ON DELETE SET NULL,
    ADD COLUMN channel_id  uuid REFERENCES sales_channels(id) ON DELETE SET NULL;

UPDATE promo_code_redemptions r
SET    order_id    = o.id,
       customer_id = o.customer_id,
       channel_id  = o.channel_id
FROM   orders o
WHERE  o.reservation_id = r.reservation_id
  AND  o.promo_code_id  = r.promo_code_id
  AND  r.order_id IS NULL;

CREATE UNIQUE INDEX promo_code_redemptions_order_uq
    ON promo_code_redemptions (order_id)
    WHERE order_id IS NOT NULL;

CREATE INDEX promo_code_redemptions_customer
    ON promo_code_redemptions (promo_code_id, customer_id)
    WHERE customer_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS promo_code_redemptions_customer;
DROP INDEX IF EXISTS promo_code_redemptions_order_uq;

ALTER TABLE promo_code_redemptions
    DROP COLUMN channel_id,
    DROP COLUMN customer_id,
    DROP COLUMN order_id;

ALTER TABLE promo_codes
    DROP COLUMN currency,
    DROP COLUMN applies_to_session_ids;

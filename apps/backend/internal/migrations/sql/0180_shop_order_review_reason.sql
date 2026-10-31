-- 0180_shop_order_review_reason.sql — second audit of the shop, 2026-10-30
-- (SB-02, SB-04).
--
-- An order parked in manual_review has one of two very different causes, and
-- until now they were told apart only by metadata.review_reason (read with a
-- substring match in places):
--
--   late_payment_no_stock  the money WAS taken, but the hold had ended and the
--                          goods are gone (metadata value 'stock_short');
--   payment_unconfirmed    the provider's report is not trusted (the amount
--                          differs, a refund before confirmation): the money
--                          may NOT have arrived (metadata value
--                          'payment_manual_review').
--
-- review_reason names the cause in a column, so the buyer's page, the
-- logistician's Telegram message and the operator's resolve action can branch
-- on it. It is meaningful while status = 'manual_review' and is cleared when
-- the order leaves it (a fully refunded order keeps it, so a refund the
-- provider later reverses returns the order to manual_review).
--
-- paid_at used to be stamped for every manual_review order, so an order whose
-- payment was NOT confirmed looked paid. It is now stamped only when money was
-- really taken; the backfill below clears it on the unconfirmed ones.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_orders
    ADD COLUMN review_reason text;

ALTER TABLE shop_orders
    ADD CONSTRAINT shop_orders_review_reason_check
        CHECK (review_reason IS NULL OR review_reason IN ('late_payment_no_stock', 'payment_unconfirmed'));

UPDATE shop_orders
SET    review_reason = CASE metadata ->> 'review_reason'
                           WHEN 'stock_short' THEN 'late_payment_no_stock'
                           WHEN 'payment_manual_review' THEN 'payment_unconfirmed'
                       END
WHERE  status IN ('manual_review', 'refunded')
  AND  shipped_at IS NULL
  AND  metadata ->> 'review_reason' IN ('stock_short', 'payment_manual_review');

UPDATE shop_orders
SET    paid_at = NULL
WHERE  status = 'manual_review' AND review_reason = 'payment_unconfirmed';

COMMENT ON COLUMN shop_orders.review_reason IS
    'Why a manual_review order waits for a person: late_payment_no_stock (money taken, goods gone) or payment_unconfirmed (the payment itself is not confirmed). NULL otherwise.';

-- +goose Down

ALTER TABLE shop_orders
    DROP CONSTRAINT IF EXISTS shop_orders_review_reason_check,
    DROP COLUMN IF EXISTS review_reason;

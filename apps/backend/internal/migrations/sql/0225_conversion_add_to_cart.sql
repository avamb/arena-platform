-- 0225_conversion_add_to_cart.sql — the widget's checkout sends AddToCart to
-- Meta and GA4 from the server (only with the buyer's explicit tracking
-- consent). conversion_deliveries.kind gains 'add_to_cart'; it is an order-less
-- event like initiate_checkout (order_id NULL, event_id the dedup key).
--
-- No permission is seeded.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_kind_chk;
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_kind_chk
    CHECK (kind IN ('purchase', 'page_view', 'view_content', 'initiate_checkout', 'lead', 'subscribe',
                    'add_to_cart'));

-- +goose Down
SET LOCAL lock_timeout = '5s';

DELETE FROM conversion_deliveries WHERE kind = 'add_to_cart';
ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_kind_chk;
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_kind_chk
    CHECK (kind IN ('purchase', 'page_view', 'view_content', 'initiate_checkout', 'lead', 'subscribe'));

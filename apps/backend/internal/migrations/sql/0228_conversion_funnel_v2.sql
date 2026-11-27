-- 0228_conversion_funnel_v2.sql — the Meta and GA4 funnel v2 (spec 68):
-- conversion_deliveries.kind gains 'tickets_click' (a click on a tickets
-- button of an event page or a static site), 'add_payment_info' (the buyer's
-- complete payment form) and 'accept_terms' (the ticked terms of sale). All
-- three are order-less events like add_to_cart (order_id NULL, event_id the
-- dedup key). 0227 is reserved by another open pull request.
--
-- No permission is seeded.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_kind_chk;
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_kind_chk
    CHECK (kind IN ('purchase', 'page_view', 'view_content', 'initiate_checkout', 'lead', 'subscribe',
                    'add_to_cart', 'tickets_click', 'add_payment_info', 'accept_terms'));

-- +goose Down
SET LOCAL lock_timeout = '5s';

DELETE FROM conversion_deliveries WHERE kind IN ('tickets_click', 'add_payment_info', 'accept_terms');
ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_kind_chk;
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_kind_chk
    CHECK (kind IN ('purchase', 'page_view', 'view_content', 'initiate_checkout', 'lead', 'subscribe',
                    'add_to_cart'));

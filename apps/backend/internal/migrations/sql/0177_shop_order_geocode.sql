-- 0177_shop_order_geocode.sql — real coordinates of a shop order's address.
--
-- The worker job shop.geocode_address asks the Google Geocoding API for the
-- point of a SHIPPING order's address and stores the answer here, so the
-- logistics Telegram message and the event center's courier text can link to
-- the exact spot instead of a text search. Every column is NULL until the job
-- ran (or for ever when geocoding is switched off).
--
-- ship_geocode_status: ok (rooftop or interpolated point), approximate (a
-- centre of a street, postal code or town only), not_found (Google knows no
-- such address, final) and failed (an error or a quota refusal, retried by the
-- worker). lat/lng are set for ok and approximate only.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_orders
    ADD COLUMN ship_lat               double precision,
    ADD COLUMN ship_lng               double precision,
    ADD COLUMN ship_geocode_status    text,
    ADD COLUMN ship_geocoded_at       timestamptz,
    ADD COLUMN ship_formatted_address text,
    ADD COLUMN ship_place_id          text;

ALTER TABLE shop_orders
    ADD CONSTRAINT shop_orders_ship_geocode_check CHECK (
        (ship_geocode_status IS NULL
            OR ship_geocode_status IN ('ok', 'approximate', 'not_found', 'failed'))
        AND (ship_lat IS NULL OR ship_lat BETWEEN -90 AND 90)
        AND (ship_lng IS NULL OR ship_lng BETWEEN -180 AND 180)
        AND ((ship_lat IS NULL) = (ship_lng IS NULL)));

COMMENT ON COLUMN shop_orders.ship_geocode_status IS
    'NULL (not geocoded), ok, approximate, not_found or failed. See migration 0177.';

-- +goose Down

ALTER TABLE shop_orders
    DROP CONSTRAINT IF EXISTS shop_orders_ship_geocode_check;

ALTER TABLE shop_orders
    DROP COLUMN IF EXISTS ship_place_id,
    DROP COLUMN IF EXISTS ship_formatted_address,
    DROP COLUMN IF EXISTS ship_geocoded_at,
    DROP COLUMN IF EXISTS ship_geocode_status,
    DROP COLUMN IF EXISTS ship_lng,
    DROP COLUMN IF EXISTS ship_lat;

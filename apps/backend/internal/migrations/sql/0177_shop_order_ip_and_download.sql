-- 0177_shop_order_ip_and_download.sql — audit SA-05 and SA-34 of the shop.
--
-- SA-05: a storefront checkout holds stock until the payment window ends, so
-- one client could "buy out" a limited run without paying. The cap on live
-- unpaid orders is counted in the database, not in process memory (two API
-- replicas must not double the budget), per shop and per client key. The key
-- is a salted sha256 of the client address (an IPv6 address is keyed by its
-- /64 prefix), never the address itself. NULL = unknown (imported and staff
-- orders, orders placed before this migration).
--
-- SA-34: a digital line remembers the file the buyer paid for (media_id, a
-- snapshot, not a foreign key: replacing the product's file later must not
-- change what an old order downloads) and how many times the downloads route
-- issued a link for it (download_count). The route refuses a line after the
-- limit and an order thirty days after payment (410 shop.download_expired).

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_orders
    ADD COLUMN buyer_ip_hash text;

ALTER TABLE shop_orders
    ADD CONSTRAINT shop_orders_buyer_ip_hash_check CHECK (
        buyer_ip_hash IS NULL OR buyer_ip_hash ~ '^[0-9a-f]{64}$');

CREATE INDEX shop_orders_pending_ip_idx
    ON shop_orders (org_id, buyer_ip_hash)
    WHERE status = 'pending_payment' AND buyer_ip_hash IS NOT NULL;

ALTER TABLE shop_order_items
    ADD COLUMN media_id       uuid,
    ADD COLUMN download_count integer NOT NULL DEFAULT 0;

ALTER TABLE shop_order_items
    ADD CONSTRAINT shop_order_items_download_count_check CHECK (download_count >= 0);

-- Orders that already exist keep downloading the file their product sells now,
-- the only file they could ever have been shown.
UPDATE shop_order_items oi
SET    media_id = p.digital_media_id
FROM   shop_variants v
JOIN   shop_products p ON p.id = v.product_id
WHERE  oi.variant_id = v.id
  AND  oi.product_kind = 'digital'
  AND  p.digital_media_id IS NOT NULL
  AND  oi.media_id IS NULL;

COMMENT ON COLUMN shop_orders.buyer_ip_hash IS
    'Salted sha256 of the buyer client key (IPv4 address or IPv6 /64) at checkout; NULL when unknown.';
COMMENT ON COLUMN shop_order_items.media_id IS
    'The file a digital line sold when it was bought (snapshot of shop_products.digital_media_id).';
COMMENT ON COLUMN shop_order_items.download_count IS
    'How many times the downloads route issued a link for this line.';

-- +goose Down

ALTER TABLE shop_order_items
    DROP CONSTRAINT IF EXISTS shop_order_items_download_count_check;

ALTER TABLE shop_order_items
    DROP COLUMN IF EXISTS download_count,
    DROP COLUMN IF EXISTS media_id;

DROP INDEX IF EXISTS shop_orders_pending_ip_idx;

ALTER TABLE shop_orders
    DROP CONSTRAINT IF EXISTS shop_orders_buyer_ip_hash_check;

ALTER TABLE shop_orders
    DROP COLUMN IF EXISTS buyer_ip_hash;

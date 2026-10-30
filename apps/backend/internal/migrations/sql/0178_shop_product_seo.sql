-- 0178_shop_product_seo.sql — what the Shopify export carries besides the text
-- of a product (audit SA-29).
--
-- A product keeps its search-engine title and description, its tags; a variant
-- its weight and barcode. The importer (cmd/arena-shop-import) fills them from
-- "SEO Title", "SEO Description", "Tags", "Variant Grams" and "Variant
-- Barcode"; showing them on the storefront and editing them in the event
-- center are separate backlog items. An empty text is stored as NULL.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_products
    ADD COLUMN seo_title       text,
    ADD COLUMN seo_description text,
    ADD COLUMN tags            text[] NOT NULL DEFAULT '{}';

ALTER TABLE shop_products
    ADD CONSTRAINT shop_products_seo_check CHECK (
        (seo_title IS NULL OR length(seo_title) BETWEEN 1 AND 255)
        AND (seo_description IS NULL OR length(seo_description) BETWEEN 1 AND 1000)
        AND cardinality(tags) <= 50);

ALTER TABLE shop_variants
    ADD COLUMN weight_grams integer,
    ADD COLUMN barcode      text;

ALTER TABLE shop_variants
    ADD CONSTRAINT shop_variants_meta_check CHECK (
        (weight_grams IS NULL OR weight_grams BETWEEN 0 AND 100000000)
        AND (barcode IS NULL OR length(barcode) BETWEEN 1 AND 64));

-- +goose Down

ALTER TABLE shop_variants
    DROP CONSTRAINT IF EXISTS shop_variants_meta_check;

ALTER TABLE shop_variants
    DROP COLUMN IF EXISTS barcode,
    DROP COLUMN IF EXISTS weight_grams;

ALTER TABLE shop_products
    DROP CONSTRAINT IF EXISTS shop_products_seo_check;

ALTER TABLE shop_products
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS seo_description,
    DROP COLUMN IF EXISTS seo_title;

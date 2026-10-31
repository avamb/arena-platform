-- 0174_shop_page_legacy_slug.sql — the old address of a storefront page.
--
-- The Shopify import gives a page a new slug (the Latin "events") while the
-- live shop answered at the page's own handle, which is often not Latin
-- (/pages/наши-мероприятия). Links from social networks and search results
-- carry the old handle, so a page may remember it: GET
-- /v1/public/shop/{org_slug}/pages/{slug} resolves a request by slug first and
-- by legacy_slug second, and always answers the canonical slug.
--
-- NULL = the page has no other address. The importer fills it from the Shopify
-- handle when that differs from the slug. Unique per organization, so one old
-- address names one page.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_pages
    ADD COLUMN legacy_slug text;

ALTER TABLE shop_pages
    ADD CONSTRAINT shop_pages_legacy_slug_check CHECK (
        legacy_slug IS NULL OR (
            length(legacy_slug) BETWEEN 1 AND 200
            AND legacy_slug = lower(legacy_slug)
            AND legacy_slug !~ '[/?#[:space:]]'));

CREATE UNIQUE INDEX shop_pages_org_legacy_slug_uq
    ON shop_pages (org_id, legacy_slug)
    WHERE legacy_slug IS NOT NULL;

COMMENT ON COLUMN shop_pages.legacy_slug IS
    'The handle this page had on the shop it was imported from, when it differs from slug. The public page route resolves it as an alias and answers the canonical slug.';

-- +goose Down

DROP INDEX IF EXISTS shop_pages_org_legacy_slug_uq;

ALTER TABLE shop_pages
    DROP CONSTRAINT IF EXISTS shop_pages_legacy_slug_check;

ALTER TABLE shop_pages
    DROP COLUMN IF EXISTS legacy_slug;

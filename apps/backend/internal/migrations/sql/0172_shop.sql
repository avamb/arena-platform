-- +goose Up
-- =====================================================================
-- arena_new — the organizer's merchandise shop, first version
-- (spec 08_architecture/47_merch_shop_module_ru.md section 2, lane L1)
--
-- An organization sells physical goods (wine, merch) from its own
-- storefront. Money goes through the organization's OWN payment provider
-- account, exactly like tickets: a shop order owns a payment_intents row
-- whose checkout_session_id is NULL, and shop_orders.payment_intent_id
-- points back at it.
--
-- shop_settings          one row per organization (currency, flat shipping,
--                        the sales channel whose provider takes the money)
-- shop_products          catalog, soft-deleted
-- shop_product_images    media_objects of a product, ordered
-- shop_variants          a product always has at least one; stock lives here
-- shop_orders            one storefront purchase (or an imported one)
-- shop_order_items       snapshot of what was bought
-- shop_stock_moves       journal of every stock and hold movement
-- shop_pages             static storefront pages (about, contacts, policies)
-- organizations.shop_domain   the storefront host, resolved by
--                             GET /v1/public/shop/by-host
--
-- Stock model: shop_variants.stock is what is on the shelf. A pending order
-- HOLDS its quantities while it is pending_payment and not past expires_at;
-- availability = stock - held. shop_stock_moves rows with reason
-- reserve/release journal the hold (they never change stock); sale, refund,
-- manual and import rows journal a change of shop_variants.stock. stock is
-- deliberately not CHECKed >= 0: a payment that arrives after its hold
-- expired is still recorded (the money was taken) and may oversell.
--
-- All money is bigint minor units with a char(3) currency.
--
-- Permissions shop.read / shop.manage are granted to org_admin, organizer and
-- platform_superadmin in this same migration (the 532 parity guard).
-- media_objects.owner_type gains 'shop_product' (mediastore.AllowedOwnerTypes
-- mirrors it).
-- =====================================================================
SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations ADD COLUMN shop_domain text;
ALTER TABLE organizations ADD CONSTRAINT organizations_shop_domain_check
    CHECK (shop_domain IS NULL OR (
        length(shop_domain) BETWEEN 3 AND 253
        AND shop_domain = lower(shop_domain)
        AND shop_domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'));
CREATE UNIQUE INDEX organizations_shop_domain_uq ON organizations (shop_domain) WHERE shop_domain IS NOT NULL;

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report',
            'shop_product'
        ));

CREATE TABLE shop_settings (
    org_id             uuid        PRIMARY KEY REFERENCES organizations(id),
    currency           char(3)     NOT NULL DEFAULT 'EUR',
    shipping_fee_minor bigint      NOT NULL DEFAULT 0,
    shipping_label     text        NOT NULL DEFAULT '',
    country_iso2       char(2),
    age_gate_min_age   integer,
    announcement_text  text        NOT NULL DEFAULT '',
    contact_email      text,
    logistics_note     text        NOT NULL DEFAULT '',
    sales_channel_id   uuid        REFERENCES sales_channels(id),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shop_settings_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT shop_settings_shipping_fee_check CHECK (shipping_fee_minor >= 0),
    CONSTRAINT shop_settings_country_check CHECK (country_iso2 IS NULL OR country_iso2 ~ '^[A-Z]{2}$'),
    CONSTRAINT shop_settings_age_gate_check CHECK (age_gate_min_age IS NULL OR age_gate_min_age BETWEEN 1 AND 99),
    CONSTRAINT shop_settings_texts_check CHECK (
        length(shipping_label) <= 200 AND length(announcement_text) <= 500
        AND length(logistics_note) <= 2000 AND (contact_email IS NULL OR length(contact_email) <= 254))
);

CREATE TABLE shop_products (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations(id),
    slug         text        NOT NULL,
    title        text        NOT NULL,
    body_html    text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'draft',
    product_type text        NOT NULL DEFAULT '',
    vendor       text        NOT NULL DEFAULT '',
    sort_order   integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    CONSTRAINT shop_products_slug_check CHECK (
        length(slug) BETWEEN 1 AND 200 AND slug = lower(slug) AND slug !~ '[/?#[:space:]]'),
    CONSTRAINT shop_products_title_check CHECK (length(title) BETWEEN 1 AND 300),
    CONSTRAINT shop_products_status_check CHECK (status IN ('draft', 'active', 'archived')),
    CONSTRAINT shop_products_texts_check CHECK (
        length(body_html) <= 100000 AND length(product_type) <= 200 AND length(vendor) <= 200)
);

-- A slug is unique among the organization's live products; a deleted
-- product frees it.
CREATE UNIQUE INDEX shop_products_org_slug_uq ON shop_products (org_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX shop_products_org_idx ON shop_products (org_id, sort_order, created_at) WHERE deleted_at IS NULL;

CREATE TABLE shop_product_images (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id uuid        NOT NULL REFERENCES shop_products(id) ON DELETE CASCADE,
    media_id   uuid        NOT NULL REFERENCES media_objects(id),
    position   integer     NOT NULL DEFAULT 0,
    alt        text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shop_product_images_alt_check CHECK (length(alt) <= 500)
);

CREATE UNIQUE INDEX shop_product_images_product_media_uq ON shop_product_images (product_id, media_id);

CREATE TABLE shop_variants (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id       uuid        NOT NULL REFERENCES shop_products(id),
    title            text        NOT NULL DEFAULT 'Default Title',
    sku              text,
    price_minor      bigint      NOT NULL,
    compare_at_minor bigint,
    stock            integer     NOT NULL DEFAULT 0,
    track_stock      boolean     NOT NULL DEFAULT true,
    position         integer     NOT NULL DEFAULT 0,
    option1          text,
    option2          text,
    option3          text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz,
    CONSTRAINT shop_variants_title_check CHECK (length(title) BETWEEN 1 AND 300),
    CONSTRAINT shop_variants_sku_check CHECK (sku IS NULL OR length(sku) BETWEEN 1 AND 100),
    CONSTRAINT shop_variants_price_check CHECK (price_minor >= 0),
    CONSTRAINT shop_variants_compare_at_check CHECK (compare_at_minor IS NULL OR compare_at_minor >= 0),
    CONSTRAINT shop_variants_options_check CHECK (
        (option1 IS NULL OR length(option1) <= 200) AND (option2 IS NULL OR length(option2) <= 200)
        AND (option3 IS NULL OR length(option3) <= 200))
);

CREATE INDEX shop_variants_product_idx ON shop_variants (product_id, position) WHERE deleted_at IS NULL;

CREATE TABLE shop_orders (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid        NOT NULL REFERENCES organizations(id),
    number            bigint      NOT NULL UNIQUE DEFAULT nextval('compatibility_system_id_seq'),
    order_token       text        NOT NULL,
    customer_id       uuid        REFERENCES customers(id),
    status            text        NOT NULL DEFAULT 'pending_payment',
    buyer_name        text        NOT NULL DEFAULT '',
    buyer_email       text        NOT NULL DEFAULT '',
    buyer_phone       text        NOT NULL DEFAULT '',
    ship_name         text        NOT NULL DEFAULT '',
    ship_address1     text        NOT NULL DEFAULT '',
    ship_address2     text        NOT NULL DEFAULT '',
    ship_city         text        NOT NULL DEFAULT '',
    ship_postal       text        NOT NULL DEFAULT '',
    ship_country      text        NOT NULL DEFAULT '',
    delivery_kind     text        NOT NULL DEFAULT 'shipping',
    delivery_note     text        NOT NULL DEFAULT '',
    currency          char(3)     NOT NULL,
    subtotal          bigint      NOT NULL,
    discount          bigint      NOT NULL DEFAULT 0,
    shipping_fee      bigint      NOT NULL DEFAULT 0,
    total             bigint      NOT NULL,
    refunded_minor    bigint      NOT NULL DEFAULT 0,
    payment_intent_id uuid        REFERENCES payment_intents(id),
    provider          text,
    buyer_locale      text,
    source            text        NOT NULL DEFAULT 'web',
    external_ref      text,
    paid_at           timestamptz,
    shipped_at        timestamptz,
    cancelled_at      timestamptz,
    tracking_note     text        NOT NULL DEFAULT '',
    expires_at        timestamptz,
    metadata          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shop_orders_status_check CHECK (status IN (
        'pending_payment', 'paid', 'shipped', 'cancelled', 'expired', 'refunded', 'partially_refunded')),
    CONSTRAINT shop_orders_delivery_kind_check CHECK (delivery_kind IN ('shipping', 'pickup')),
    CONSTRAINT shop_orders_source_check CHECK (source IN ('web', 'shopify')),
    CONSTRAINT shop_orders_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT shop_orders_amounts_check CHECK (
        subtotal >= 0 AND discount >= 0 AND shipping_fee >= 0 AND total >= 0
        AND refunded_minor >= 0 AND refunded_minor <= total),
    CONSTRAINT shop_orders_total_check CHECK (total = subtotal - discount + shipping_fee),
    CONSTRAINT shop_orders_token_check CHECK (order_token ~ '^[0-9a-f]{64}$'),
    CONSTRAINT shop_orders_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT shop_orders_texts_check CHECK (
        length(buyer_name) <= 200 AND length(buyer_email) <= 254 AND length(buyer_phone) <= 50
        AND length(ship_name) <= 200 AND length(ship_address1) <= 300 AND length(ship_address2) <= 300
        AND length(ship_city) <= 120 AND length(ship_postal) <= 30 AND length(ship_country) <= 2
        AND length(delivery_note) <= 2000 AND length(tracking_note) <= 2000)
);

CREATE UNIQUE INDEX shop_orders_token_uq ON shop_orders (order_token);
CREATE UNIQUE INDEX shop_orders_payment_intent_uq ON shop_orders (payment_intent_id) WHERE payment_intent_id IS NOT NULL;
CREATE UNIQUE INDEX shop_orders_external_ref_uq ON shop_orders (org_id, source, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX shop_orders_org_idx ON shop_orders (org_id, created_at DESC);
CREATE INDEX shop_orders_pending_idx ON shop_orders (expires_at) WHERE status = 'pending_payment';

CREATE TABLE shop_order_items (
    id               uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id         uuid    NOT NULL REFERENCES shop_orders(id) ON DELETE CASCADE,
    variant_id       uuid    REFERENCES shop_variants(id),
    product_title    text    NOT NULL,
    variant_title    text    NOT NULL DEFAULT '',
    sku              text,
    qty              integer NOT NULL,
    unit_price_minor bigint  NOT NULL,
    total_minor      bigint  NOT NULL,
    ordinal          integer NOT NULL,
    CONSTRAINT shop_order_items_qty_check CHECK (qty > 0),
    CONSTRAINT shop_order_items_price_check CHECK (unit_price_minor >= 0),
    CONSTRAINT shop_order_items_total_check CHECK (total_minor = unit_price_minor * qty),
    CONSTRAINT shop_order_items_ordinal_uq UNIQUE (order_id, ordinal)
);

CREATE INDEX shop_order_items_variant_idx ON shop_order_items (variant_id) WHERE variant_id IS NOT NULL;

CREATE TABLE shop_stock_moves (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid        NOT NULL REFERENCES organizations(id),
    variant_id uuid        NOT NULL REFERENCES shop_variants(id),
    order_id   uuid        REFERENCES shop_orders(id),
    delta      integer     NOT NULL,
    reason     text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shop_stock_moves_reason_check CHECK (reason IN ('reserve', 'release', 'sale', 'refund', 'manual', 'import'))
);

CREATE INDEX shop_stock_moves_variant_idx ON shop_stock_moves (variant_id, created_at);
CREATE INDEX shop_stock_moves_order_idx ON shop_stock_moves (order_id) WHERE order_id IS NOT NULL;

CREATE TABLE shop_pages (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid        NOT NULL REFERENCES organizations(id),
    slug       text        NOT NULL,
    title      text        NOT NULL,
    body_html  text        NOT NULL DEFAULT '',
    position   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shop_pages_slug_check CHECK (
        length(slug) BETWEEN 1 AND 200 AND slug = lower(slug) AND slug !~ '[/?#[:space:]]'),
    CONSTRAINT shop_pages_title_check CHECK (length(title) BETWEEN 1 AND 300),
    CONSTRAINT shop_pages_body_check CHECK (length(body_html) <= 200000),
    CONSTRAINT shop_pages_org_slug_uq UNIQUE (org_id, slug)
);

INSERT INTO permissions (name, description) VALUES
    ('shop.read',   'Read the shop: settings, products, stock and orders'),
    ('shop.manage', 'Edit the shop catalog and settings, ship, cancel and refund shop orders')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('shop.read', 'shop.manage')
WHERE  r.name IN ('org_admin', 'organizer')
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('shop.read', 'shop.manage')
WHERE  r.name = 'platform_superadmin'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name IN ('shop.read', 'shop.manage'));

DELETE FROM permissions WHERE name IN ('shop.read', 'shop.manage');

DROP TABLE IF EXISTS shop_pages;
DROP TABLE IF EXISTS shop_stock_moves;
DROP TABLE IF EXISTS shop_order_items;
DROP TABLE IF EXISTS shop_orders;
DROP TABLE IF EXISTS shop_variants;
DROP TABLE IF EXISTS shop_product_images;
DROP TABLE IF EXISTS shop_products;
DROP TABLE IF EXISTS shop_settings;

DELETE FROM media_objects WHERE owner_type = 'shop_product';

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report'
        ));

DROP INDEX IF EXISTS organizations_shop_domain_uq;
ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_shop_domain_check;
ALTER TABLE organizations DROP COLUMN IF EXISTS shop_domain;

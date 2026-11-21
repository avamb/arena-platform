-- 0212_legacy_ticket_imports.sql — tickets sold in the Bil24 era move onto the
-- platform (Vino&Co cutover, owner decision 2026-11-08).
--
-- Until 2026-09-25 Vino&Co sold through Bil24; those tickets live only in the
-- WordPress order meta. cmd/arena-legacy-tickets-import files each such order
-- as a platform order so the buyer sees it under "My tickets", the guest list
-- and its automatic sends carry it, and the session counts it as sold. Nothing
-- of that import is delivered anywhere: no outbox event, no webhook, no MACS
-- push, no letter, no billing usage. MACS already knows the barcodes.
--
-- orders.source 'legacy_import': the marker every consumer filters on (billing
-- collect and reconcile, the ops sales feed, referral eligibility). The Go
-- mirror is ordering.SourceLegacyImport.
--
-- wp_order_links.bil24_order_id: the 7-digit Bil24 order number of the
-- WooCommerce order (bil24_external_order_id), so a WordPress letter of that
-- order is matched to the platform order (cmd/arena-wp-letters-import).
--
-- legacy_ticket_imports: one row per imported ticket. (org_id, bil24_ticket_id)
-- is what makes a second run a no-op; the original barcode itself is a
-- barcodes row in the 'legacy_bil24' authority.
--
-- No permission is seeded.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE orders DROP CONSTRAINT orders_source_check;
ALTER TABLE orders ADD CONSTRAINT orders_source_check CHECK (source IN
    ('bil24_gateway', 'public_feed', 'checkout_api', 'complimentary', 'legacy_import'));

ALTER TABLE wp_order_links ADD COLUMN bil24_order_id bigint
    CHECK (bil24_order_id IS NULL OR bil24_order_id > 0);

CREATE UNIQUE INDEX wp_order_links_bil24_order_uq
    ON wp_order_links (org_id, bil24_order_id) WHERE bil24_order_id IS NOT NULL;

COMMENT ON COLUMN wp_order_links.bil24_order_id IS
    'The Bil24 order number of a WooCommerce order of the Bil24 era (bil24_external_order_id), set by arena-legacy-tickets-import.';

CREATE TABLE legacy_ticket_imports (
    ticket_id        uuid        PRIMARY KEY REFERENCES tickets(id) ON DELETE CASCADE,
    org_id           uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    order_id         uuid        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    bil24_ticket_id  bigint      NOT NULL CHECK (bil24_ticket_id > 0),
    bil24_order_id   bigint      NOT NULL CHECK (bil24_order_id > 0),
    barcode          text        NOT NULL CHECK (barcode ~ '^[0-9]{13}$'),
    wc_order_id      text        NOT NULL CHECK (length(wc_order_id) BETWEEN 1 AND 64),
    imported_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT legacy_ticket_imports_org_ticket_uq UNIQUE (org_id, bil24_ticket_id)
);

CREATE INDEX legacy_ticket_imports_order_idx ON legacy_ticket_imports (order_id);

COMMENT ON TABLE legacy_ticket_imports IS
    'A ticket sold in Bil24 before the platform and filed by arena-legacy-tickets-import: its Bil24 ticket id, order number and original barcode.';

-- +goose Down
DROP TABLE IF EXISTS legacy_ticket_imports;
DROP INDEX IF EXISTS wp_order_links_bil24_order_uq;
ALTER TABLE wp_order_links DROP COLUMN IF EXISTS bil24_order_id;
-- Refuses while imported orders exist: they would not fit the old CHECK.
ALTER TABLE orders DROP CONSTRAINT orders_source_check;
ALTER TABLE orders ADD CONSTRAINT orders_source_check CHECK (source IN
    ('bil24_gateway', 'public_feed', 'checkout_api', 'complimentary'));

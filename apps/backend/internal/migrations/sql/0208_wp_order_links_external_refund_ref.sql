-- 0208_wp_order_links_external_refund_ref.sql — spec 62 section 14.6 (P-14).
--
-- wp_order_links: the correspondence of an order of the WordPress era
-- (WooCommerce order id, AllPay order id) to the platform order
-- (orders.system_id) and its tickets (tickets.system_ticket_id). The operator
-- loads it with cmd/arena-wp-orders-import when WordPress is stopped, so a
-- refund made later in the AllPay cabinet can be matched to the platform's
-- tickets. One row per (organization, WooCommerce order): a re-run of the
-- import changes nothing.
--
-- refunds.external_ref: the refund number the payment provider's cabinet gave
-- for an external refund (settlement 'external'), kept for reconciliation. It
-- is NOT unique: one AllPay refund may cover several tickets of an order.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE wp_order_links (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    wc_order_id        text        NOT NULL CHECK (length(wc_order_id) BETWEEN 1 AND 64),
    allpay_order_id    text        CHECK (allpay_order_id IS NULL OR length(allpay_order_id) <= 128),
    order_id           uuid        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    ticket_system_ids  bigint[]    NOT NULL DEFAULT '{}',
    imported_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wp_order_links_org_wc_uq UNIQUE (org_id, wc_order_id)
);

CREATE INDEX wp_order_links_order_idx ON wp_order_links (order_id);
CREATE INDEX wp_order_links_allpay_idx ON wp_order_links (org_id, allpay_order_id) WHERE allpay_order_id IS NOT NULL;

COMMENT ON TABLE wp_order_links IS
    'Spec 62 14.6: WooCommerce order id <-> AllPay order id <-> platform order <-> ticket system ids, loaded once by arena-wp-orders-import.';

ALTER TABLE refunds ADD COLUMN external_ref text
    CHECK (external_ref IS NULL OR length(external_ref) <= 120);

COMMENT ON COLUMN refunds.external_ref IS
    'Refund number of the provider cabinet for a settlement=external refund (reconciliation only, not unique).';

-- +goose Down
ALTER TABLE refunds DROP COLUMN IF EXISTS external_ref;
DROP TABLE IF EXISTS wp_order_links;

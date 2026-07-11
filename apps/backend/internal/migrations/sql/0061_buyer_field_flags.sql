-- +goose Up
-- =====================================================================
-- arena_new — Buyer-field collection flags on sales_channels
-- Feature #321 WID-0d.
--
-- Adds two boolean flags to sales_channels that control which optional
-- buyer fields the checkout widget collects. When false (default) the
-- widget only collects the buyer's email; when true, the widget also
-- renders and requires the corresponding field.
--
-- Organizers toggle these flags per channel via the admin UI; the
-- public feed endpoint exposes them as buyer_fields:[{key,required,
-- enabled}] so the widget can build its checkout form from the API
-- response without any hard-coded assumptions.
-- =====================================================================

ALTER TABLE sales_channels
    ADD COLUMN IF NOT EXISTS collect_name  boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS collect_phone boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN sales_channels.collect_name IS
    'When true, the checkout widget collects and requires buyer.name. '
    'Default false (email-only checkout). Feature #321 WID-0d.';

COMMENT ON COLUMN sales_channels.collect_phone IS
    'When true, the checkout widget collects and requires buyer.phone. '
    'Default false (email-only checkout). Feature #321 WID-0d.';

-- +goose Down
ALTER TABLE sales_channels
    DROP COLUMN IF EXISTS collect_name,
    DROP COLUMN IF EXISTS collect_phone;

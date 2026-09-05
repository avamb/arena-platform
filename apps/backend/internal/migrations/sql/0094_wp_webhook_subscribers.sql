-- 0094_wp_webhook_subscribers.sql — W1-B7a (spec §3.5).
--
-- The WordPress sites (Lampyris, Vino&Co) receive Bil24-shaped webhooks
-- (kind='bil24_wp') per SALES CHANNEL, not per organization: one WP site is
-- one sales channel of the org, and each site registers its own callback.
-- MACS (kind='macs') stays org-scoped and is untouched by this migration.

-- +goose Up
ALTER TABLE webhook_subscribers
    ADD COLUMN IF NOT EXISTS channel_id uuid REFERENCES sales_channels(id) ON DELETE CASCADE;

-- At most one ACTIVE bil24_wp subscriber per channel; re-registration
-- deactivates the previous row (spec §9.2) instead of colliding.
CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_subscribers_bil24_wp_per_channel
    ON webhook_subscribers (channel_id)
    WHERE kind = 'bil24_wp' AND active = TRUE;

-- +goose Down
DROP INDEX IF EXISTS uq_webhook_subscribers_bil24_wp_per_channel;

ALTER TABLE webhook_subscribers
    DROP COLUMN IF EXISTS channel_id;

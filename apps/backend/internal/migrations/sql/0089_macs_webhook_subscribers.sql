-- +goose Up
ALTER TABLE webhook_subscribers
    ADD COLUMN kind   text NOT NULL DEFAULT 'generic',
    ADD COLUMN org_id uuid REFERENCES organizations(id) ON DELETE CASCADE;

ALTER TABLE webhook_subscribers
    DROP CONSTRAINT IF EXISTS webhook_subscribers_callback_url_unique;

CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_subscribers_macs_per_org
    ON webhook_subscribers (org_id)
    WHERE kind = 'macs' AND active = TRUE;

CREATE INDEX IF NOT EXISTS idx_webhook_subscribers_kind_active
    ON webhook_subscribers (kind, active)
    WHERE active = TRUE;

-- +goose Down
DROP INDEX IF EXISTS idx_webhook_subscribers_kind_active;
DROP INDEX IF EXISTS uq_webhook_subscribers_macs_per_org;

ALTER TABLE webhook_subscribers
    ADD CONSTRAINT webhook_subscribers_callback_url_unique UNIQUE (callback_url);

ALTER TABLE webhook_subscribers
    DROP COLUMN IF EXISTS org_id,
    DROP COLUMN IF EXISTS kind;

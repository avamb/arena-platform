-- +goose Up
-- +goose StatementBegin

-- webhook_subscribers: persistent registry of HTTP endpoints that receive
-- outbox events from the Arena platform via signed webhook POST requests.
--
-- A subscriber is matched to an outbox event when:
--   - active = TRUE, AND
--   - event_types is empty (wildcard — receives all event types), OR
--     the array contains the event's event_type string.
--
-- The signing_secret is stored in plaintext here so the dispatcher can
-- compute HMAC-SHA256(secret, body) to set the X-Arena-Signature header.
-- Treat it with the same care as any credential (restrict SELECT access).
--
-- Feature #156 — WordPress webhook receiver / subscriber registration.
CREATE TABLE IF NOT EXISTS webhook_subscribers (
    id             UUID        NOT NULL DEFAULT gen_random_uuid(),
    site_url       TEXT        NOT NULL,
    callback_url   TEXT        NOT NULL,
    signing_secret TEXT        NOT NULL,
    event_types    TEXT[]      NOT NULL DEFAULT '{}',
    active         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT webhook_subscribers_pkey PRIMARY KEY (id),
    CONSTRAINT webhook_subscribers_callback_url_unique UNIQUE (callback_url)
);

-- Index for fast active-subscriber fan-out lookups.
CREATE INDEX IF NOT EXISTS idx_webhook_subscribers_active
    ON webhook_subscribers (active)
    WHERE active = TRUE;

-- RBAC seed: platform superadmins and org_admins can manage webhook subscribers.
INSERT INTO permissions (name, description)
VALUES ('webhook.subscriber.manage', 'Manage webhook subscribers')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('admin', 'org_admin')
  AND p.name = 'webhook.subscriber.manage'
ON CONFLICT DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX  IF EXISTS idx_webhook_subscribers_active;
DROP TABLE  IF EXISTS webhook_subscribers;

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name = 'webhook.subscriber.manage'
);

DELETE FROM permissions WHERE name = 'webhook.subscriber.manage';
-- +goose StatementEnd

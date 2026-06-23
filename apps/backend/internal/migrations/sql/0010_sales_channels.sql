-- +goose Up
-- =====================================================================
-- arena_new — Sales Channels (Wave 2, feature #121)
--
-- Sales channels represent the payment and commercial configuration for
-- a specific organization. Each channel belongs to one organization and
-- defines how payments are processed (direct_merchant vs merchant_of_record),
-- which payment provider to use (stripe|allpay), provider credentials,
-- fee percentage, and an optional override for the reservation TTL.
--
-- Design decisions (ADR-011 — direct-merchant default):
--   * payment_mode defaults to 'direct_merchant' (org receives funds directly).
--   * provider_account_id is REQUIRED when payment_mode = 'direct_merchant'
--     (it identifies the merchant's account with the payment provider).
--   * reservation_ttl_override overrides the parent organization's
--     reservation_ttl_seconds when set (enables per-channel seat-hold windows).
--   * Soft-delete: deleted_at IS NULL for active channels.
--   * updated_at is maintained by the UPDATE trigger inherited from the
--     organizations pattern; here we track it manually via UPDATE SET.
-- =====================================================================

CREATE TABLE sales_channels (
    id                       uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id                   uuid        NOT NULL REFERENCES organizations(id),
    name                     text        NOT NULL,
    payment_mode             text        NOT NULL DEFAULT 'direct_merchant'
                                         CHECK (payment_mode IN ('direct_merchant', 'merchant_of_record')),
    provider                 text        NOT NULL DEFAULT 'stripe'
                                         CHECK (provider IN ('stripe', 'allpay')),
    provider_account_id      text,
    fee_percent              numeric(5,2) NOT NULL DEFAULT 0.00,
    reservation_ttl_override integer,   -- NULL = use org default; seconds
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    deleted_at               timestamptz          -- NULL = active; non-NULL = soft-deleted
);

-- Index: list channels by org quickly.
CREATE INDEX channels_org_id_active ON sales_channels (org_id)
    WHERE deleted_at IS NULL;

-- Partial unique index: channel name unique within an org (active only).
CREATE UNIQUE INDEX channels_name_org_unique_active ON sales_channels (org_id, name)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE sales_channels IS
    'Payment and commercial configuration for an organization channel. '
    'Each channel defines payment_mode (direct_merchant|merchant_of_record), '
    'the payment provider (stripe|allpay), provider credentials, fee percentage, '
    'and an optional reservation TTL override. Supports soft-delete. '
    'Feature #121 — Wave 2.';

COMMENT ON COLUMN sales_channels.payment_mode IS
    'How payments are processed. direct_merchant: funds go directly to the '
    'merchant account (provider_account_id required). '
    'merchant_of_record: platform collects funds and remits to merchant.';

COMMENT ON COLUMN sales_channels.provider_account_id IS
    'External payment provider account identifier. Required when '
    'payment_mode = ''direct_merchant''.';

COMMENT ON COLUMN sales_channels.reservation_ttl_override IS
    'Seat-hold expiry override in seconds. NULL means use the parent '
    'organization''s reservation_ttl_seconds. Takes precedence when set.';

COMMENT ON COLUMN sales_channels.deleted_at IS
    'Soft-delete timestamp. NULL means the channel is active.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for sales channel management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('channel.create', 'Create a new sales channel within an organization'),
    ('channel.read',   'Read sales channel details and list channels for an organization'),
    ('channel.update', 'Update an existing sales channel'),
    ('channel.delete', 'Soft-delete a sales channel')
ON CONFLICT DO NOTHING;

-- Grant all channel permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('channel.create', 'channel.read', 'channel.update', 'channel.delete')
ON CONFLICT DO NOTHING;

-- Grant all channel permissions to org_admin (tenant admin manages own channels).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('channel.create', 'channel.read', 'channel.update', 'channel.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('channel.create', 'channel.read', 'channel.update', 'channel.delete')
);
DELETE FROM permissions
WHERE name IN ('channel.create', 'channel.read', 'channel.update', 'channel.delete');
DROP TABLE IF EXISTS sales_channels;

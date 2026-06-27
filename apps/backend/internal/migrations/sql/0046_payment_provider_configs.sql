-- +goose Up
-- =====================================================================
-- arena_new — Payment provider configs (feature #237)
--
-- Per-organization payment provider configuration. Captures both the
-- public connection metadata (which provider, which account_id, which
-- mode, free-form public knobs) and the secret credentials needed to
-- talk to the provider (api_key, webhook_secret, …).
--
-- Why a dedicated table (not a reuse of sales_channels)?
--
--   * sales_channels carries commercial config (name, fee_percent, TTL
--     overrides, payment_mode) and is the unit a checkout/feed token
--     attaches to. It already mixes ONE provider per channel.
--   * The org may need MULTIPLE configured providers at once — e.g.
--     Stripe in `live` mode plus AllPay in `test` mode for a parallel
--     run — and channels select a provider by name. The configs are
--     therefore a separate, provider-keyed resource.
--   * Secrets must be excluded from GET responses but kept on the row.
--     Bundling them into sales_channels would force every channel
--     consumer to learn about the secret-masking layer.
--
-- Status discipline:
--
--   * status = 'configured' when every required secret for the chosen
--     provider is present (see internal/payments/required_secrets.go).
--   * status = 'missing_required_fields' when at least one required
--     secret is empty. The HTTP layer attaches the list of missing
--     field names to the response so admins can see what to fill in.
--   * status is a stored column so simple LIST queries can filter on
--     it without rehydrating the secrets jsonb.
--
-- Soft-delete:
--   * deleted_at IS NULL for active rows. Partial unique index ensures
--     at most ONE active row per (org_id, provider, mode). Deleted
--     rows free up the slot.
-- =====================================================================

CREATE TABLE payment_provider_configs (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    provider            text        NOT NULL CHECK (provider <> ''),
    mode                text        NOT NULL CHECK (mode IN ('test', 'live')),
    provider_account_id text,
    public_config       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    secrets             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status              text        NOT NULL DEFAULT 'missing_required_fields'
        CHECK (status IN ('configured', 'missing_required_fields')),
    is_active           boolean     NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz
);

-- Partial unique index: one active config per (org, provider, mode).
CREATE UNIQUE INDEX payment_provider_configs_unique_active
    ON payment_provider_configs (org_id, provider, mode)
    WHERE deleted_at IS NULL;

-- List by org quickly.
CREATE INDEX payment_provider_configs_org_active
    ON payment_provider_configs (org_id)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE payment_provider_configs IS
    'Per-organization payment provider configuration. Holds both public '
    'connection metadata (provider, mode, account_id, public_config) and '
    'secret credentials (secrets jsonb) needed to authenticate against '
    'the provider. Secrets are never returned in GET responses. '
    'Feature #237.';

COMMENT ON COLUMN payment_provider_configs.provider IS
    'Provider slug, e.g. ''stripe'', ''allpay'', ''cloudpayments''. '
    'Matches the provider name used by sales_channels.provider.';

COMMENT ON COLUMN payment_provider_configs.mode IS
    'Operating mode for the credential set: ''test'' or ''live''. '
    'Allows running test + live in parallel against the same org.';

COMMENT ON COLUMN payment_provider_configs.provider_account_id IS
    'Optional public account identifier (e.g. Stripe ''acct_...'' or '
    'AllPay merchant id). Safe to return in GET responses.';

COMMENT ON COLUMN payment_provider_configs.public_config IS
    'Non-secret provider knobs (e.g. statement_descriptor, terminal_id, '
    'redirect_url). Always a JSON object — never null. Returned in GET.';

COMMENT ON COLUMN payment_provider_configs.secrets IS
    'Secret credentials (api_key, webhook_secret, …). Stored on the row '
    'but NEVER returned by the GET/LIST endpoints. Always a JSON object.';

COMMENT ON COLUMN payment_provider_configs.status IS
    '''configured'' when every required secret for the chosen provider '
    'is present; otherwise ''missing_required_fields''. Cached on the '
    'row so LIST can filter without rehydrating secrets.';

-- =====================================================================
-- Seed RBAC permissions for payment config management
-- =====================================================================

INSERT INTO permissions (name, description) VALUES
    ('payment_config.read',
        'Read payment provider configs for an organization (secrets excluded).'),
    ('payment_config.write',
        'Create, update, or soft-delete payment provider configs.')
ON CONFLICT DO NOTHING;

-- Grant both perms to the platform admin role (the 0008 broad-grant
-- pattern). Repeat explicit grant for idempotent re-seed.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  r.org_id IS NULL
  AND  p.name IN ('payment_config.read', 'payment_config.write')
ON CONFLICT DO NOTHING;

-- Grant both perms to org_admin so tenant admins can manage their
-- own org's payment configuration without platform staff intervention.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('payment_config.read', 'payment_config.write')
ON CONFLICT DO NOTHING;

-- Grant read-only to platform_superadmin so cross-tenant support staff
-- can see WHICH providers an org has wired up (without ever seeing
-- secrets, which are stripped by the HTTP serializer).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'platform_superadmin'
  AND  r.org_id IS NULL
  AND  p.name = 'payment_config.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE  permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('payment_config.read', 'payment_config.write')
);
DELETE FROM permissions
WHERE name IN ('payment_config.read', 'payment_config.write');

DROP INDEX IF EXISTS payment_provider_configs_unique_active;
DROP INDEX IF EXISTS payment_provider_configs_org_active;
DROP TABLE IF EXISTS payment_provider_configs;

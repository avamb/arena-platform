-- 0096_api_keys.sql — W1-C1a (feature #512, spec §3.6 / §13.1).
--
-- Organization service credentials ("API keys") used by server-to-server
-- callers (e.g. the WordPress "lampyris-ops" plugin) that cannot hold a
-- user session. A key is scoped to an org (and optionally to one sales
-- channel), carries an explicit permission-code allow-list in `scopes`,
-- and can expire or be revoked independently of any user account.
--
-- The migration number differs from the spec's illustrative "0095" because
-- 0095 was already taken by 0095_customer_read_role_grants.sql when this
-- sub-feature landed; 0096 is the next free head (see AGENTS.md migration
-- numbering note — Head() picks the max numeric filename prefix, not the
-- spec's example number).
--
-- Wire format for the raw key (never stored): `ak_<prefix12>_<secret43>`.
-- `key_prefix` is the 12-character lookup key (unique, indexed via the
-- column's own UNIQUE constraint); `key_hash` is bcrypt(secret). The full
-- raw key is shown to the caller exactly once, at issue time, the same
-- pattern used for the MACS webhook `signing_secret`
-- (hcatalog/macs_webhook.go:92).

-- +goose Up
CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    channel_id   uuid REFERENCES sales_channels(id) ON DELETE CASCADE,
    name         text NOT NULL,
    key_prefix   text NOT NULL UNIQUE,
    key_hash     text NOT NULL,
    scopes       text[] NOT NULL,
    created_by   uuid NOT NULL REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);

COMMENT ON TABLE api_keys IS
    'W1-C1a: organization service credentials (spec §3.6 / §13.1). key_prefix '
    'is the 12-char lookup key embedded in the wire token ak_<prefix12>_<secret43>; '
    'key_hash is bcrypt of the 43-char secret half. scopes is an explicit '
    'allow-list of permissions.name codes; api_key.manage, and platform.*/'
    'admin.* codes are rejected by the issuing package regardless of what a '
    'caller requests. revoked_at/expires_at gate Authenticate(); last_used_at '
    'is updated by the issuing package at most once a minute.';

COMMENT ON COLUMN api_keys.key_prefix IS
    'W1-C1a: 12-character lookup segment of ak_<prefix12>_<secret43>, unique '
    'so Authenticate() can find the candidate row before verifying the '
    'bcrypt secret.';

CREATE INDEX idx_api_keys_org_id ON api_keys (org_id);

INSERT INTO permissions (name, description) VALUES
  ('api_key.manage', 'Issue and revoke organization API keys'),
  ('import.bil24_session', 'Import a Bil24 session (event, tiers, plan, seats) preserving ids');

-- +goose Down
DELETE FROM permissions WHERE name IN ('api_key.manage', 'import.bil24_session');
DROP TABLE IF EXISTS api_keys;

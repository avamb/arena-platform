-- +goose Up
-- =====================================================================
-- arena_new — Agent Feed Tokens (Wave 2, feature #122)
--
-- Agent feed tokens are public read-only credentials that allow external
-- agents (widgets, embeds, scanner apps) to consume a sales channel's
-- event feed without possessing a full JWT session. They are designed
-- following ADR-013 (federated feeds):
--
--   * The token value is a cryptographically random 32-byte hex string
--     generated on the application side before INSERT.
--   * Tokens are globally unique (UNIQUE constraint on token column).
--   * Each token is scoped to a single sales channel (sales_channel_id FK).
--   * A token may be deactivated (is_active=false) or hard-revoked
--     (revoked_at IS NOT NULL + is_active=false) via the DELETE endpoint.
--   * last_used_at is touched on every public feed read — the column is
--     updated independently of the main CRUD flow.
--   * label is a human-readable name for the token (e.g. "website widget").
--   * Soft-delete is NOT used here: token revocation is a one-way operation
--     implemented as revoked_at + is_active=false. The row is kept for audit.
-- =====================================================================

CREATE TABLE agent_feed_tokens (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    token            text        NOT NULL UNIQUE,
    sales_channel_id uuid        NOT NULL REFERENCES sales_channels(id),
    label            text        NOT NULL DEFAULT '',
    is_active        boolean     NOT NULL DEFAULT true,
    revoked_at       timestamptz,           -- NULL = not revoked
    last_used_at     timestamptz,           -- NULL = never used
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Index: look up tokens by sales channel for management API.
CREATE INDEX feed_tokens_channel_id ON agent_feed_tokens (sales_channel_id);

-- Index: look up a token value quickly (public feed reads hit this path).
CREATE UNIQUE INDEX feed_tokens_token_unique ON agent_feed_tokens (token);

COMMENT ON TABLE agent_feed_tokens IS
    'Public read-only credentials scoped to a sales channel. Used by external '
    'agents (widgets, scanner apps) to consume event feeds without a full JWT. '
    'ADR-013 (federated feeds). Feature #122 — Wave 2.';

COMMENT ON COLUMN agent_feed_tokens.token IS
    'Cryptographically random 32-byte hex token generated application-side. '
    'Safe to embed in client HTML — grants only read access to the channel feed.';

COMMENT ON COLUMN agent_feed_tokens.is_active IS
    'FALSE when the token has been revoked. Revoked tokens are rejected on '
    'every public feed read. The row is retained for audit purposes.';

COMMENT ON COLUMN agent_feed_tokens.revoked_at IS
    'Timestamp when the token was explicitly revoked via DELETE endpoint. '
    'NULL means the token is still potentially active (check is_active).';

COMMENT ON COLUMN agent_feed_tokens.last_used_at IS
    'Timestamp of the most recent successful public feed read using this token. '
    'Updated asynchronously (best-effort) to avoid blocking the feed response.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for feed token management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('feed_token.create', 'Issue a new agent feed token for a sales channel'),
    ('feed_token.read',   'List or retrieve agent feed tokens for a sales channel'),
    ('feed_token.delete', 'Revoke an agent feed token')
ON CONFLICT DO NOTHING;

-- Grant all feed token permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('feed_token.create', 'feed_token.read', 'feed_token.delete')
ON CONFLICT DO NOTHING;

-- Grant all feed token permissions to org_admin (tenant admin manages own tokens).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('feed_token.create', 'feed_token.read', 'feed_token.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('feed_token.create', 'feed_token.read', 'feed_token.delete')
);
DELETE FROM permissions
WHERE name IN ('feed_token.create', 'feed_token.read', 'feed_token.delete');
DROP TABLE IF EXISTS agent_feed_tokens;

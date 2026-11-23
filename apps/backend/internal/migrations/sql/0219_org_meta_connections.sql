-- 0219_org_meta_connections.sql — an organization connects its Meta business
-- through "Facebook Login for Business" (spec 08_architecture/66, stage M1).
--
-- org_meta_connections  one connection per organization. access_token_enc is
--                       the business integration system user token sealed by
--                       internal/platform/secretbox (AAD = the org id bytes),
--                       never plaintext; it is set to NULL on revoke. No route
--                       returns it and it never reaches a log or the audit.
--                       meta_user_id is the app-scoped id from GET /me: Meta's
--                       deauthorize and data-deletion callbacks name only it.
-- org_meta_assets       the datasets (pixels), ad accounts and pages the
--                       organizer gave access to. selected marks the one of a
--                       kind the platform uses (at most one per kind).
--
-- No new permission: the routes are gated on org.update (the owner), which
-- platform_superadmin already holds.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE org_meta_connections (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    meta_business_id text        NOT NULL DEFAULT '',
    meta_user_id     text        NOT NULL,
    config_id        text        NOT NULL DEFAULT '',
    access_token_enc text,
    token_expires_at timestamptz,
    scopes           text[]      NOT NULL DEFAULT '{}',
    status           text        NOT NULL DEFAULT 'active',
    last_error       text,
    connected_by     uuid        REFERENCES users (id) ON DELETE SET NULL,
    connected_at     timestamptz NOT NULL DEFAULT now(),
    checked_at       timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT org_meta_connections_org_uq UNIQUE (org_id),
    CONSTRAINT org_meta_connections_status_chk CHECK (status IN ('active', 'revoked', 'error')),
    CONSTRAINT org_meta_connections_active_token_chk CHECK (status <> 'active' OR access_token_enc IS NOT NULL)
);
CREATE INDEX org_meta_connections_meta_user_idx ON org_meta_connections (meta_user_id);

COMMENT ON TABLE org_meta_connections IS
    'The Meta business connection of an organization (spec 66 M1). access_token_enc is sealed by secretbox, NULL after a revoke; no route returns it.';
COMMENT ON COLUMN org_meta_connections.access_token_enc IS
    'secretbox value "<keyid>:<base64url>" of the Meta access token, AAD = org_id bytes. Never plaintext.';
COMMENT ON COLUMN org_meta_connections.meta_user_id IS
    'App-scoped user id from GET /me; the deauthorize and data-deletion callbacks find the connection by it.';

CREATE TABLE org_meta_assets (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    kind       text        NOT NULL,
    meta_id    text        NOT NULL,
    name       text        NOT NULL DEFAULT '',
    currency   text,
    timezone   text,
    selected   boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT org_meta_assets_kind_chk CHECK (kind IN ('dataset', 'ad_account', 'page')),
    CONSTRAINT org_meta_assets_uq UNIQUE (org_id, kind, meta_id)
);
CREATE UNIQUE INDEX org_meta_assets_one_selected_uq
    ON org_meta_assets (org_id, kind) WHERE selected;

COMMENT ON TABLE org_meta_assets IS
    'Datasets, ad accounts and pages of the Meta connection of an organization; selected = the one of its kind in use (at most one).';

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TABLE org_meta_assets;
DROP TABLE org_meta_connections;

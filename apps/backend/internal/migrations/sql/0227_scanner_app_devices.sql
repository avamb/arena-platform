-- 0227_scanner_app_devices.sql — the door-control app on the arena API
-- (spec 69, task MA-10): scanner devices, controller passes and one-time
-- pairing codes.
--
-- A phone that checks tickets at the door is a DEVICE. It belongs to ONE
-- organization and speaks either for a person (an organizer device: the
-- person's membership in that organization must still hold scan.perform) or
-- for a controller PASS (a pass device: a door controller without an account
-- in arena). Issuing and exchanging pairing codes, token refresh, the bot
-- screens and the scans themselves are later tasks; this migration only
-- creates the tables they write.
--
--   scanner_passes          a controller pass of an organization.
--     kind                  standard: works from valid_from until valid_until;
--                           demo: no window (the app store review pass).
--     max_devices           how many non-revoked devices may use the pass.
--   scanner_pass_sessions   the dates a pass may check.
--     all_tiers             true: every category of the date, and the pass has
--                           no scanner_pass_tiers rows for it. false: ONLY the
--                           categories in scanner_pass_tiers; when none of them
--                           is left the pass checks nothing on that date (a
--                           restricted date never widens to every category).
--   scanner_pass_tiers      the categories of a date with all_tiers = false.
--   scanner_devices         a paired phone.
--     access_token_hash     SHA-256 hex of the device token; the token itself
--                           is never stored.
--     refresh_token_hash    SHA-256 hex of the refresh token (a later task).
--     revoke_reason         why the device was taken back.
--   scanner_pairing_codes   a one-time code a phone exchanges for a device.
--     code_hash             SHA-256 hex of the code; the code itself is never
--                           stored.
--     used_device_id        the device the code became; referenced through
--                           (used_device_id, org_id), so it is always a device
--                           of the code's organization.
--
-- A device and a pairing code of a pass reference the pass through
-- (pass_id, org_id), so neither can sit in another organization than its
-- pass.
--
-- Three permissions are seeded and granted to org_admin, organizer and
-- platform_superadmin in this file (rule of TestSuperadminPermissionParity532).
-- No API key may carry them (apikeys.IsForbiddenScope).

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE scanner_passes (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    label              text        NOT NULL,
    kind               text        NOT NULL DEFAULT 'standard',
    valid_from         timestamptz,
    valid_until        timestamptz,
    max_devices        integer     NOT NULL,
    created_by_user_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    revoked_at         timestamptz,
    revoked_by_user_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT scanner_passes_id_org_uq UNIQUE (id, org_id),
    CONSTRAINT scanner_passes_label_check CHECK (length(btrim(label)) > 0 AND length(label) <= 80),
    CONSTRAINT scanner_passes_kind_check CHECK (kind IN ('standard', 'demo')),
    CONSTRAINT scanner_passes_window_check CHECK (
        (kind = 'demo' AND valid_from IS NULL AND valid_until IS NULL)
        OR (kind = 'standard' AND valid_from IS NOT NULL AND valid_until IS NOT NULL
            AND valid_until > valid_from)),
    CONSTRAINT scanner_passes_max_devices_check CHECK (max_devices > 0)
);

-- Passes of an organization.
CREATE INDEX scanner_passes_org_idx ON scanner_passes (org_id, created_at DESC);

CREATE TABLE scanner_pass_sessions (
    pass_id    uuid NOT NULL REFERENCES scanner_passes(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    all_tiers  boolean NOT NULL DEFAULT true,
    PRIMARY KEY (pass_id, session_id)
);

-- The cascade from a deleted session.
CREATE INDEX scanner_pass_sessions_session_idx ON scanner_pass_sessions (session_id);

CREATE TABLE scanner_pass_tiers (
    pass_id    uuid NOT NULL,
    session_id uuid NOT NULL,
    tier_id    uuid NOT NULL REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    PRIMARY KEY (pass_id, session_id, tier_id),
    CONSTRAINT scanner_pass_tiers_session_fk FOREIGN KEY (pass_id, session_id)
        REFERENCES scanner_pass_sessions (pass_id, session_id) ON DELETE CASCADE
);

-- The cascade from a deleted category.
CREATE INDEX scanner_pass_tiers_tier_idx ON scanner_pass_tiers (tier_id);

CREATE TABLE scanner_devices (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id            uuid        REFERENCES users(id) ON DELETE CASCADE,
    pass_id            uuid,
    label              text        NOT NULL DEFAULT '',
    platform           text        NOT NULL,
    access_token_hash  text        NOT NULL,
    access_expires_at  timestamptz NOT NULL,
    refresh_token_hash text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz,
    revoked_at         timestamptz,
    revoked_by_user_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    revoke_reason      text,
    CONSTRAINT scanner_devices_id_org_uq UNIQUE (id, org_id),
    CONSTRAINT scanner_devices_pass_fk FOREIGN KEY (pass_id, org_id)
        REFERENCES scanner_passes (id, org_id) ON DELETE CASCADE,
    CONSTRAINT scanner_devices_owner_check CHECK ((user_id IS NULL) <> (pass_id IS NULL)),
    CONSTRAINT scanner_devices_label_check CHECK (length(label) <= 80),
    CONSTRAINT scanner_devices_platform_check CHECK (platform IN ('ios', 'android', 'other')),
    CONSTRAINT scanner_devices_access_token_hash_uq UNIQUE (access_token_hash),
    CONSTRAINT scanner_devices_refresh_token_hash_uq UNIQUE (refresh_token_hash),
    CONSTRAINT scanner_devices_access_token_hash_check CHECK (access_token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT scanner_devices_refresh_token_hash_check CHECK (
        refresh_token_hash IS NULL OR refresh_token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT scanner_devices_revoke_reason_check CHECK (
        revoke_reason IS NULL OR (revoked_at IS NOT NULL AND length(revoke_reason) <= 200))
);

-- Devices of an organization, of a pass and of a person.
CREATE INDEX scanner_devices_org_idx ON scanner_devices (org_id, created_at DESC);
CREATE INDEX scanner_devices_pass_idx ON scanner_devices (pass_id) WHERE pass_id IS NOT NULL;
CREATE INDEX scanner_devices_user_idx ON scanner_devices (user_id) WHERE user_id IS NOT NULL;

CREATE TABLE scanner_pairing_codes (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash          text        NOT NULL,
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id            uuid        REFERENCES users(id) ON DELETE CASCADE,
    pass_id            uuid,
    created_by_user_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    used_at            timestamptz,
    used_device_id     uuid,
    CONSTRAINT scanner_pairing_codes_code_hash_uq UNIQUE (code_hash),
    CONSTRAINT scanner_pairing_codes_device_fk FOREIGN KEY (used_device_id, org_id)
        REFERENCES scanner_devices (id, org_id) ON DELETE SET NULL (used_device_id),
    CONSTRAINT scanner_pairing_codes_code_hash_check CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT scanner_pairing_codes_pass_fk FOREIGN KEY (pass_id, org_id)
        REFERENCES scanner_passes (id, org_id) ON DELETE CASCADE,
    CONSTRAINT scanner_pairing_codes_owner_check CHECK ((user_id IS NULL) <> (pass_id IS NULL)),
    CONSTRAINT scanner_pairing_codes_expiry_check CHECK (expires_at > created_at)
);

-- Codes of an organization, of a pass and of a person; the device a code became.
CREATE INDEX scanner_pairing_codes_org_idx ON scanner_pairing_codes (org_id);
CREATE INDEX scanner_pairing_codes_pass_idx ON scanner_pairing_codes (pass_id) WHERE pass_id IS NOT NULL;
CREATE INDEX scanner_pairing_codes_user_idx ON scanner_pairing_codes (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX scanner_pairing_codes_device_idx ON scanner_pairing_codes (used_device_id) WHERE used_device_id IS NOT NULL;

INSERT INTO permissions (name, description) VALUES
    ('scanner_pass.manage', 'Create, change and revoke the controller passes and scanner devices of the organization'),
    ('scan.perform', 'Check tickets at the door with the scanner app'),
    ('scan.read', 'Read the door scans of the organization in the scanner app')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('scanner_pass.manage', 'scan.perform', 'scan.read')
WHERE  r.name IN ('org_admin', 'organizer', 'platform_superadmin')
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
SET LOCAL lock_timeout = '5s';

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions
                        WHERE name IN ('scanner_pass.manage', 'scan.perform', 'scan.read'));
DELETE FROM permissions WHERE name IN ('scanner_pass.manage', 'scan.perform', 'scan.read');

DROP TABLE IF EXISTS scanner_pairing_codes;
DROP TABLE IF EXISTS scanner_devices;
DROP TABLE IF EXISTS scanner_pass_tiers;
DROP TABLE IF EXISTS scanner_pass_sessions;
DROP TABLE IF EXISTS scanner_passes;

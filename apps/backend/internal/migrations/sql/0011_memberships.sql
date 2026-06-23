-- +goose Up
-- =====================================================================
-- arena_new — Memberships + role assignment (Wave 2, feature #120)
--
-- Implements the user → organization role relationship:
--   * memberships — the join table binding a user to an org with a role
--
-- Built-in membership roles (stored in the `roles` table for RBAC
-- permission resolution and in the memberships.role CHECK constraint):
--   organizer               — operates events for a specific org
--   agent                   — sells tickets on behalf of an org
--   platform_operator       — internal staff with wide platform access
--   external_ticketing_operator — third-party integration operator
--   platform_superadmin     — highest-privilege platform administrator
--
-- Permission codes for membership management:
--   membership.grant  — grant a role to a user in an org
--   membership.revoke — revoke a role from a user in an org
--   membership.read   — read the membership list of an org
-- =====================================================================

-- memberships: binds a user to an organization with a named role.
-- A user may hold multiple roles in the same organization (e.g. both
-- organizer and agent) so the unique constraint is on (user_id, org_id, role).
CREATE TABLE memberships (
    id        uuid        PRIMARY KEY DEFAULT uuidv7(),
    user_id   uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id    uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role      text        NOT NULL,
    status    text        NOT NULL DEFAULT 'active',
    joined_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT memberships_role_check CHECK (role IN (
        'organizer',
        'agent',
        'platform_operator',
        'external_ticketing_operator',
        'platform_superadmin'
    )),
    CONSTRAINT memberships_status_check CHECK (status IN ('active', 'suspended', 'revoked')),
    UNIQUE (user_id, org_id, role)
);

-- Index for common lookups: all active memberships for a user (permission resolution).
CREATE INDEX memberships_user_id_active_idx ON memberships (user_id)
    WHERE status = 'active';

-- Index for listing all members of an org.
CREATE INDEX memberships_org_id_active_idx ON memberships (org_id)
    WHERE status = 'active';

COMMENT ON TABLE memberships IS
    'Binds a user to an organization with a named role. A user may hold multiple '
    'roles in the same organization. status=active means the membership is in force; '
    'status=revoked means the role was explicitly removed. Feature #120 — Wave 2.';

COMMENT ON COLUMN memberships.role IS
    'One of: organizer, agent, platform_operator, external_ticketing_operator, '
    'platform_superadmin. Must match an entry in the roles table for RBAC permission '
    'resolution to work correctly.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed built-in membership roles into the roles table
-- (so RBAC permission resolution can look up their permissions)
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO roles (name, description) VALUES
    ('organizer',                   'Event organizer: operates events for a specific organization'),
    ('agent',                       'Ticket agent: sells tickets on behalf of an organization'),
    ('platform_operator',           'Internal staff with wide platform-level access'),
    ('external_ticketing_operator', 'Third-party integration operator with limited API access'),
    ('platform_superadmin',         'Highest-privilege platform administrator')
ON CONFLICT DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed permissions for membership management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('membership.grant',  'Grant a role to a user within an organization'),
    ('membership.revoke', 'Revoke a role from a user within an organization'),
    ('membership.read',   'List and read membership assignments for an organization')
ON CONFLICT DO NOTHING;

-- Grant all membership.* permissions to the admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('membership.grant', 'membership.revoke', 'membership.read')
ON CONFLICT DO NOTHING;

-- Grant membership.* permissions to org_admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('membership.grant', 'membership.revoke', 'membership.read')
ON CONFLICT DO NOTHING;

-- Grant membership.read to the organizer role (can see who else is in the org).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'organizer'
  AND  p.name = 'membership.read'
ON CONFLICT DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed org.read permission for organizer and agent roles
-- (members need to be able to see their own org)
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name IN ('organizer', 'agent', 'platform_operator', 'platform_superadmin')
  AND  p.name = 'org.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('membership.grant', 'membership.revoke', 'membership.read')
);
DELETE FROM permissions WHERE name IN ('membership.grant', 'membership.revoke', 'membership.read');
DELETE FROM role_permissions
WHERE role_id IN (
    SELECT id FROM roles WHERE name IN (
        'organizer', 'agent', 'platform_operator',
        'external_ticketing_operator', 'platform_superadmin'
    )
);
DELETE FROM roles WHERE name IN (
    'organizer', 'agent', 'platform_operator',
    'external_ticketing_operator', 'platform_superadmin'
);
DROP TABLE IF EXISTS memberships;

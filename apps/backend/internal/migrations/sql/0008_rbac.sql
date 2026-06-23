-- +goose Up
-- =====================================================================
-- arena_new — RBAC (Role-Based Access Control) scoped by org (feature #117)
--
-- Implements the real permission engine on top of the AllowAll placeholder:
--   * roles           — named roles, optionally scoped to an organization
--   * permissions     — named capabilities (e.g. "geo.admin")
--   * role_permissions— M:N join between roles and permissions
--   * user_roles      — assigns users to roles (optionally scoped by org)
--
-- Default built-in roles and permissions are seeded so that the platform
-- works out of the box after the migration runs.
-- =====================================================================

-- roles: per-org or global named roles
CREATE TABLE roles (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    name        text        NOT NULL,
    org_id      uuid,                         -- NULL = global / built-in role
    description text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Unique constraint for global roles (org_id IS NULL)
CREATE UNIQUE INDEX roles_global_name_idx ON roles (name) WHERE org_id IS NULL;
-- Unique constraint for org-scoped roles
CREATE UNIQUE INDEX roles_org_name_idx ON roles (name, org_id) WHERE org_id IS NOT NULL;

COMMENT ON TABLE roles IS
    'Named roles. Global (built-in) roles have org_id=NULL; org-scoped roles '
    'have org_id set to the owning organization UUID. Feature #117 RBAC engine.';

-- permissions: named capabilities
CREATE TABLE permissions (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    name        text        NOT NULL UNIQUE,
    description text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE permissions IS
    'Named capabilities / permission codes. The name field matches the action '
    'string passed to permissions.Checker.Check(ctx, action, resource). '
    'Examples: "geo.admin", "scaffold.echo.create". Feature #117.';

-- role_permissions: M:N join between roles and permissions
CREATE TABLE role_permissions (
    role_id       uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

COMMENT ON TABLE role_permissions IS
    'Many-to-many mapping of roles to permissions. Cascade-deletes when the '
    'parent role or permission is removed. Feature #117.';

-- user_roles: assigns users to roles
CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    org_id  uuid,                   -- NULL = global assignment
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX user_roles_user_id_idx ON user_roles (user_id);

COMMENT ON TABLE user_roles IS
    'Assigns users to roles. org_id=NULL means the assignment is global; '
    'org_id set means the user holds the role only within that organization. '
    'Feature #117.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed built-in roles
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO roles (name, description) VALUES
    ('admin',         'Global administrator with all permissions'),
    ('geo_admin',     'Administrator of geographic reference data (countries, cities)'),
    ('scaffold_user', 'Access to the scaffold echo endpoint')
ON CONFLICT DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed built-in permissions
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('geo.admin',            'Create and update countries and cities via admin endpoints'),
    ('scaffold.echo.create', 'POST /v1/scaffold/echo — scaffolding example command endpoint')
ON CONFLICT DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed role_permissions: admin gets ALL permissions
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
ON CONFLICT DO NOTHING;

-- geo_admin gets geo.admin
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'geo_admin'
  AND  p.name = 'geo.admin'
ON CONFLICT DO NOTHING;

-- scaffold_user gets scaffold.echo.create
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'scaffold_user'
  AND  p.name = 'scaffold.echo.create'
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;

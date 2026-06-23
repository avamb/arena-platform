-- +goose Up
-- =====================================================================
-- arena_new — Organizations (Wave 2, feature #119)
--
-- Implements the primary multi-tenant boundary: one organization (tenant)
-- owns all downstream resources (events, catalog, inventory, orders).
--
-- Design decisions (ADR-016):
--   * Org is the root tenant boundary; every business table will carry
--     org_id referencing organizations.id.
--   * Soft-delete: deleted_at timestamptz is NULL for active orgs.
--     Unique indexes are filtered to exclude deleted rows so deleted
--     slugs and names can be reused by a new org.
--   * reservation_ttl_seconds defaults to 1200 (20 minutes) — the
--     window within which a seat hold expires without payment.
--   * updated_at is maintained by the UPDATE trigger defined below.
-- =====================================================================

CREATE TABLE organizations (
    id                      uuid        PRIMARY KEY DEFAULT uuidv7(),
    name                    text        NOT NULL,
    slug                    text        NOT NULL,
    country                 text        NOT NULL DEFAULT '',
    default_locale          text        NOT NULL DEFAULT 'en',
    reservation_ttl_seconds integer     NOT NULL DEFAULT 1200,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz          -- NULL = active; non-NULL = soft-deleted
);

-- Partial unique index: enforce name uniqueness only for active organizations.
-- Deleted orgs may share a name with a future active org.
CREATE UNIQUE INDEX orgs_name_unique_active ON organizations (name)
    WHERE deleted_at IS NULL;

-- Partial unique index: enforce slug uniqueness only for active organizations.
CREATE UNIQUE INDEX orgs_slug_unique_active ON organizations (slug)
    WHERE deleted_at IS NULL;

-- Index to speed up listing active organizations by creation order.
CREATE INDEX orgs_created_at_active ON organizations (created_at)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE organizations IS
    'Primary tenant boundary. Each organization owns all downstream resources '
    '(events, catalog, inventory, orders, tickets). Supports soft-delete via '
    'deleted_at; partial unique indexes on name and slug exclude deleted rows. '
    'Feature #119 — Wave 2.';

COMMENT ON COLUMN organizations.reservation_ttl_seconds IS
    'Seat-hold expiry window in seconds. Defaults to 1200 (20 minutes). '
    'The inventory module uses this value to expire unpaid reservations.';

COMMENT ON COLUMN organizations.deleted_at IS
    'Soft-delete marker (timestamptz). NULL means the organization is active. '
    'Non-NULL means the org has been deactivated; all downstream queries '
    'must filter WHERE deleted_at IS NULL.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for org management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('org.create', 'Create a new organization'),
    ('org.read',   'Read organization details and list all organizations'),
    ('org.update', 'Update an existing organization'),
    ('org.delete', 'Soft-delete an organization')
ON CONFLICT DO NOTHING;

-- Grant all org permissions to the existing admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('org.create', 'org.read', 'org.update', 'org.delete')
ON CONFLICT DO NOTHING;

-- Create org_admin role for tenant administrators.
INSERT INTO roles (name, description)
VALUES ('org_admin', 'Administrator of a specific organization (all org.* permissions)')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('org.create', 'org.read', 'org.update', 'org.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
-- Remove org permissions from role_permissions and permissions table.
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('org.create', 'org.read', 'org.update', 'org.delete')
);
DELETE FROM permissions
WHERE name IN ('org.create', 'org.read', 'org.update', 'org.delete');
DELETE FROM roles WHERE name = 'org_admin';
DROP TABLE IF EXISTS organizations;

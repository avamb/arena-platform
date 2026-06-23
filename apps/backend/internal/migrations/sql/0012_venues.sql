-- +goose Up
-- =====================================================================
-- arena_new — Venues (Wave 3 — Catalog, feature #124)
--
-- Venues are physical locations owned by one organization (org_id).
-- They are linked to a city in the geo reference data (city_id FK).
-- Any organization can read (GET) venue data; only the owning org can
-- create, update, or soft-delete a venue (owner-gated mutations).
--
-- Design decisions:
--   * org_id is the immutable owner; cannot be changed after creation.
--   * city_id is a nullable FK to cities (ON DELETE RESTRICT) so the
--     city reference can be omitted for venues without a precise city.
--   * capacity_default is nullable (not all venues have a fixed capacity).
--   * Soft-delete: deleted_at IS NULL for active venues.
--   * Unique index: (org_id, name) on active venues prevents duplicate
--     names within the same organization.
-- =====================================================================

CREATE TABLE venues (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    city_id          uuid        REFERENCES cities(id) ON DELETE RESTRICT,
    name             text        NOT NULL,
    address          text,
    capacity_default integer,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz          -- NULL = active; non-NULL = soft-deleted
);

-- Index: list venues by org quickly.
CREATE INDEX venues_org_id_active ON venues (org_id)
    WHERE deleted_at IS NULL;

-- Index: list venues by city quickly.
CREATE INDEX venues_city_id_active ON venues (city_id)
    WHERE deleted_at IS NULL;

-- Partial unique index: venue name unique within an org (active only).
CREATE UNIQUE INDEX venues_name_org_unique_active ON venues (org_id, name)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE venues IS
    'Physical event locations owned by one organization. '
    'Any org can read venue data; only the owning org can mutate it. '
    'Linked to geo.cities reference table. Supports soft-delete. '
    'Feature #124 — Wave 3 Catalog.';

COMMENT ON COLUMN venues.org_id IS
    'Owning organization. Immutable after creation. '
    'Only this org may create, update, or delete the venue.';

COMMENT ON COLUMN venues.city_id IS
    'Optional FK to cities reference table. NULL means city is not specified.';

COMMENT ON COLUMN venues.address IS
    'Free-form street address. Optional.';

COMMENT ON COLUMN venues.capacity_default IS
    'Default total capacity of the venue. NULL means unspecified. '
    'Individual events may override this with their own capacity.';

COMMENT ON COLUMN venues.deleted_at IS
    'Soft-delete marker (timestamptz). NULL means the venue is active.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for venue management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('venue.create', 'Create a new venue within the owning organization'),
    ('venue.read',   'Read venue details and list venues'),
    ('venue.update', 'Update an existing venue owned by the organization'),
    ('venue.delete', 'Soft-delete a venue owned by the organization')
ON CONFLICT DO NOTHING;

-- Grant all venue permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('venue.create', 'venue.read', 'venue.update', 'venue.delete')
ON CONFLICT DO NOTHING;

-- Grant all venue permissions to org_admin (tenant admin manages own venues).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('venue.create', 'venue.read', 'venue.update', 'venue.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('venue.create', 'venue.read', 'venue.update', 'venue.delete')
);
DELETE FROM permissions
WHERE name IN ('venue.create', 'venue.read', 'venue.update', 'venue.delete');
DROP TABLE IF EXISTS venues;

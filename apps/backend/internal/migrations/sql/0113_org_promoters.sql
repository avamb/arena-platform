-- 0113_org_promoters.sql — the promoter of an event is not always the
-- organization that sells it; organizations may add cities.
--
-- An organization (the account that sells tickets through arena) is often
-- the promoter of its own events, but not always: a partner can be the
-- promoter of an event the organization sells. Until now arena had no such
-- concept and printed organizations.name as the "Organizer" on every ticket.
--
-- org_promoters is the organization's own list of promoters (name plus the
-- optional legal id — ИНН / IČO — phone and e-mail). A promoter is archived,
-- never deleted, so an event that already names it keeps rendering.
--
-- event_promoters links an event to one promoter. It is a separate table
-- rather than an events column on purpose: the shared EventRow scanner is
-- fed by many queries and widening events would oblige every one of them to
-- select the new column. NO ROW means the organization itself is the
-- promoter. Both foreign keys carry org_id, so a link can only ever name a
-- promoter of the event's own organization.
--
-- Permissions:
--   promoter.read   — every role that holds event.read
--   promoter.manage — every role that holds event.update
--   city.create     — every role that holds venue.create: an organizer
--                     creating a venue in a city arena does not know yet
--                     may add that city (POST /v1/organizations/{org_id}/cities);
--                     geo.admin stays the platform-level, API-key-forbidden
--                     permission for editing countries and cities.
-- platform_superadmin is granted all three explicitly (AGENTS.md: a new
-- permission reaches the superadmin in the migration that seeds it).

-- +goose Up

CREATE TABLE org_promoters (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name        text        NOT NULL CHECK (btrim(name) <> ''),
    legal_id    text,
    phone       text,
    email       text,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT org_promoters_id_org_uq UNIQUE (id, org_id)
);

CREATE UNIQUE INDEX org_promoters_active_name_uq
    ON org_promoters (org_id, lower(btrim(name)))
    WHERE archived_at IS NULL;

COMMENT ON TABLE org_promoters IS
    'Promoters an organization sells events for (the organization itself is the implicit default).';
COMMENT ON COLUMN org_promoters.legal_id IS
    'Company / tax identifier of the promoter (ИНН, IČO), free-form.';

-- A composite foreign key needs a unique index over exactly its columns.
CREATE UNIQUE INDEX events_id_org_id_uq ON events (id, org_id);

CREATE TABLE event_promoters (
    event_id    uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL,
    promoter_id uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_promoters_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_promoters_promoter_fk FOREIGN KEY (promoter_id, org_id)
        REFERENCES org_promoters (id, org_id)
);

CREATE INDEX event_promoters_promoter_idx ON event_promoters (promoter_id);

COMMENT ON TABLE event_promoters IS
    'The promoter of an event. No row = the organization itself is the promoter.';

INSERT INTO permissions (name, description) VALUES
  ('promoter.read', 'List the promoters of an organization'),
  ('promoter.manage', 'Create, edit and archive promoters and set the promoter of an event'),
  ('city.create', 'Add a city to an existing country from the organization side');

-- Roles that can read events can read promoters.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, np.id
FROM   role_permissions rp
JOIN   permissions ep ON ep.id = rp.permission_id AND ep.name = 'event.read'
JOIN   permissions np ON np.name = 'promoter.read'
ON CONFLICT DO NOTHING;

-- Roles that can edit events can manage promoters.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, np.id
FROM   role_permissions rp
JOIN   permissions ep ON ep.id = rp.permission_id AND ep.name = 'event.update'
JOIN   permissions np ON np.name = 'promoter.manage'
ON CONFLICT DO NOTHING;

-- Roles that can create venues can add the city a new venue is in.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT rp.role_id, np.id
FROM   role_permissions rp
JOIN   permissions ep ON ep.id = rp.permission_id AND ep.name = 'venue.create'
JOIN   permissions np ON np.name = 'city.create'
ON CONFLICT DO NOTHING;

-- Superadmin parity (TestSuperadminPermissionParity532).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('promoter.read', 'promoter.manage', 'city.create')
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM permissions WHERE name IN ('promoter.read', 'promoter.manage', 'city.create');
DROP TABLE IF EXISTS event_promoters;
DROP INDEX IF EXISTS events_id_org_id_uq;
DROP TABLE IF EXISTS org_promoters;

-- +goose Up
-- =====================================================================
-- arena_new — Events (Wave 3 — Catalog, feature #125)
--
-- An Event is a dated occurrence hosted by one organization at an
-- optional venue.  Events follow a lifecycle: draft → published →
-- cancelled|archived.  Titles and descriptions support locale
-- translations via i18n_text (namespaces "event.name" /
-- "event.description", key = event UUID::text).
--
-- Design decisions:
--   * org_id is the immutable owner; cannot be changed after creation.
--   * venue_id is a nullable FK to venues; ON DELETE RESTRICT keeps
--     the venue row alive while events reference it.
--   * status uses a CHECK constraint (not PG enum) to stay migration-
--     friendly.  Allowed values: draft, published, cancelled, archived.
--   * visibility: public (anyone), private (org members only),
--     unlisted (direct link only).
--   * Date invariant: end_at > start_at is enforced by a table CHECK.
--   * No seating_plan_id field — deferred to a later milestone.
--   * Soft-delete: deleted_at IS NULL for active events.
-- =====================================================================

CREATE TABLE events (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id      uuid        NOT NULL REFERENCES organizations(id),
    venue_id    uuid        REFERENCES venues(id) ON DELETE RESTRICT,
    name        text        NOT NULL,
    description text,
    status      text        NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft', 'published', 'cancelled', 'archived')),
    start_at    timestamptz NOT NULL,
    end_at      timestamptz NOT NULL,
    visibility  text        NOT NULL DEFAULT 'public'
                            CHECK (visibility IN ('public', 'private', 'unlisted')),
    image_url   text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,                              -- NULL = active
    CONSTRAINT  events_date_order CHECK (end_at > start_at)
);

-- Index: list events by org quickly.
CREATE INDEX events_org_id_active ON events (org_id)
    WHERE deleted_at IS NULL;

-- Index: list events by venue quickly.
CREATE INDEX events_venue_id_active ON events (venue_id)
    WHERE deleted_at IS NULL AND venue_id IS NOT NULL;

-- Index: filter events by status efficiently.
CREATE INDEX events_status_active ON events (status)
    WHERE deleted_at IS NULL;

-- Index: filter events by visibility efficiently.
CREATE INDEX events_visibility_active ON events (visibility)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE events IS
    'Dated occurrences organized by a single organization at an optional venue. '
    'Lifecycle: draft → published → cancelled|archived. '
    'Titles and descriptions may be translated via i18n_text. '
    'Feature #125 — Wave 3 Catalog.';

COMMENT ON COLUMN events.org_id IS
    'Owning organization. Immutable after creation.';

COMMENT ON COLUMN events.venue_id IS
    'Optional FK to venues. NULL means the event has no fixed venue.';

COMMENT ON COLUMN events.status IS
    'Lifecycle status: draft (default), published, cancelled, archived. '
    'Allowed transitions: draft→published, draft→cancelled, '
    'published→cancelled, published→archived, cancelled→archived.';

COMMENT ON COLUMN events.visibility IS
    'Audience visibility: public (everyone), private (org members), '
    'unlisted (direct link only).';

COMMENT ON COLUMN events.deleted_at IS
    'Soft-delete marker (timestamptz). NULL means the event is active.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for event management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('event.create',  'Create a new event within the owning organization'),
    ('event.read',    'Read event details and list events'),
    ('event.update',  'Update an existing event owned by the organization'),
    ('event.delete',  'Soft-delete an event owned by the organization'),
    ('event.publish', 'Transition an event to published status')
ON CONFLICT DO NOTHING;

-- Grant all event permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('event.create', 'event.read', 'event.update', 'event.delete', 'event.publish')
ON CONFLICT DO NOTHING;

-- Grant all event permissions to org_admin (tenant admin manages own events).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('event.create', 'event.read', 'event.update', 'event.delete', 'event.publish')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('event.create', 'event.read', 'event.update', 'event.delete', 'event.publish')
);
DELETE FROM permissions
WHERE name IN ('event.create', 'event.read', 'event.update', 'event.delete', 'event.publish');
DROP TABLE IF EXISTS events;

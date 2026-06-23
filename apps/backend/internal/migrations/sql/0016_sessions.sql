-- +goose Up
-- =====================================================================
-- arena_new — Sessions (Wave 3 — Catalog, feature #126)
--
-- A Session is a specific time slot for an Event.  Each session has
-- independent inventory: capacity_total tracks the total seats available
-- for that slot.  Multiple sessions per event are allowed; overlapping
-- sessions are permitted but flagged at the application layer.
--
-- Design decisions:
--   * event_id is the immutable owner; cannot be changed after creation.
--   * status lifecycle: draft → scheduled → completed|cancelled.
--   * capacity_total > 0 enforced by a CHECK constraint.
--   * Date invariant: end_at > start_at is enforced by a table CHECK.
--   * Soft-delete: deleted_at IS NULL for active sessions.
--   * Overlapping sessions for the same event are allowed but flagged
--     at the application layer (has_overlapping_sessions in response).
--   * Capacity propagation to the inventory module is triggered at the
--     application layer whenever capacity_total changes (hook point for
--     future inventory milestone).
-- =====================================================================

CREATE TABLE sessions (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    event_id        uuid        NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    start_at        timestamptz NOT NULL,
    end_at          timestamptz NOT NULL,
    capacity_total  integer     NOT NULL CHECK (capacity_total > 0),
    status          text        NOT NULL DEFAULT 'scheduled'
                                CHECK (status IN ('draft', 'scheduled', 'cancelled', 'completed')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,                              -- NULL = active
    CONSTRAINT sessions_date_order CHECK (end_at > start_at)
);

-- Index: list sessions by event quickly.
CREATE INDEX sessions_event_id_active ON sessions (event_id)
    WHERE deleted_at IS NULL;

-- Index: filter sessions by status efficiently.
CREATE INDEX sessions_status_active ON sessions (status)
    WHERE deleted_at IS NULL;

-- Index: time range queries for overlap detection.
CREATE INDEX sessions_time_range ON sessions (event_id, start_at, end_at)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE sessions IS
    'Time slots for an Event.  Each session carries independent inventory '
    'via capacity_total.  Lifecycle: draft → scheduled → completed|cancelled. '
    'Overlapping sessions for the same event are allowed but flagged. '
    'Feature #126 — Wave 3 Catalog.';

COMMENT ON COLUMN sessions.event_id IS
    'Owning event.  Immutable after creation.';

COMMENT ON COLUMN sessions.capacity_total IS
    'Total seat capacity for this session.  Must be positive (> 0). '
    'Any change triggers the capacity propagation hook at the application layer.';

COMMENT ON COLUMN sessions.status IS
    'Lifecycle status: draft, scheduled (default), cancelled, completed. '
    'Allowed transitions: draft→scheduled, draft→cancelled, '
    'scheduled→cancelled, scheduled→completed.';

COMMENT ON COLUMN sessions.deleted_at IS
    'Soft-delete timestamp.  NULL means the session is active.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for session management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('session.create', 'Create a new session within an event'),
    ('session.read',   'Read session details and list sessions for an event'),
    ('session.update', 'Update an existing session (times, capacity, status)'),
    ('session.delete', 'Soft-delete a session')
ON CONFLICT DO NOTHING;

-- Grant all session permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('session.create', 'session.read', 'session.update', 'session.delete')
ON CONFLICT DO NOTHING;

-- Grant all session permissions to org_admin (tenant admin manages own sessions).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('session.create', 'session.read', 'session.update', 'session.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('session.create', 'session.read', 'session.update', 'session.delete')
);
DELETE FROM permissions
WHERE name IN ('session.create', 'session.read', 'session.update', 'session.delete');
DROP TABLE IF EXISTS sessions;

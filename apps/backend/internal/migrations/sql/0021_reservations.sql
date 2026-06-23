-- +goose Up
-- =====================================================================
-- arena_new — Reservations state machine (Wave 5 — feature #131)
--
-- A reservation holds inventory for a buyer within a session (and optionally
-- a specific ticket tier). The state machine is:
--
--   draft → active → converted   (purchase confirmed)
--                  → expired     (TTL exceeded, worker marks this)
--                  → cancelled   (buyer or org cancelled)
--           ↓
--         cancelled  (draft can also be cancelled before activation)
--
-- TTL: expires_at is set at creation time from:
--   1. channel.reservation_ttl_override (per-channel override), OR
--   2. org.reservation_ttl_seconds      (per-org default), OR
--   3. 1200 seconds (20 min) — system-wide fallback
--
-- Concurrency: the TTL worker uses GET ... FOR UPDATE SKIP LOCKED so that
-- multiple worker instances never double-process the same reservation.
-- =====================================================================

CREATE TABLE reservations (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id          uuid        NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    channel_id      uuid        NOT NULL REFERENCES sales_channels(id) ON DELETE RESTRICT,
    session_id      uuid        NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
    tier_id         uuid        REFERENCES ticket_tiers(id) ON DELETE RESTRICT,  -- NULL = session-level GA
    user_id         uuid        REFERENCES users(id) ON DELETE SET NULL,
    quantity        integer     NOT NULL CHECK (quantity > 0),
    state           text        NOT NULL DEFAULT 'draft'
                                CHECK (state IN ('draft', 'active', 'converted', 'expired', 'cancelled')),
    expires_at      timestamptz NOT NULL,   -- absolute deadline; worker marks expired after this
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    cancelled_at    timestamptz,   -- set when state = 'cancelled'
    converted_at    timestamptz,   -- set when state = 'converted'
    expired_at      timestamptz    -- set when state = 'expired' (by TTL worker)
);

COMMENT ON TABLE reservations IS
    'Capacity holds representing buyer intent. State machine: draft → active → '
    'converted|expired|cancelled. Inventory is released when the reservation '
    'expires or is cancelled. Feature #131 — Wave 5 Inventory & Reservations.';

COMMENT ON COLUMN reservations.state IS
    'State machine state. draft = newly created; active = buyer confirmed intent; '
    'converted = purchase completed; expired = TTL exceeded (worker); cancelled = explicitly cancelled.';

COMMENT ON COLUMN reservations.tier_id IS
    'Owning ticket tier. NULL means this is a session-level GA reservation (no tier assigned).';

COMMENT ON COLUMN reservations.expires_at IS
    'Absolute expiry deadline. Derived from channel.reservation_ttl_override '
    'or org.reservation_ttl_seconds or the system default of 1200s.';

COMMENT ON COLUMN reservations.cancelled_at IS
    'Timestamp when the reservation transitioned to cancelled. NULL otherwise.';

COMMENT ON COLUMN reservations.converted_at IS
    'Timestamp when the reservation transitioned to converted (purchase complete). NULL otherwise.';

COMMENT ON COLUMN reservations.expired_at IS
    'Timestamp when the TTL worker marked the reservation as expired. NULL otherwise.';

-- Worker polling: find expirable reservations efficiently
CREATE INDEX reservations_expires_at_active ON reservations (expires_at)
    WHERE state IN ('draft', 'active');

-- User reservation history
CREATE INDEX reservations_user_id ON reservations (user_id)
    WHERE user_id IS NOT NULL;

-- Session inventory cross-reference
CREATE INDEX reservations_session_id ON reservations (session_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for reservation management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('reservation.create',   'Create a new reservation (holds inventory)'),
    ('reservation.read',     'Read reservation details'),
    ('reservation.activate', 'Transition a reservation from draft to active'),
    ('reservation.cancel',   'Cancel a reservation (releases inventory)')
ON CONFLICT DO NOTHING;

-- Grant all reservation permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('reservation.create', 'reservation.read', 'reservation.activate', 'reservation.cancel')
ON CONFLICT DO NOTHING;

-- Grant all reservation permissions to org_admin (manages own org's reservations).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('reservation.create', 'reservation.read', 'reservation.activate', 'reservation.cancel')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('reservation.create', 'reservation.read', 'reservation.activate', 'reservation.cancel')
);
DELETE FROM permissions
WHERE name IN ('reservation.create', 'reservation.read', 'reservation.activate', 'reservation.cancel');
DROP TABLE IF EXISTS reservations;

-- +goose Up
-- =====================================================================
-- arena_new — Inventory Ledger (Wave 5 — Inventory & Reservations, feature #130)
--
-- Tracks real-time capacity state for sessions and their tiers.
-- Implements the GA (General Availability) capacity model; seat-level
-- reservation (assigned seating) is deferred to a later milestone.
--
-- Ledger entry lifecycle:
--   1. INSERT  — created when a session (tier_id NULL) or tier is initialised
--   2. RESERVE — capacity_held += amount (SELECT FOR UPDATE guards invariant)
--   3. CONFIRM — capacity_held -= amount, capacity_sold += amount (purchase confirmed)
--   4. RELEASE — capacity_held -= amount (reservation cancelled / expired)
--
-- Invariant (enforced by CHECK + atomic SQL):
--   capacity_held >= 0
--   capacity_sold >= 0
--   capacity_held + capacity_sold <= capacity_total  (when total IS NOT NULL)
--
-- NULL capacity_total means unlimited availability; capacity checks are skipped.
--
-- Concurrency model:
--   ReserveCapacity / ReleaseCapacity / ConfirmCapacity all use a CTE with
--   SELECT ... FOR UPDATE to serialise concurrent operations on the same row.
--   The conditional UPDATE (WHERE invariant still holds) returns ErrNoRows on
--   over-capacity so the application layer can surface 409 Conflict.
--
-- Capacity propagation:
--   When sessions.capacity_total changes (PATCH .../sessions/{id}), the
--   application layer calls UpdateCapacityTotal to keep the ledger in sync.
--   The update is rejected if new_total < held + sold (invariant would break).
-- =====================================================================

CREATE TABLE inventory_ledger (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    session_id       uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tier_id          uuid        REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    capacity_total   integer,                            -- NULL = unlimited
    capacity_held    integer     NOT NULL DEFAULT 0,     -- reserved, not yet confirmed
    capacity_sold    integer     NOT NULL DEFAULT 0,     -- confirmed (sold)
    version          bigint      NOT NULL DEFAULT 0,     -- optimistic-lock counter
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    -- Non-negative invariant: counters cannot go below zero.
    CONSTRAINT inventory_ledger_nonnegative
        CHECK (capacity_held >= 0 AND capacity_sold >= 0),

    -- Capacity total must be positive when set.
    CONSTRAINT inventory_ledger_total_positive
        CHECK (capacity_total IS NULL OR capacity_total > 0),

    -- Core GA invariant: held + sold must never exceed total.
    -- Enforced at DB level as a safety net; app layer enforces it atomically
    -- via SELECT FOR UPDATE before each update.
    CONSTRAINT inventory_ledger_invariant
        CHECK (capacity_total IS NULL OR capacity_held + capacity_sold <= capacity_total)
);

-- One ledger row per (session, tier). tier_id = NULL represents the
-- session-level aggregate (no tier assigned). NULLS NOT DISTINCT treats
-- all NULL tier_ids as equal, enforcing a single session-level row.
CREATE UNIQUE INDEX inventory_ledger_session_tier_unique
    ON inventory_ledger (session_id, tier_id) NULLS NOT DISTINCT;

-- Fast lookup by tier_id for tier-scoped inventory queries.
CREATE INDEX inventory_ledger_tier_id
    ON inventory_ledger (tier_id)
    WHERE tier_id IS NOT NULL;

COMMENT ON TABLE inventory_ledger IS
    'Real-time GA capacity ledger for sessions and tiers. '
    'capacity_held = reserved but not confirmed; capacity_sold = confirmed. '
    'NULL capacity_total means unlimited availability. '
    'Atomic updates via SELECT FOR UPDATE. Feature #130 — Wave 5 Inventory.';

COMMENT ON COLUMN inventory_ledger.tier_id IS
    'Owning ticket tier. NULL means this is the session-level aggregate row '
    '(no tier assigned). Unique per session+tier pair (NULLS NOT DISTINCT).';

COMMENT ON COLUMN inventory_ledger.capacity_total IS
    'Total capacity ceiling. NULL means unlimited. '
    'Must be >= capacity_held + capacity_sold when set.';

COMMENT ON COLUMN inventory_ledger.capacity_held IS
    'Capacity currently reserved but not yet confirmed (in-flight reservations). '
    'Counts down from available capacity on reserve, up on release.';

COMMENT ON COLUMN inventory_ledger.capacity_sold IS
    'Capacity confirmed as sold. Incremented by ConfirmCapacity, '
    'never decremented (refunds handled via separate domain events).';

COMMENT ON COLUMN inventory_ledger.version IS
    'Monotonically incrementing counter. Incremented on every write; '
    'callers can detect concurrent modification by comparing versions.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for inventory management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('inventory.read',    'Read inventory ledger state for sessions and tiers'),
    ('inventory.reserve', 'Reserve capacity — decrement available seats'),
    ('inventory.release', 'Release held capacity back to available'),
    ('inventory.confirm', 'Confirm held capacity as sold (purchase completed)')
ON CONFLICT DO NOTHING;

-- Grant all inventory permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('inventory.read', 'inventory.reserve', 'inventory.release', 'inventory.confirm')
ON CONFLICT DO NOTHING;

-- Grant all inventory permissions to org_admin (manages own session inventory).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('inventory.read', 'inventory.reserve', 'inventory.release', 'inventory.confirm')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('inventory.read', 'inventory.reserve', 'inventory.release', 'inventory.confirm')
);
DELETE FROM permissions
WHERE name IN ('inventory.read', 'inventory.reserve', 'inventory.release', 'inventory.confirm');
DROP TABLE IF EXISTS inventory_ledger;

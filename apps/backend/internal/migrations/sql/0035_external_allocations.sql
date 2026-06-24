-- 0035_external_allocations.sql — External allocation quota model (feature #145).
--
-- External allocations represent quota blocks reserved for partner organisations
-- (distribution agents, resellers, box offices) who sell tickets outside the
-- platform. The platform reduces its own inventory when a quota is allocated;
-- the partner reports consumption at reconciliation time, and any unused quota
-- is returned to the platform inventory.
--
-- Status lifecycle:
--   pending   → allocation request created; inventory NOT yet held
--   active    → allocation confirmed; inventory IS held (ReserveCapacity called)
--   reconciled→ partner reported final consumption; inventory settled
--               (ConfirmCapacity for consumed + ReleaseCapacity for remainder)
--   disputed  → allocation is under dispute; inventory still held
--               (disputed → reconciled resolves via PATCH)
--
-- Invariant:
--   quota_consumed <= quota_qty (enforced by CHECK constraint)
--   Only active/disputed allocations hold platform inventory.

-- +goose Up

-- ── external_allocations ─────────────────────────────────────────────────────

CREATE TABLE external_allocations (
    id              uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    session_id      uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    partner_org_id  uuid        NOT NULL REFERENCES organizations(id),
    tier_id         uuid        REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    quota_qty       integer     NOT NULL,
    quota_consumed  integer     NOT NULL DEFAULT 0,
    status          text        NOT NULL DEFAULT 'pending',
    notes           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- quota_qty must be positive
    CONSTRAINT ext_alloc_qty_positive CHECK (quota_qty > 0),

    -- partner cannot consume more than the allocated quota
    CONSTRAINT ext_alloc_consumed_le_qty CHECK (quota_consumed >= 0 AND quota_consumed <= quota_qty),

    -- status must be one of the defined values
    CONSTRAINT ext_alloc_status_check CHECK (status IN ('pending', 'active', 'reconciled', 'disputed'))
);

-- Index for looking up allocations by session (common in inventory audit flows).
CREATE INDEX ext_alloc_session_id ON external_allocations (session_id);

-- Index for looking up all allocations for a partner org.
CREATE INDEX ext_alloc_partner_org_id ON external_allocations (partner_org_id);

-- Partial index for active allocations holding inventory (used by cleanup jobs).
CREATE INDEX ext_alloc_active ON external_allocations (session_id, tier_id)
    WHERE status IN ('active', 'disputed');

COMMENT ON TABLE external_allocations IS
    'Quota blocks reserved for partner organisations (resellers/agents). '
    'Creating an active allocation calls ReserveCapacity on the inventory_ledger. '
    'Reconciliation settles inventory: consumed→ConfirmCapacity, remainder→ReleaseCapacity. '
    'Feature #145 — Wave 10 External Allocations.';

COMMENT ON COLUMN external_allocations.quota_qty IS
    'Total quota allocated to the partner. Must be positive. '
    'Reducing inventory by this amount when status transitions to active.';

COMMENT ON COLUMN external_allocations.quota_consumed IS
    'Units actually sold/consumed by the partner. Reported via PATCH at reconciliation. '
    'Must be <= quota_qty. Default 0.';

COMMENT ON COLUMN external_allocations.status IS
    'Allocation lifecycle status. '
    'pending: created, inventory not held. '
    'active: confirmed, inventory held. '
    'reconciled: final consumption reported, inventory settled. '
    'disputed: under dispute, inventory still held.';

-- ── RBAC seeds ────────────────────────────────────────────────────────────────
--
-- allocation.read   — list/get external allocations for a session/org
-- allocation.create — create a new external allocation (reduces inventory)
-- allocation.update — update status or report consumption
--
-- Role grants:
--   admin     → all three permissions
--   org_admin → all three permissions (manages own org's allocations)

INSERT INTO permissions (name, description) VALUES
    ('allocation.read',   'Read external allocation quotas for a session or org (feature #145)'),
    ('allocation.create', 'Create an external allocation quota block (feature #145)'),
    ('allocation.update', 'Update allocation status or report consumption (feature #145)')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('allocation.read', 'allocation.create', 'allocation.update')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('allocation.read', 'allocation.create', 'allocation.update')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('allocation.read', 'allocation.create', 'allocation.update')
);

DELETE FROM permissions
WHERE name IN ('allocation.read', 'allocation.create', 'allocation.update');

DROP TABLE IF EXISTS external_allocations;

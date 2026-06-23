-- +goose Up
-- =====================================================================
-- arena_new — Ticket Tiers (Wave 3 — Catalog, feature #127)
--
-- A Ticket Tier defines a pricing option within a Session.  Each tier
-- carries a pricing_mode that governs how the buyer pays:
--
--   free  — no charge; price_amount must be 0.
--   fixed — a fixed price in cents; price_amount > 0.
--   pwyw  — pay-what-you-want; optional pwyw_min and pwyw_max bounds.
--           When both are present, pwyw_min <= pwyw_max is enforced.
--
-- Design decisions:
--   * price_amount is stored in the smallest currency unit (integer cents).
--   * pwyw_min / pwyw_max are nullable: NULL means "no bound".
--   * capacity NULL means unlimited availability.
--   * sale_window_start / sale_window_end are optional; NULL = no bound.
--   * sort_order controls display order within the session tier list.
--   * Soft-delete: deleted_at IS NULL for active tiers.
--   * DB-level CHECK constraints enforce pricing invariants; the
--     application layer re-validates before writing to give early errors.
-- =====================================================================

CREATE TABLE ticket_tiers (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    session_id          uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name                text        NOT NULL CHECK (length(trim(name)) > 0),
    pricing_mode        text        NOT NULL
                                    CHECK (pricing_mode IN ('fixed', 'free', 'pwyw')),
    price_amount        bigint      NOT NULL DEFAULT 0,    -- cents; 0 for free tiers
    currency            text        NOT NULL DEFAULT 'USD',-- ISO 4217
    pwyw_min            bigint,                            -- NULL = no minimum (pwyw only)
    pwyw_max            bigint,                            -- NULL = no maximum (pwyw only)
    capacity            integer,                           -- NULL = unlimited
    sale_window_start   timestamptz,                       -- NULL = on sale immediately
    sale_window_end     timestamptz,                       -- NULL = no expiry
    sort_order          integer     NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,                       -- NULL = active

    -- Pricing invariants
    CONSTRAINT ticket_tiers_pwyw_range        CHECK (pwyw_min IS NULL OR pwyw_max IS NULL OR pwyw_min <= pwyw_max),
    CONSTRAINT ticket_tiers_free_price        CHECK (pricing_mode != 'free' OR price_amount = 0),
    CONSTRAINT ticket_tiers_capacity_positive CHECK (capacity IS NULL OR capacity > 0),
    CONSTRAINT ticket_tiers_sale_window_order CHECK (sale_window_start IS NULL OR sale_window_end IS NULL OR sale_window_end > sale_window_start)
);

-- Index: list tiers for a session ordered by display sort.
CREATE INDEX ticket_tiers_session_id_active ON ticket_tiers (session_id, sort_order)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE ticket_tiers IS
    'Pricing tiers for a Session.  Supports fixed, free, and pay-what-you-want '
    '(pwyw) modes.  price_amount is stored in the smallest currency unit (cents). '
    'Feature #127 — Wave 3 Catalog.';

COMMENT ON COLUMN ticket_tiers.pricing_mode IS
    'Pricing mode: fixed (set price), free (no charge), pwyw (buyer chooses amount). '
    'Governs how price_amount, pwyw_min, and pwyw_max are interpreted.';

COMMENT ON COLUMN ticket_tiers.price_amount IS
    'Price in smallest currency unit (cents). Must be 0 for free tiers. '
    'For pwyw tiers this is the suggested amount (may be 0 if no suggestion).';

COMMENT ON COLUMN ticket_tiers.pwyw_min IS
    'Minimum amount the buyer may enter for a pwyw tier (cents). '
    'NULL means no minimum. When both pwyw_min and pwyw_max are set, '
    'pwyw_min <= pwyw_max is enforced by a CHECK constraint.';

COMMENT ON COLUMN ticket_tiers.pwyw_max IS
    'Maximum amount the buyer may enter for a pwyw tier (cents). '
    'NULL means no maximum.';

COMMENT ON COLUMN ticket_tiers.capacity IS
    'Maximum number of tickets available for this tier. NULL = unlimited.';

COMMENT ON COLUMN ticket_tiers.deleted_at IS
    'Soft-delete marker (timestamptz).  NULL means the tier is active.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for ticket tier management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('tier.create', 'Create a ticket tier for a session'),
    ('tier.read',   'Read ticket tier details and list tiers for a session'),
    ('tier.update', 'Update an existing ticket tier (name, pricing, capacity)'),
    ('tier.delete', 'Soft-delete a ticket tier')
ON CONFLICT DO NOTHING;

-- Grant all tier permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('tier.create', 'tier.read', 'tier.update', 'tier.delete')
ON CONFLICT DO NOTHING;

-- Grant all tier permissions to org_admin (tenant admin manages own tiers).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('tier.create', 'tier.read', 'tier.update', 'tier.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('tier.create', 'tier.read', 'tier.update', 'tier.delete')
);
DELETE FROM permissions
WHERE name IN ('tier.create', 'tier.read', 'tier.update', 'tier.delete');
DROP TABLE IF EXISTS ticket_tiers;

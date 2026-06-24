-- 0022_promo_codes.sql — promo code model + validation (feature #128)
--
-- Adds promo_codes and promo_code_redemptions tables, RBAC permission seeds
-- (promo.create, promo.read, promo.update, promo.delete, promo.validate),
-- and grants those permissions to admin and org_admin roles.

-- +goose Up

CREATE TABLE promo_codes (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id                uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    code                  text        NOT NULL CHECK (length(trim(code)) > 0),
    discount_type         text        NOT NULL CHECK (discount_type IN ('percent', 'fixed_amount')),
    discount_value        bigint      NOT NULL CHECK (discount_value > 0),
    -- For 'percent': value is 1–100 (percentage points). For 'fixed_amount': value is cents.
    applies_to_tier_ids   uuid[]      NOT NULL DEFAULT '{}',
    -- Empty array means applies to any tier. Non-empty means restricted to those tier IDs.
    max_uses              integer,    -- NULL = unlimited total uses
    max_uses_per_customer integer,    -- NULL = unlimited per customer
    valid_from            timestamptz,-- NULL = immediately valid
    valid_until           timestamptz,-- NULL = never expires
    min_order_amount      bigint      NOT NULL DEFAULT 0,  -- minimum order in cents (0 = no minimum)
    status                text        NOT NULL DEFAULT 'active'
                                      CHECK (status IN ('active', 'inactive', 'exhausted', 'expired')),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz,
    CONSTRAINT promo_codes_unique_per_org UNIQUE (org_id, code),
    CONSTRAINT promo_codes_percent_range
        CHECK (discount_type != 'percent' OR (discount_value >= 1 AND discount_value <= 100)),
    CONSTRAINT promo_codes_date_order
        CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_until > valid_from)
);

CREATE INDEX promo_codes_org_id_active ON promo_codes (org_id)
    WHERE deleted_at IS NULL;
CREATE INDEX promo_codes_code ON promo_codes (org_id, code)
    WHERE deleted_at IS NULL;

CREATE TABLE promo_code_redemptions (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    promo_code_id   uuid        NOT NULL REFERENCES promo_codes(id) ON DELETE RESTRICT,
    user_id         uuid        REFERENCES users(id) ON DELETE SET NULL,
    reservation_id  uuid        REFERENCES reservations(id) ON DELETE SET NULL,
    redeemed_at     timestamptz NOT NULL DEFAULT now(),
    discount_amount bigint      NOT NULL CHECK (discount_amount >= 0),
    order_amount    bigint      NOT NULL CHECK (order_amount >= 0)
);

CREATE INDEX promo_code_redemptions_code_id ON promo_code_redemptions (promo_code_id);
CREATE INDEX promo_code_redemptions_user_id ON promo_code_redemptions (user_id)
    WHERE user_id IS NOT NULL;

-- ── RBAC permission seeds ─────────────────────────────────────────────────────
-- Insert permissions for promo code management and validation.
-- Duplicate names are silently skipped via ON CONFLICT DO NOTHING.

INSERT INTO permissions (name, description) VALUES
    ('promo.create',   'Create promo codes for an organization'),
    ('promo.read',     'Read/list promo codes for an organization'),
    ('promo.update',   'Update promo code attributes'),
    ('promo.delete',   'Soft-delete a promo code'),
    ('promo.validate', 'Validate and compute discount for a promo code at checkout')
ON CONFLICT (name) DO NOTHING;

-- Grant all promo permissions to admin and org_admin roles.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('admin', 'org_admin')
  AND p.name IN ('promo.create','promo.read','promo.update','promo.delete','promo.validate')
ON CONFLICT DO NOTHING;

-- +goose Down

-- Revoke role_permissions rows inserted by this migration.
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('promo.create','promo.read','promo.update','promo.delete','promo.validate')
);

-- Remove permissions inserted by this migration.
DELETE FROM permissions
WHERE name IN ('promo.create','promo.read','promo.update','promo.delete','promo.validate');

-- Drop tables in reverse dependency order.
DROP TABLE IF EXISTS promo_code_redemptions;
DROP TABLE IF EXISTS promo_codes;

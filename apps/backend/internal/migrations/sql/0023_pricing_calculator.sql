-- 0023_pricing_calculator.sql — RBAC seeds for the pricing calculator (feature #129).
--
-- The pricing calculator does not require a new table. All price computation is
-- performed in Go using data already in ticket_tiers and promo_codes. This
-- migration only adds the pricing.quote permission and grants it to the roles
-- that are expected to perform pre-checkout price queries.
--
-- Roles granted:
--   admin     — full platform access
--   org_admin — can quote prices for their own events
--   member    — any authenticated user can get a quote

-- +goose Up

-- Insert the pricing.quote permission if it doesn't exist.
INSERT INTO permissions (name, description)
VALUES ('pricing.quote', 'Compute an all-in price quote for a ticket tier (feature #129)')
ON CONFLICT (name) DO NOTHING;

-- Grant pricing.quote to admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name = 'pricing.quote'
ON CONFLICT DO NOTHING;

-- Grant pricing.quote to org_admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name = 'pricing.quote'
ON CONFLICT DO NOTHING;

-- Grant pricing.quote to member role (any authenticated member can request a quote).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name = 'pricing.quote'
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE name = 'pricing.quote');

DELETE FROM permissions WHERE name = 'pricing.quote';

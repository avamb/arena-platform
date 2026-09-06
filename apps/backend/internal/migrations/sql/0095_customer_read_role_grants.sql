-- +goose Up
-- =====================================================================
-- arena_new — Customer read role grants (feature #482, W1-A4d)
--
-- 0091_customers.sql seeded the `customer.read` / `customer.import`
-- permissions but granted them to no role, so the org-scoped customer
-- read surface (GET /v1/organizations/{org_id}/customers[/{id}]) was
-- unreachable by any tenant actor even though the permission existed.
-- Mirrors the grant pattern used for reservation.read / ticket.update /
-- org.read (0011_memberships.sql): platform admin, org_admin, and the two
-- membership-table org roles (organizer, agent) all get customer.read so a
-- real tenant membership (not just the global org_admin user_roles path)
-- can reach the read surface. customer.import (platform-level bulk import)
-- is admin-only.
-- =====================================================================

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('customer.read', 'customer.import')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name IN ('org_admin', 'organizer', 'agent')
  AND  p.name = 'customer.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('customer.read', 'customer.import')
)
AND role_id IN (
    SELECT id FROM roles WHERE name IN ('admin', 'org_admin', 'organizer', 'agent')
);

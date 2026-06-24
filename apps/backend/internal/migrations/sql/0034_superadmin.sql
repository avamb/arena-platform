-- +goose Up
-- =====================================================================
-- arena_new — Platform superadmin role and permission (feature #166)
--
-- Adds the platform_superadmin role and superadmin.read permission so that
-- cross-tenant read-only endpoints (/v1/admin/*) can be gated to a
-- dedicated high-trust role separate from the general 'admin'.
-- =====================================================================

INSERT INTO roles (name, description) VALUES
    ('platform_superadmin',
     'Platform-level superadmin with read-only cross-tenant access to all organizations, orders, tickets, and refunds')
ON CONFLICT DO NOTHING;

INSERT INTO permissions (name, description) VALUES
    ('superadmin.read',
     'Read-only cross-tenant access to all organizations, orders (checkout sessions), tickets, and refunds via /v1/admin/* endpoints')
ON CONFLICT DO NOTHING;

-- admin role gets superadmin.read (admin inherits all permissions)
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name = 'superadmin.read'
ON CONFLICT DO NOTHING;

-- platform_superadmin role gets superadmin.read
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'platform_superadmin'
  AND  p.name = 'superadmin.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'superadmin.read');
DELETE FROM permissions WHERE name = 'superadmin.read';
DELETE FROM roles WHERE name = 'platform_superadmin';

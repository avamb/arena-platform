-- +goose Up
-- =====================================================================
-- arena_new - SuperAdmin user provisioning permission grant
--
-- The POST /v1/admin/users provisioning endpoint is gated by
-- membership.grant because it creates either an organization membership or a
-- global platform role assignment. The platform_superadmin role needs that
-- existing capability to perform the documented "create user + assign role"
-- workflow from the SuperAdmin console.
-- =====================================================================

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'platform_superadmin'
  AND  p.name = 'membership.grant'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE role_id IN (SELECT id FROM roles WHERE name = 'platform_superadmin')
  AND permission_id IN (SELECT id FROM permissions WHERE name = 'membership.grant');

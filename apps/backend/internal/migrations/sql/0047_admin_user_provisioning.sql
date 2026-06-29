-- +goose Up
-- =====================================================================
-- arena_new - SuperAdmin user provisioning permission grant
--
-- SuperAdmin user provisioning is exposed through the SuperAdmin console and
-- gated by superadmin.read at the route level. The same workflow may also
-- need membership.grant for organization membership management surfaces, so
-- platform_superadmin receives the grant explicitly.
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

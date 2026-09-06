-- 0097_api_key_role_grants.sql — W1-C1c (feature #514, spec §13.1).
--
-- 0096_api_keys.sql seeded the `api_key.manage` and `import.bil24_session`
-- permissions but granted them to no role via role_permissions, so the
-- org-scoped API-key management surface (GET/POST/DELETE
-- /v1/organizations/{org_id}/api-keys[/{id}]) was unreachable by any tenant
-- actor. Mirrors the grant pattern used for customer.read (0095): org_admin
-- gets both permissions so an org administrator can issue and revoke
-- service credentials and (transitively, since api_key scopes are checked
-- independently) import Bil24 sessions.

-- +goose Up
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('api_key.manage', 'import.bil24_session')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('api_key.manage', 'import.bil24_session')
)
AND role_id IN (
    SELECT id FROM roles WHERE name = 'org_admin'
);

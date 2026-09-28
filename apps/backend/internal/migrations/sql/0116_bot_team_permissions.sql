-- 0116_bot_team_permissions.sql — the organization OWNER (org_admin) manages
-- the team from the Telegram event-center bot (spec 08_architecture/28 §3.2,
-- §10 step 5): invite an employee by e-mail (POST .../bot-invitations needs
-- membership.grant) and remove one (DELETE .../members/{user_id} needs
-- membership.revoke). A manager (organizer) keeps membership.read only.
--
-- No permission is seeded here (both exist since the IAM migrations and the
-- platform superadmin already holds them), so the 532 parity guard has
-- nothing to check.

-- +goose Up

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('membership.grant', 'membership.revoke')
WHERE  r.name = 'org_admin'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions rp
USING  roles r, permissions p
WHERE  rp.role_id = r.id
  AND  rp.permission_id = p.id
  AND  r.name = 'org_admin'
  AND  r.org_id IS NULL
  AND  p.name IN ('membership.grant', 'membership.revoke');

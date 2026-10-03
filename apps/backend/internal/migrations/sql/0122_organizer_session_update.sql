-- 0122_organizer_session_update.sql — a manager (membership role organizer)
-- moves and cancels a session from the Telegram event-center bot's "Sessions"
-- screens (spec 08_architecture/30): both are a PATCH of the EXISTING session,
-- which needs session.update. Migration 0115 gave the manager session.read but
-- not session.update, so the bot's last step answered 403 for every manager
-- (the owner, org_admin, already holds it). The write is as safe as the edit
-- wizard the manager already has: sessionchange.Apply refuses a change that
-- would leave buyers unwritten.
--
-- No permission is seeded here (session.update exists since the IAM
-- migrations and the platform superadmin already holds it), so the 532 parity
-- guard has nothing to check.

-- +goose Up

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'session.update'
WHERE  r.name = 'organizer'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions rp
USING  roles r, permissions p
WHERE  rp.role_id = r.id
  AND  rp.permission_id = p.id
  AND  r.name = 'organizer'
  AND  r.org_id IS NULL
  AND  p.name = 'session.update';

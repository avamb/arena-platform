-- 0140_org_admin_webhook_subscriber_manage.sql — the owner (membership role
-- org_admin) no longer holds webhook.subscriber.manage.
--
-- Migration 0040 granted the permission to 'admin' and 'org_admin'. The
-- webhook subscriber registry is one table for every organization (a
-- subscriber row names no organization) and its POST hands out a signing
-- secret, so the routes have been the platform superadmin's alone since the
-- gate in mountWebhookSubscriberRoutes. The grant to org_admin was left
-- behind: /v1/me still listed the permission for every owner and admin-web
-- showed them a Webhooks page that answered 403. The route gate stays as
-- the real check; this only removes the misleading grant.
--
-- 'admin' and platform_superadmin keep the permission. No permission is
-- seeded, so the 532 parity guard has nothing to check.

-- +goose Up

DELETE FROM role_permissions rp
USING  roles r, permissions p
WHERE  rp.role_id = r.id
  AND  rp.permission_id = p.id
  AND  r.name = 'org_admin'
  AND  p.name = 'webhook.subscriber.manage';

-- +goose Down

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'webhook.subscriber.manage'
WHERE  r.name = 'org_admin'
ON CONFLICT DO NOTHING;

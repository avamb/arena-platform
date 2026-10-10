-- 0131_org_admin_order_write.sql — the owner (membership role org_admin) can
-- cancel an unpaid order. Migration 0129 handed the manager the whole
-- operational event center and said "the owner already holds all of these",
-- but of the sixteen permissions it lists only order.write was never granted
-- to org_admin (order.read always was): an owner pressing "Cancel unpaid
-- order" in the Telegram bot, or calling POST .../orders/{id}/cancel, got
-- 403 while their assistant could do it. Found by the bot's orders e2e
-- (spec 08_architecture/35, EC-04 and EC-05).
--
-- No permission is seeded here (order.write exists since the orders surface
-- of feature #489 and the platform superadmin holds it), so the 532 parity
-- guard has nothing to check.

-- +goose Up

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'order.write'
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
  AND  p.name = 'order.write';

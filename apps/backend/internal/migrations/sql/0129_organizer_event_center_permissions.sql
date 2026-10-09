-- 0129_organizer_event_center_permissions.sql — the manager (membership role
-- organizer) runs the whole operational event center from the Telegram bot
-- (spec 08_architecture/35 §3, EC-17; owner decision 2026-10-09: the owner
-- usually hands the routine to an assistant, so the assistant must be able to
-- do everything operational, refunds included). The owner (org_admin) already
-- holds all of these; what stays with the owner alone is the team
-- (membership.grant/revoke), payment configuration, billing, API keys, KYB
-- and the organization itself.
--
-- Granted here: cancelling an unpaid order, cancelling a ticket, resending
-- tickets, the refund flow, promo codes, complimentary tickets, the scans
-- panel, event reports and category edits (price, sale window, closing).
--
-- No permission is seeded here — every one of them exists since the IAM
-- migrations (0019, 0022, 0026, 0028, 0032, 0036, 0054, 0055, 0092) and the
-- platform superadmin holds them through the 0100 catch-up grant — so the 532
-- parity guard has nothing to check. The one permission that must NOT be
-- here is refund.* for the agent role: agents sell, they do not refund.

-- +goose Up

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN (
           'order.write',
           'ticket.cancel', 'ticket.update',
           'refund.create', 'refund.read', 'refund.approve',
           'promo.read', 'promo.create', 'promo.update', 'promo.delete',
           'complimentary.issue', 'complimentary.read',
           'scan_event.read',
           'report.read', 'report.generate',
           'tier.update'
       )
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
  AND  p.name IN (
           'order.write',
           'ticket.cancel', 'ticket.update',
           'refund.create', 'refund.read', 'refund.approve',
           'promo.read', 'promo.create', 'promo.update', 'promo.delete',
           'complimentary.issue', 'complimentary.read',
           'scan_event.read',
           'report.read', 'report.generate',
           'tier.update'
       );

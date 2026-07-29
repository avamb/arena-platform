-- +goose Up
-- =====================================================================
-- arena_new - platform_superadmin receives every seeded permission
--
-- Background (admin bootstrap walkthrough, 2026-07-29 / backlog AB-14):
-- every permission-seeding migration granted its new permissions to the
-- admin / org_admin / organizer roles but almost never to
-- platform_superadmin (0034). On a fresh database the very first
-- superadmin bootstrap actions therefore failed with 403:
-- org.create (0009), media.write (0053), membership.read (0011),
-- venue.read, channel.read, and others.
--
-- platform_superadmin is defined as the unrestricted platform operator
-- role, so it receives the full permission catalogue. The CROSS JOIN is
-- idempotent (ON CONFLICT DO NOTHING) and intentionally repeatable.
--
-- NOTE for future permission migrations: seed the new permission AND
-- grant it to platform_superadmin (or re-run a grant like the one below)
-- in the same migration. See 09_autoforge/admin_bootstrap_backlog.md
-- (AB-14) for the CI-check follow-up that enforces this.
-- =====================================================================

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
CROSS  JOIN permissions p
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

-- +goose Down
-- Restore the pre-0071 curated grant set: keep only the permissions that
-- earlier migrations explicitly granted to platform_superadmin and drop
-- the rest. The explicit list mirrors grants from 0034 (superadmin.read),
-- 0042/0044 (network.*), 0046 (payment_config.read), 0047
-- (membership.grant) and the org/media grants applied operationally.
DELETE FROM role_permissions rp
USING  roles r, permissions p
WHERE  rp.role_id = r.id
  AND  rp.permission_id = p.id
  AND  r.name = 'platform_superadmin'
  AND  p.name NOT IN (
        'superadmin.read',
        'membership.grant',
        'payment_config.read',
        'network.read', 'network.create', 'network.update', 'network.archive',
        'network.manage_users', 'network.manage_organizers', 'network.manage_agents',
        'network.manage_channels', 'network.view_sales', 'network.view_reports',
        'network.view_audit', 'network.support_orders', 'network.support_refunds',
        'network.support_tickets'
      );

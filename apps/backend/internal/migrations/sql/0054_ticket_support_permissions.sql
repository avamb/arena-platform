-- +goose Up
-- =====================================================================
-- arena_new — Ticket support permissions (Wave T — feature #291, T-4)
--
-- Seeds two permissions required by the support console ticket-detail
-- "Delivery" section:
--
--   * ticket.update  — Mutate ticket lifecycle from the support console
--                      (e.g. trigger a delivery resend, change holder email).
--   * support.act    — Catch-all for support-console actions that aren't
--                      tied to a specific business permission. Grants the
--                      ticket-delivery resend action when the actor lacks
--                      ticket.update but is on the support role.
--
-- Granted to:
--   * admin         (platform superadmin)
--   * org_admin     (tenant admin)
--   * support       (dedicated support role; created here if absent so
--                    the migration is idempotent)
-- =====================================================================

INSERT INTO permissions (name, description) VALUES
    ('ticket.update', 'Update ticket lifecycle from the support console (resend delivery, etc.)'),
    ('support.act',   'Generic support-console action permission for read-only operators with limited write scope')
ON CONFLICT DO NOTHING;

-- Ensure the support role exists so we have something to grant `support.act` to.
INSERT INTO roles (name, description) VALUES
    ('support', 'Support operator role: read-only access plus narrow action grants (delivery resend, etc.)')
ON CONFLICT DO NOTHING;

-- Grant both to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('ticket.update', 'support.act')
ON CONFLICT DO NOTHING;

-- Grant both to org_admin (tenant admin).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('ticket.update', 'support.act')
ON CONFLICT DO NOTHING;

-- Grant support.act to the support role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'support'
  AND  p.name IN ('support.act')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('ticket.update', 'support.act')
);
DELETE FROM permissions
WHERE name IN ('ticket.update', 'support.act');
-- We do not drop the 'support' role on rollback because other migrations
-- may have referenced it; permission_id deletes above already revoke the
-- two new grants from it.

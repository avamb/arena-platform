-- +goose Up
-- =====================================================================
-- arena_new — network.* permissions for Operator Network (feature #206)
--
-- Registers the operator-network permission set on top of the RBAC
-- engine introduced in 0008_rbac.sql. Binds the full set to the
-- platform_superadmin role (seeded in 0034_superadmin.sql) and the
-- operational subset to the network_operator role (seeded in
-- 0042_network_operator_role.sql).
--
-- See the design note at
--   09_autoforge/admin_ui/operator_network_design_note.md  (feature #202)
-- for the rationale behind each permission and which surfaces consume
-- it (admin UI, middleware choke points, public APIs).
--
-- Permission catalogue (14):
--   network.read                 — view network + roster + memberships
--   network.create               — create new operator network
--   network.update               — rename / change metadata of a network
--   network.archive              — soft-archive a network
--   network.manage_users         — add/remove network_operator users
--   network.manage_organizers    — attach/detach organizer organizations
--   network.manage_agents        — attach/detach agent organizations
--   network.manage_channels      — configure sales channels for the network
--   network.view_sales           — read aggregated sales for the network
--   network.support_orders       — open support views for orders in the network
--   network.support_tickets      — open support views for tickets in the network
--   network.support_refunds      — open support views for refunds in the network
--   network.view_reports         — read network-scoped reports
--   network.view_audit           — read network-scoped audit events
--
-- Role bindings:
--   platform_superadmin → ALL 14 network.* permissions.
--   network_operator    → operational subset (no create / archive /
--                         manage_users): read, update, manage_organizers,
--                         manage_agents, manage_channels, view_sales,
--                         support_orders, support_tickets, support_refunds,
--                         view_reports, view_audit  (11 permissions).
--   platform_operator   → NO bindings (behavior intentionally preserved).
--   admin               → inherits everything via the 0008 broad-grant seed
--                         pattern; this migration also explicitly attaches
--                         the network.* set to admin so future re-seeds
--                         remain idempotent.
-- =====================================================================

-- ---------------------------------------------------------------------
-- 1. Seed the permission catalogue (idempotent).
-- ---------------------------------------------------------------------

INSERT INTO permissions (name, description) VALUES
    ('network.read',
     'View an operator network, its roster, and its memberships.'),
    ('network.create',
     'Create a new operator network.'),
    ('network.update',
     'Update mutable metadata of an operator network (name, contact, etc.).'),
    ('network.archive',
     'Soft-archive an operator network.'),
    ('network.manage_users',
     'Add or remove network_operator users on an operator network.'),
    ('network.manage_organizers',
     'Attach or detach organizer organizations to/from an operator network.'),
    ('network.manage_agents',
     'Attach or detach agent organizations to/from an operator network.'),
    ('network.manage_channels',
     'Configure sales channels belonging to an operator network.'),
    ('network.view_sales',
     'Read aggregated sales information scoped to an operator network.'),
    ('network.support_orders',
     'Read order/checkout support views scoped to an operator network.'),
    ('network.support_tickets',
     'Read ticket support views scoped to an operator network.'),
    ('network.support_refunds',
     'Read refund support views scoped to an operator network.'),
    ('network.view_reports',
     'Read reports scoped to an operator network.'),
    ('network.view_audit',
     'Read audit events scoped to an operator network.')
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------
-- 2. Bind all 14 network.* permissions to platform_superadmin.
--    platform_superadmin is the cross-tenant high-trust role (see
--    migration 0034_superadmin.sql). It receives the full set including
--    create / archive / manage_users.
-- ---------------------------------------------------------------------

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'platform_superadmin'
  AND  r.org_id IS NULL
  AND  p.name LIKE 'network.%'
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------
-- 3. Bind the operational subset to network_operator.
--    The network_operator runs day-to-day operations against an existing
--    network; lifecycle management of the network entity itself
--    (create / archive) and changes to its administrative roster
--    (manage_users) are platform_superadmin-only.
-- ---------------------------------------------------------------------

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'network_operator'
  AND  r.org_id IS NULL
  AND  p.name IN (
      'network.read',
      'network.update',
      'network.manage_organizers',
      'network.manage_agents',
      'network.manage_channels',
      'network.view_sales',
      'network.support_orders',
      'network.support_tickets',
      'network.support_refunds',
      'network.view_reports',
      'network.view_audit'
  )
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------
-- 4. The legacy 'admin' role gets every permission per the 0008 seed.
--    Repeat the broad grant for the new network.* rows so a re-seed of
--    just this migration keeps 'admin' consistent.
-- ---------------------------------------------------------------------

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  r.org_id IS NULL
  AND  p.name LIKE 'network.%'
ON CONFLICT DO NOTHING;

-- Intentionally NO grants for platform_operator: that role's existing
-- permission surface is preserved unchanged by feature #206.

-- +goose Down
-- Remove the role bindings first (FK cascade would also clean these
-- up when the permission rows are deleted, but explicit DELETE keeps
-- the rollback obvious in audit logs).
DELETE FROM role_permissions
WHERE  permission_id IN (
    SELECT id FROM permissions WHERE name LIKE 'network.%'
);

DELETE FROM permissions WHERE name LIKE 'network.%';

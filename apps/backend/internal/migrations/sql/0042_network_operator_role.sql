-- +goose Up
-- =====================================================================
-- arena_new — network_operator role (feature #203)
--
-- Introduces the `network_operator` role on top of the existing RBAC
-- scaffolding (see migrations 0008_rbac.sql, 0011_memberships.sql,
-- 0034_superadmin.sql). The role is distinct from `platform_operator`
-- (internal Arena staff) — it represents an external operator that
-- runs its own carrier organization and books capacity across a
-- network of organizer/agent organizations. The wider operator-network
-- domain (operator_networks, operator_network_organizations, network.*
-- permissions, dedicated middleware) is intentionally OUT OF SCOPE for
-- this migration; it will be layered on by a follow-up feature. See
-- 09_autoforge/admin_ui/operator_network_design_note.md (feature #202)
-- for the full design.
--
-- This migration MUST NOT weaken the semantics of `platform_superadmin`
-- or `platform_operator` — it only extends the membership role
-- vocabulary by one additional value.
-- =====================================================================

-- ---------------------------------------------------------------------
-- 1. Extend the memberships.role CHECK constraint so the new role is
--    a legal value for memberships.role. We drop the constraint added
--    in 0011_memberships.sql and recreate it with the new entry; the
--    five pre-existing values are preserved verbatim so existing
--    memberships keep validating.
-- ---------------------------------------------------------------------

ALTER TABLE memberships
    DROP CONSTRAINT memberships_role_check;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_role_check CHECK (role IN (
        'organizer',
        'agent',
        'platform_operator',
        'external_ticketing_operator',
        'platform_superadmin',
        'network_operator'
    ));

-- ---------------------------------------------------------------------
-- 2. Seed the new role into the roles table as a *global* role
--    (org_id NULL) so the RBAC permission resolver can attach
--    permissions to it in subsequent migrations. The role is created
--    with no permissions of its own here; permission seeds will land
--    with the network.* migration in a later feature.
--
--    Distinct row from `platform_operator` (different name => different
--    roles.id => different permission set). No update or deletion of
--    `platform_operator` or `platform_superadmin` is performed.
-- ---------------------------------------------------------------------

INSERT INTO roles (name, description) VALUES
    ('network_operator',
     'External operator that runs a carrier organization and coordinates capacity '
     'across a network of organizer/agent organizations. Distinct from '
     'platform_operator (internal Arena staff).')
ON CONFLICT DO NOTHING;

-- +goose Down
-- Remove the seeded role (NO-OP if not present; ON DELETE CASCADE in
-- user_roles and role_permissions handles dangling joins, but the
-- expectation is that no permissions or assignments exist for this role
-- at the moment of rollback because they are introduced by later
-- migrations).
DELETE FROM roles WHERE name = 'network_operator';

-- Restore the pre-#203 CHECK constraint shape.
ALTER TABLE memberships
    DROP CONSTRAINT memberships_role_check;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_role_check CHECK (role IN (
        'organizer',
        'agent',
        'platform_operator',
        'external_ticketing_operator',
        'platform_superadmin'
    ));

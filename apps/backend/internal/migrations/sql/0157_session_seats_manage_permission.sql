-- 0157_session_seats_manage_permission.sql — the dedicated permission for
-- managing the places of a session (task S1f, spec 08_architecture/26 §3.3
-- and 40 §5.S).
--
-- Until now the seat routes (block and unblock places, move places between
-- price categories, read the admin seat inventory) were gated by
-- event_session.assign_seating_plan, the permission of BINDING a plan to a
-- session. Those are two different jobs: binding a plan is a setup step,
-- while moving a place to another category is routine work the manager does
-- every day, and the token of a WordPress host needs only the second one.
--
-- The new permission session.seats.manage is granted to
--   * org_admin (the owner) and organizer (the manager)
--   * admin, the old platform admin role
--   * platform_superadmin (same migration as the seed, the 532 parity guard)
--   * every other role that holds event_session.assign_seating_plan today,
--     so no one who could move places yesterday loses the right.
-- The agent role gets nothing: agents sell, they do not rearrange the hall.
-- Binding a plan keeps event_session.assign_seating_plan.

-- +goose Up
SET LOCAL lock_timeout = '5s';

INSERT INTO permissions (name, description) VALUES
    ('session.seats.manage', 'Block, unblock and re-categorise the places of a session and read its admin seat inventory')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'session.seats.manage'
WHERE  r.name IN ('org_admin', 'organizer', 'admin')
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'session.seats.manage'
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT rp.role_id, p_new.id
FROM   role_permissions rp
JOIN   permissions p_old ON p_old.id = rp.permission_id
                        AND p_old.name = 'event_session.assign_seating_plan'
JOIN   permissions p_new ON p_new.name = 'session.seats.manage'
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'session.seats.manage');

DELETE FROM permissions WHERE name = 'session.seats.manage';

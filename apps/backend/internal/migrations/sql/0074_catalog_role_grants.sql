-- +goose Up
-- =====================================================================
-- arena_new — Catalog RBAC role matrix (Admin Bootstrap AB-1 / #406)
--
-- Permission seed rule: every new permission must document its recipients,
-- including platform_superadmin.  0071 grants the latter the complete
-- catalogue.  This migration audits the existing catalog permissions for the
-- remaining tenant roles and fills the gaps exposed by the admin UI.
--
-- Role matrix:
--   org_admin  : all venue.*, channel.*, event.*, payment_config.*, media.*
--                (already seeded by the owning feature migrations)
--   organizer  : manages events and their media, reads venue/channel choices
--   agent      : read-only catalog/media access; never configures payments or
--                publishes/changes the organizer's events
--
-- All actions remain constrained by the org-membership and resource-owner
-- gates in the HTTP layer; role grants never make a caller cross-tenant.
-- =====================================================================

-- Organizers operate the event lifecycle and upload its presentation assets.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'organizer'
  AND r.org_id IS NULL
  AND p.name IN (
      'venue.read', 'channel.read',
      'event.create', 'event.read', 'event.update', 'event.delete', 'event.publish',
      'media.write', 'media.read', 'media.delete'
  )
ON CONFLICT DO NOTHING;

-- Agents need event/catalog context to sell tickets, but stay read-only.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'agent'
  AND r.org_id IS NULL
  AND p.name IN ('venue.read', 'channel.read', 'event.read', 'media.read')
ON CONFLICT DO NOTHING;

-- +goose Down
-- These grants are unique to this migration.  Do not remove org_admin or
-- platform_superadmin grants: they are intentionally seeded elsewhere.
DELETE FROM role_permissions rp
USING roles r, permissions p
WHERE rp.role_id = r.id
  AND rp.permission_id = p.id
  AND r.org_id IS NULL
  AND (
      (r.name = 'organizer' AND p.name IN (
          'venue.read', 'channel.read',
          'event.create', 'event.read', 'event.update', 'event.delete', 'event.publish',
          'media.write', 'media.read', 'media.delete'
      ))
      OR
      (r.name = 'agent' AND p.name IN ('venue.read', 'channel.read', 'event.read', 'media.read'))
  );

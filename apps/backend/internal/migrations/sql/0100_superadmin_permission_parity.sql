-- +goose Up
-- =====================================================================
-- arena_new — platform_superadmin permission parity (feature #532, W1-S0b)
--
-- Background (08_architecture/21_superadmin_org_access_parity_ru.md §2,
-- found during live superadmin verification on head 72527a3): migration
-- 0071 granted platform_superadmin every permission that existed AT THAT
-- TIME (a one-shot CROSS JOIN, not a standing trigger). Every permission
-- seeded afterwards — order.read / order.write (0092), customer.read /
-- customer.import (0091, granted only to admin/org_admin/organizer/agent
-- by 0095), api_key.manage / import.bil24_session (0096, granted only to
-- org_admin by 0097) — was never granted to platform_superadmin. A real
-- superadmin therefore got 403 permissions.denied on the API-keys,
-- customers and orders admin surfaces even though /v1/me reported the
-- platform_superadmin role.
--
-- This migration re-runs the same idempotent CROSS JOIN as 0071 against
-- the CURRENT permission catalogue, closing the gap for every permission
-- that exists today. Being a straight "grant everything" CROSS JOIN with
-- ON CONFLICT DO NOTHING, it is safe to re-run and naturally covers any
-- permission seeded between 0072 and 0099 without needing to enumerate
-- them by name.
--
-- NOTE for future permission migrations (see also 0071's note, and the
-- AGENTS.md rule this feature adds): grant every newly seeded permission
-- to platform_superadmin in the SAME migration that inserts it, or
-- TestSuperadminPermissionParity532 (and the companion static guardrail
-- over migrations numbered > 0100) will fail.
-- =====================================================================

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
CROSS  JOIN permissions p
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

-- +goose Down
-- Revoke exactly the six permissions this migration is known to have
-- closed the gap for (spec §2's inventory of the 0099 drift). Any other
-- permission platform_superadmin already held before this migration ran
-- (via 0071 or an explicit per-role grant migration) is left untouched.
DELETE FROM role_permissions rp
USING  roles r, permissions p
WHERE  rp.role_id = r.id
  AND  rp.permission_id = p.id
  AND  r.name = 'platform_superadmin'
  AND  p.name IN (
        'order.read', 'order.write',
        'customer.read', 'customer.import',
        'api_key.manage', 'import.bil24_session'
      );

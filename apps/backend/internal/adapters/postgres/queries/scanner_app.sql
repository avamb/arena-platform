-- Scanner app: controller passes, devices (migration 0227, spec 69 MA-10).
-- The hand-maintained wrappers live in gen/scanner_app.sql.go; the package
-- that uses them is internal/platform/scannerapp.
--
-- The organization of a device always comes from the device row. Every read
-- of a pass's dates re-joins sessions -> events and keeps only the rows of
-- the device's organization.

-- ── passes ──────────────────────────────────────────────────────────────────

-- name: CountScannerSessionsOfOrg :one
SELECT count(DISTINCT s.id)
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  s.id = ANY($2::uuid[])
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- name: CountScannerTiersOfOrg :one
SELECT count(DISTINCT (x.tier_id, x.session_id))
FROM   unnest($2::uuid[], $3::uuid[]) AS x(tier_id, session_id)
JOIN   ticket_tiers t ON t.id = x.tier_id AND t.session_id = x.session_id
JOIN   sessions s     ON s.id = t.session_id
JOIN   events e       ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  t.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- name: InsertScannerPass :one
INSERT INTO scanner_passes (org_id, label, kind, valid_from, valid_until, max_devices, created_by_user_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: InsertScannerPassSession :exec
INSERT INTO scanner_pass_sessions (pass_id, session_id, all_tiers) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: InsertScannerPassTier :exec
INSERT INTO scanner_pass_tiers (pass_id, session_id, tier_id) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: LockScannerPass :one
SELECT id, kind, valid_until, max_devices, revoked_at
FROM   scanner_passes
WHERE  id = $1 AND org_id = $2
FOR UPDATE;

-- name: CountLiveScannerDevicesOfPass :one
SELECT count(*) FROM scanner_devices WHERE pass_id = $1 AND revoked_at IS NULL;

-- name: RevokeScannerPass :execrows
UPDATE scanner_passes
SET    revoked_at = $3, revoked_by_user_id = $4
WHERE  id = $1 AND org_id = $2 AND revoked_at IS NULL;

-- name: GetScannerPassOfOrg :one
SELECT id, label, kind, valid_from, valid_until
FROM   scanner_passes
WHERE  id = $1 AND org_id = $2;

-- name: ListScannerPassSessions :many
SELECT ps.session_id, s.event_id, ps.all_tiers,
       COALESCE(array_agg(t.id ORDER BY t.id) FILTER (WHERE t.id IS NOT NULL), '{}')::uuid[]
FROM   scanner_pass_sessions ps
JOIN   sessions s ON s.id = ps.session_id AND s.deleted_at IS NULL
JOIN   events e   ON e.id = s.event_id AND e.org_id = $2 AND e.deleted_at IS NULL
LEFT   JOIN scanner_pass_tiers pt ON pt.pass_id = ps.pass_id AND pt.session_id = ps.session_id
                                 AND NOT ps.all_tiers
LEFT   JOIN ticket_tiers t        ON t.id = pt.tier_id AND t.session_id = ps.session_id
                                 AND t.deleted_at IS NULL
WHERE  ps.pass_id = $1
GROUP  BY ps.session_id, s.event_id, ps.all_tiers, s.start_at
ORDER  BY s.start_at, ps.session_id;

-- ── devices ─────────────────────────────────────────────────────────────────

-- name: InsertScannerDevice :one
INSERT INTO scanner_devices (org_id, user_id, pass_id, label, platform, access_token_hash, access_expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: UserCanScanInOrg :one
SELECT EXISTS (
    SELECT 1
    FROM   memberships m
    JOIN   users u             ON u.id = m.user_id AND u.deactivated_at IS NULL
    JOIN   roles r             ON r.org_id IS NULL AND r.name = m.role
    JOIN   role_permissions rp ON rp.role_id = r.id
    JOIN   permissions p       ON p.id = rp.permission_id AND p.name = 'scan.perform'
    WHERE  m.user_id = $1 AND m.org_id = $2 AND m.status = 'active'
      AND  m.role NOT IN ('platform_superadmin', 'platform_operator'));

-- name: GetScannerDeviceForAuth :one
SELECT d.id, d.org_id, d.user_id, d.pass_id, d.label, d.platform, d.last_seen_at,
       (d.revoked_at IS NOT NULL), d.access_expires_at,
       (o.deleted_at IS NOT NULL),
       (d.user_id IS NOT NULL AND EXISTS (
           SELECT 1
           FROM   memberships m
           JOIN   users u             ON u.id = m.user_id AND u.deactivated_at IS NULL
           JOIN   roles r             ON r.org_id IS NULL AND r.name = m.role
           JOIN   role_permissions rp ON rp.role_id = r.id
           JOIN   permissions p       ON p.id = rp.permission_id AND p.name = 'scan.perform'
           WHERE  m.user_id = d.user_id AND m.org_id = d.org_id AND m.status = 'active'
             AND  m.role NOT IN ('platform_superadmin', 'platform_operator'))),
       COALESCE(sp.revoked_at IS NOT NULL, false), sp.kind, sp.valid_from, sp.valid_until
FROM   scanner_devices d
JOIN   organizations o ON o.id = d.org_id
LEFT   JOIN scanner_passes sp ON sp.id = d.pass_id AND sp.org_id = d.org_id
WHERE  d.access_token_hash = $1;

-- name: TouchScannerDeviceLastSeen :execrows
UPDATE scanner_devices
SET    last_seen_at = $2
WHERE  id = $1 AND (last_seen_at IS NULL OR last_seen_at <= $3);

-- name: RevokeScannerDevice :execrows
UPDATE scanner_devices
SET    revoked_at = $3, revoked_by_user_id = $4, revoke_reason = $5
WHERE  id = $1 AND org_id = $2 AND revoked_at IS NULL;

-- name: GetOrganizationNameForScanner :one
SELECT name FROM organizations WHERE id = $1;

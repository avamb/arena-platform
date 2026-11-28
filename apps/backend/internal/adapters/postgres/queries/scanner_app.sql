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
SELECT d.id, d.org_id, d.user_id, d.pass_id, d.label, d.platform, d.last_seen_at, d.controller_name, d.pair_confirmed,
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

-- ── pairing codes (migration 0229, spec 69 MA-11 / MA-12) ───────────────────

-- name: DeleteOpenScannerOrganizerCodes :exec
DELETE FROM scanner_pairing_codes
WHERE  org_id = $1 AND user_id = $2 AND used_at IS NULL;

-- name: DeleteScannerPassCodes :exec
DELETE FROM scanner_pairing_codes WHERE pass_id = $1;

-- name: InsertScannerPairingCode :exec
INSERT INTO scanner_pairing_codes (code_hash, org_id, user_id, pass_id, created_by_user_id, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: LockScannerPairingCode :one
SELECT c.id, c.org_id, c.user_id, c.pass_id, c.expires_at, c.used_at, (o.deleted_at IS NOT NULL)
FROM   scanner_pairing_codes c
JOIN   organizations o ON o.id = c.org_id
WHERE  c.code_hash = $1
FOR UPDATE OF c;

-- name: MarkScannerPairingCodeUsed :exec
UPDATE scanner_pairing_codes SET used_at = $2, used_device_id = $3 WHERE id = $1;

-- name: InsertPairedScannerDevice :one
INSERT INTO scanner_devices (org_id, user_id, pass_id, label, platform, controller_name,
                             access_token_hash, access_expires_at, refresh_token_hash, refresh_expires_at,
                             pair_confirmed, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, $11)
RETURNING id;

-- ── pass management (migration 0229) ────────────────────────────────────────

-- name: LockScannerPassRow :one
SELECT id, label, kind, valid_from, valid_until, max_devices, revoked_at
FROM   scanner_passes
WHERE  id = $1 AND org_id = $2
FOR UPDATE;

-- name: ReenableScannerPass :exec
UPDATE scanner_passes SET revoked_at = NULL, revoked_by_user_id = NULL WHERE id = $1;

-- name: UpdateScannerPassRow :exec
UPDATE scanner_passes
SET    label = $2, valid_from = $3, valid_until = $4, max_devices = $5
WHERE  id = $1;

-- name: DeleteScannerPassSessions :exec
DELETE FROM scanner_pass_sessions WHERE pass_id = $1;

-- name: RevokeScannerDevicesOfPass :many
UPDATE scanner_devices
SET    revoked_at = $2, revoked_by_user_id = $3, revoke_reason = $4
WHERE  pass_id = $1 AND revoked_at IS NULL
RETURNING id;

-- name: ListScannerPassesOfOrg :many
SELECT p.id, p.org_id, p.label, p.kind, p.valid_from, p.valid_until, p.max_devices, p.created_at, p.revoked_at,
       (SELECT count(*) FROM scanner_devices d WHERE d.pass_id = p.id AND d.revoked_at IS NULL)
FROM   scanner_passes p
WHERE  p.org_id = $1
  AND  ($2::uuid IS NULL OR EXISTS (
           SELECT 1
           FROM   scanner_pass_sessions ps
           JOIN   sessions s ON s.id = ps.session_id AND s.deleted_at IS NULL
           JOIN   events e   ON e.id = s.event_id AND e.org_id = p.org_id AND e.deleted_at IS NULL
           WHERE  ps.pass_id = p.id AND s.event_id = $2::uuid))
ORDER  BY p.created_at DESC, p.id
LIMIT  $3;

-- name: GetScannerPassFull :one
SELECT p.id, p.org_id, p.label, p.kind, p.valid_from, p.valid_until, p.max_devices, p.created_at, p.revoked_at,
       (SELECT count(*) FROM scanner_devices d WHERE d.pass_id = p.id AND d.revoked_at IS NULL)
FROM   scanner_passes p
WHERE  p.id = $1 AND p.org_id = $2;

-- name: ListScannerPassSessionsOfPasses :many
SELECT ps.pass_id, ps.session_id, s.event_id, ps.all_tiers,
       COALESCE(array_agg(t.id ORDER BY t.id) FILTER (WHERE t.id IS NOT NULL), '{}')::uuid[]
FROM   scanner_pass_sessions ps
JOIN   sessions s ON s.id = ps.session_id AND s.deleted_at IS NULL
JOIN   events e   ON e.id = s.event_id AND e.org_id = $2 AND e.deleted_at IS NULL
LEFT   JOIN scanner_pass_tiers pt ON pt.pass_id = ps.pass_id AND pt.session_id = ps.session_id
                                 AND NOT ps.all_tiers
LEFT   JOIN ticket_tiers t        ON t.id = pt.tier_id AND t.session_id = ps.session_id
                                 AND t.deleted_at IS NULL
WHERE  ps.pass_id = ANY($1::uuid[])
GROUP  BY ps.pass_id, ps.session_id, s.event_id, ps.all_tiers, s.start_at
ORDER  BY ps.pass_id, s.start_at, ps.session_id;

-- name: ListLiveScannerDevicesOfPasses :many
SELECT pass_id, id, controller_name, label, platform, created_at, last_seen_at
FROM   scanner_devices
WHERE  pass_id = ANY($1::uuid[]) AND org_id = $2 AND revoked_at IS NULL
ORDER  BY pass_id, created_at, id;

-- name: ScannerSessionsWindow :one
SELECT count(DISTINCT s.id), min(COALESCE(s.doors_open_at, s.start_at)), max(s.start_at)
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  s.id = ANY($2::uuid[])
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- ── refresh (migration 0229) ────────────────────────────────────────────────

-- name: FindScannerDeviceByRefresh :one
SELECT id FROM scanner_devices
WHERE  refresh_token_hash = $1 OR prev_refresh_token_hash = $1
LIMIT  1;

-- name: FindRetiredScannerRefreshToken :one
SELECT r.device_id, d.org_id, d.revoke_reason
FROM   scanner_retired_refresh_tokens r
JOIN   scanner_devices d ON d.id = r.device_id
WHERE  r.token_hash = $1;

-- name: LockScannerDeviceForRefresh :one
SELECT d.id, d.org_id, d.user_id, d.pass_id, d.refresh_token_hash, d.prev_refresh_token_hash,
       d.pair_confirmed, d.refresh_expires_at, d.revoked_at, d.revoke_reason,
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
       COALESCE(sp.revoked_at IS NOT NULL, false), sp.kind, sp.valid_until
FROM   scanner_devices d
JOIN   organizations o ON o.id = d.org_id
LEFT   JOIN scanner_passes sp ON sp.id = d.pass_id AND sp.org_id = d.org_id
WHERE  d.id = $1
FOR UPDATE OF d;

-- name: InsertRetiredScannerRefreshToken :exec
INSERT INTO scanner_retired_refresh_tokens (token_hash, device_id, retired_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: RotateScannerDeviceTokens :exec
UPDATE scanner_devices
SET    access_token_hash = $2, access_expires_at = $3,
       refresh_token_hash = $4, refresh_expires_at = $5,
       prev_refresh_token_hash = $6, pair_confirmed = false, refreshed_at = $7
WHERE  id = $1;

-- name: DeleteOldRetiredScannerRefreshTokens :exec
DELETE FROM scanner_retired_refresh_tokens WHERE device_id = $1 AND retired_at < $2;

-- name: ConfirmScannerDevicePair :one
WITH old AS (
    SELECT id, prev_refresh_token_hash AS prev
    FROM   scanner_devices
    WHERE  id = $1 AND NOT pair_confirmed AND access_token_hash = $2
    FOR UPDATE
), upd AS (
    UPDATE scanner_devices d
    SET    pair_confirmed = true, prev_refresh_token_hash = NULL
    FROM   old
    WHERE  d.id = old.id AND NOT d.pair_confirmed AND d.access_token_hash = $2
    RETURNING d.id, old.prev
), ret AS (
    INSERT INTO scanner_retired_refresh_tokens (token_hash, device_id, retired_at)
    SELECT prev, id, $3 FROM upd WHERE prev IS NOT NULL
    ON CONFLICT DO NOTHING
)
SELECT count(*) FROM upd;

-- ── device management (migration 0229) ──────────────────────────────────────

-- name: ListScannerDevicesOfOrg :many
SELECT d.id, d.user_id, d.pass_id, sp.label, d.label, d.platform, d.controller_name, d.created_at, d.last_seen_at
FROM   scanner_devices d
LEFT   JOIN scanner_passes sp ON sp.id = d.pass_id AND sp.org_id = d.org_id
WHERE  d.org_id = $1 AND d.revoked_at IS NULL
  AND  ($2::uuid IS NULL OR d.user_id = $2::uuid)
ORDER  BY d.created_at DESC, d.id;

-- name: RevokeScannerDeviceOfOrg :execrows
UPDATE scanner_devices
SET    revoked_at = $3, revoked_by_user_id = $5, revoke_reason = $6
WHERE  id = $1 AND org_id = $2 AND revoked_at IS NULL
  AND  ($4::uuid IS NULL OR user_id = $4::uuid);

-- name: ScannerDeviceOfOrgExists :one
SELECT EXISTS (
    SELECT 1 FROM scanner_devices
    WHERE  id = $1 AND org_id = $2 AND ($3::uuid IS NULL OR user_id = $3::uuid));

-- ── pair attempts (migration 0229) ──────────────────────────────────────────

-- name: InsertScannerPairAttempt :exec
INSERT INTO scanner_pair_attempts (ip_hash, code_hash, outcome, created_at) VALUES ($1, $2, $3, $4);

-- name: DeleteOldScannerPairAttempts :exec
DELETE FROM scanner_pair_attempts WHERE created_at < $1;

-- name: ScannerPairAttemptsByIP :one
SELECT count(*) FILTER (WHERE outcome = 'not_found'), count(*),
       min(created_at) FILTER (WHERE outcome = 'not_found'), min(created_at)
FROM   scanner_pair_attempts
WHERE  ip_hash = $1 AND created_at > $2;

-- name: ScannerPairAttemptsByCode :one
SELECT count(*), min(created_at)
FROM   scanner_pair_attempts
WHERE  code_hash = $1 AND created_at > $2 AND outcome IN ('not_found', 'pass_full');

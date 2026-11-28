// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: scanner_app.sql (migrations 0227 and 0229, spec 69 MA-10 to MA-12)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ── passes ──────────────────────────────────────────────────────────────────

const countScannerSessionsOfOrg = `-- name: CountScannerSessionsOfOrg :one
SELECT count(DISTINCT s.id)
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  s.id = ANY($2::uuid[])
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL
`

// CountScannerSessionsOfOrg counts how many of sessionIDs are live dates of
// orgID (through sessions -> events; neither the date nor its event
// soft-deleted). A pass may take a date only when the
// count equals the number of distinct ids asked for.
func (q *Queries) CountScannerSessionsOfOrg(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countScannerSessionsOfOrg, orgID, sessionIDs).Scan(&n)
	return n, err
}

const countScannerTiersOfOrg = `-- name: CountScannerTiersOfOrg :one
SELECT count(DISTINCT (x.tier_id, x.session_id))
FROM   unnest($2::uuid[], $3::uuid[]) AS x(tier_id, session_id)
JOIN   ticket_tiers t ON t.id = x.tier_id AND t.session_id = x.session_id
JOIN   sessions s     ON s.id = t.session_id
JOIN   events e       ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  t.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL
`

// CountScannerTiersOfOrg counts how many (tier, session) pairs name a live
// category of that very live date of orgID (none of category, date and event
// soft-deleted). tierIDs and sessionIDs are parallel.
func (q *Queries) CountScannerTiersOfOrg(ctx context.Context, orgID uuid.UUID, tierIDs, sessionIDs []uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countScannerTiersOfOrg, orgID, tierIDs, sessionIDs).Scan(&n)
	return n, err
}

const insertScannerPass = `-- name: InsertScannerPass :one
INSERT INTO scanner_passes (org_id, label, kind, valid_from, valid_until, max_devices, created_by_user_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id
`

// InsertScannerPassParams are the columns of a new controller pass.
type InsertScannerPassParams struct {
	OrgID           uuid.UUID  `json:"org_id"`
	Label           string     `json:"label"`
	Kind            string     `json:"kind"`
	ValidFrom       *time.Time `json:"valid_from"`
	ValidUntil      *time.Time `json:"valid_until"`
	MaxDevices      int32      `json:"max_devices"`
	CreatedByUserID *uuid.UUID `json:"created_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
}

// InsertScannerPass stores a controller pass and returns its id.
func (q *Queries) InsertScannerPass(ctx context.Context, arg InsertScannerPassParams) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, insertScannerPass, arg.OrgID, arg.Label, arg.Kind, arg.ValidFrom,
		arg.ValidUntil, arg.MaxDevices, arg.CreatedByUserID, arg.CreatedAt).Scan(&id)
	return id, err
}

const insertScannerPassSession = `-- name: InsertScannerPassSession :exec
INSERT INTO scanner_pass_sessions (pass_id, session_id, all_tiers) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
`

// InsertScannerPassSession lets a pass check one date: every category of it
// (allTiers true) or only the categories stored with InsertScannerPassTier
// (allTiers false).
func (q *Queries) InsertScannerPassSession(ctx context.Context, passID, sessionID uuid.UUID, allTiers bool) error {
	_, err := q.db.Exec(ctx, insertScannerPassSession, passID, sessionID, allTiers)
	return err
}

const insertScannerPassTier = `-- name: InsertScannerPassTier :exec
INSERT INTO scanner_pass_tiers (pass_id, session_id, tier_id) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
`

// InsertScannerPassTier narrows a pass's date to one more category.
func (q *Queries) InsertScannerPassTier(ctx context.Context, passID, sessionID, tierID uuid.UUID) error {
	_, err := q.db.Exec(ctx, insertScannerPassTier, passID, sessionID, tierID)
	return err
}

// ScannerPassLockRow is a pass read under its row lock.
type ScannerPassLockRow struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind"`
	ValidUntil *time.Time `json:"valid_until"`
	MaxDevices int32      `json:"max_devices"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

const lockScannerPass = `-- name: LockScannerPass :one
SELECT id, kind, valid_until, max_devices, revoked_at
FROM   scanner_passes
WHERE  id = $1 AND org_id = $2
FOR UPDATE
`

// LockScannerPass reads a pass of orgID under FOR UPDATE: two device
// creations of the same pass serialize on it. A pass of another
// organization is pgx.ErrNoRows.
func (q *Queries) LockScannerPass(ctx context.Context, passID, orgID uuid.UUID) (ScannerPassLockRow, error) {
	var r ScannerPassLockRow
	err := q.db.QueryRow(ctx, lockScannerPass, passID, orgID).Scan(&r.ID, &r.Kind, &r.ValidUntil, &r.MaxDevices, &r.RevokedAt)
	return r, err
}

const countLiveScannerDevicesOfPass = `-- name: CountLiveScannerDevicesOfPass :one
SELECT count(*) FROM scanner_devices WHERE pass_id = $1 AND revoked_at IS NULL
`

// CountLiveScannerDevicesOfPass counts the non-revoked devices of a pass.
func (q *Queries) CountLiveScannerDevicesOfPass(ctx context.Context, passID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countLiveScannerDevicesOfPass, passID).Scan(&n)
	return n, err
}

const revokeScannerPass = `-- name: RevokeScannerPass :execrows
UPDATE scanner_passes
SET    revoked_at = $3, revoked_by_user_id = $4
WHERE  id = $1 AND org_id = $2 AND revoked_at IS NULL
`

// RevokeScannerPass takes a pass of orgID back. Zero rows: unknown, foreign
// or already revoked.
func (q *Queries) RevokeScannerPass(ctx context.Context, passID, orgID uuid.UUID, at time.Time, by *uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, revokeScannerPass, passID, orgID, at, by)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ScannerPassRow is a pass as the app describes it.
type ScannerPassRow struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	Kind       string     `json:"kind"`
	ValidFrom  *time.Time `json:"valid_from"`
	ValidUntil *time.Time `json:"valid_until"`
}

const getScannerPassOfOrg = `-- name: GetScannerPassOfOrg :one
SELECT id, label, kind, valid_from, valid_until
FROM   scanner_passes
WHERE  id = $1 AND org_id = $2
`

// GetScannerPassOfOrg reads a pass of orgID; a foreign pass is pgx.ErrNoRows.
func (q *Queries) GetScannerPassOfOrg(ctx context.Context, passID, orgID uuid.UUID) (ScannerPassRow, error) {
	var r ScannerPassRow
	err := q.db.QueryRow(ctx, getScannerPassOfOrg, passID, orgID).Scan(&r.ID, &r.Label, &r.Kind, &r.ValidFrom, &r.ValidUntil)
	return r, err
}

// ScannerPassSessionRow is one date of a pass. AllTiers true: every category
// of the date and TierIDs is empty. AllTiers false: ONLY the live categories
// in TierIDs; empty TierIDs then means the pass checks nothing on the date.
type ScannerPassSessionRow struct {
	SessionID uuid.UUID   `json:"session_id"`
	EventID   uuid.UUID   `json:"event_id"`
	AllTiers  bool        `json:"all_tiers"`
	TierIDs   []uuid.UUID `json:"tier_ids"`
}

const listScannerPassSessions = `-- name: ListScannerPassSessions :many
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
ORDER  BY s.start_at, ps.session_id
`

// ListScannerPassSessions lists the live dates of a pass that belong to orgID
// (re-joined through sessions -> events, soft-deleted dates and events
// skipped): a row naming a date of another organization is never returned.
// The categories of a restricted date are its live categories only; a tier
// row of a date with all_tiers true is ignored.
func (q *Queries) ListScannerPassSessions(ctx context.Context, passID, orgID uuid.UUID) ([]ScannerPassSessionRow, error) {
	rows, err := q.db.Query(ctx, listScannerPassSessions, passID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScannerPassSessionRow
	for rows.Next() {
		var r ScannerPassSessionRow
		if err := rows.Scan(&r.SessionID, &r.EventID, &r.AllTiers, &r.TierIDs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── devices ─────────────────────────────────────────────────────────────────

const insertScannerDevice = `-- name: InsertScannerDevice :one
INSERT INTO scanner_devices (org_id, user_id, pass_id, label, platform, access_token_hash, access_expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id
`

// InsertScannerDeviceParams are the columns of a new device. Exactly one of
// UserID and PassID is set.
type InsertScannerDeviceParams struct {
	OrgID           uuid.UUID  `json:"org_id"`
	UserID          *uuid.UUID `json:"user_id"`
	PassID          *uuid.UUID `json:"pass_id"`
	Label           string     `json:"label"`
	Platform        string     `json:"platform"`
	AccessTokenHash string     `json:"access_token_hash"`
	AccessExpiresAt time.Time  `json:"access_expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// InsertScannerDevice stores a device and returns its id.
func (q *Queries) InsertScannerDevice(ctx context.Context, arg InsertScannerDeviceParams) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, insertScannerDevice, arg.OrgID, arg.UserID, arg.PassID, arg.Label,
		arg.Platform, arg.AccessTokenHash, arg.AccessExpiresAt, arg.CreatedAt).Scan(&id)
	return id, err
}

const userCanScanInOrg = `-- name: UserCanScanInOrg :one
SELECT EXISTS (
    SELECT 1
    FROM   memberships m
    JOIN   users u             ON u.id = m.user_id AND u.deactivated_at IS NULL
    JOIN   roles r             ON r.org_id IS NULL AND r.name = m.role
    JOIN   role_permissions rp ON rp.role_id = r.id
    JOIN   permissions p       ON p.id = rp.permission_id AND p.name = 'scan.perform'
    WHERE  m.user_id = $1 AND m.org_id = $2 AND m.status = 'active'
      AND  m.role NOT IN ('platform_superadmin', 'platform_operator'))
`

// UserCanScanInOrg reports whether the person has an ACTIVE membership in
// orgID whose role holds scan.perform. A platform-level role or a membership
// in another organization does not count.
func (q *Queries) UserCanScanInOrg(ctx context.Context, userID, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, userCanScanInOrg, userID, orgID).Scan(&ok)
	return ok, err
}

// ScannerDeviceAuthRow is a device with everything Authenticate decides on.
type ScannerDeviceAuthRow struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	UserID          *uuid.UUID `json:"user_id"`
	PassID          *uuid.UUID `json:"pass_id"`
	Label           string     `json:"label"`
	Platform        string     `json:"platform"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	ControllerName  string     `json:"controller_name"`
	PairConfirmed   bool       `json:"pair_confirmed"`
	Revoked         bool       `json:"revoked"`
	AccessExpiresAt time.Time  `json:"access_expires_at"`
	OrgArchived     bool       `json:"org_archived"`
	UserCanScan     bool       `json:"user_can_scan"`
	PassRevoked     bool       `json:"pass_revoked"`
	PassKind        *string    `json:"pass_kind"`
	PassValidFrom   *time.Time `json:"pass_valid_from"`
	PassValidUntil  *time.Time `json:"pass_valid_until"`
	PassRevokedAt   *time.Time `json:"pass_revoked_at"`
}

const getScannerDeviceForAuth = `-- name: GetScannerDeviceForAuth :one
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
       COALESCE(sp.revoked_at IS NOT NULL, false), sp.kind, sp.valid_from, sp.valid_until, sp.revoked_at
FROM   scanner_devices d
JOIN   organizations o ON o.id = d.org_id
LEFT   JOIN scanner_passes sp ON sp.id = d.pass_id AND sp.org_id = d.org_id
WHERE  d.access_token_hash = $1
`

// GetScannerDeviceForAuth reads a device by the SHA-256 of its token in ONE
// query, with its organization, the person's right to scan in the device's
// organization and its pass. Unknown hash: pgx.ErrNoRows.
func (q *Queries) GetScannerDeviceForAuth(ctx context.Context, accessTokenHash string) (ScannerDeviceAuthRow, error) {
	var r ScannerDeviceAuthRow
	err := q.db.QueryRow(ctx, getScannerDeviceForAuth, accessTokenHash).Scan(
		&r.ID, &r.OrgID, &r.UserID, &r.PassID, &r.Label, &r.Platform, &r.LastSeenAt,
		&r.ControllerName, &r.PairConfirmed,
		&r.Revoked, &r.AccessExpiresAt, &r.OrgArchived, &r.UserCanScan,
		&r.PassRevoked, &r.PassKind, &r.PassValidFrom, &r.PassValidUntil, &r.PassRevokedAt)
	return r, err
}

const touchScannerDeviceLastSeen = `-- name: TouchScannerDeviceLastSeen :execrows
UPDATE scanner_devices
SET    last_seen_at = $2
WHERE  id = $1 AND (last_seen_at IS NULL OR last_seen_at <= $3)
`

// TouchScannerDeviceLastSeen sets last_seen_at to at when it is unset or not
// later than notAfter (the throttle). Returns the rows written.
func (q *Queries) TouchScannerDeviceLastSeen(ctx context.Context, id uuid.UUID, at, notAfter time.Time) (int64, error) {
	tag, err := q.db.Exec(ctx, touchScannerDeviceLastSeen, id, at, notAfter)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const revokeScannerDevice = `-- name: RevokeScannerDevice :execrows
UPDATE scanner_devices
SET    revoked_at = $3, revoked_by_user_id = $4, revoke_reason = $5
WHERE  id = $1 AND org_id = $2 AND revoked_at IS NULL
`

// RevokeScannerDevice takes a device of orgID back. Zero rows: unknown,
// foreign or already revoked.
func (q *Queries) RevokeScannerDevice(ctx context.Context, id, orgID uuid.UUID, at time.Time, by *uuid.UUID, reason *string) (int64, error) {
	tag, err := q.db.Exec(ctx, revokeScannerDevice, id, orgID, at, by, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const getOrganizationNameForScanner = `-- name: GetOrganizationNameForScanner :one
SELECT name FROM organizations WHERE id = $1
`

// GetOrganizationNameForScanner reads the name of a device's organization.
func (q *Queries) GetOrganizationNameForScanner(ctx context.Context, orgID uuid.UUID) (string, error) {
	var name string
	err := q.db.QueryRow(ctx, getOrganizationNameForScanner, orgID).Scan(&name)
	return name, err
}

// ── pairing codes (migration 0229, spec 69 MA-11 / MA-12) ───────────────────

const deleteOpenScannerOrganizerCodes = `-- name: DeleteOpenScannerOrganizerCodes :exec
DELETE FROM scanner_pairing_codes
WHERE  org_id = $1 AND user_id = $2 AND used_at IS NULL
`

// DeleteOpenScannerOrganizerCodes deletes the unused codes of a person in an
// organization; used codes stay as the record of their devices.
func (q *Queries) DeleteOpenScannerOrganizerCodes(ctx context.Context, orgID, userID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteOpenScannerOrganizerCodes, orgID, userID)
	return err
}

const deleteScannerPassCodes = `-- name: DeleteScannerPassCodes :exec
DELETE FROM scanner_pairing_codes WHERE pass_id = $1
`

// DeleteScannerPassCodes deletes every pairing code of a pass.
func (q *Queries) DeleteScannerPassCodes(ctx context.Context, passID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteScannerPassCodes, passID)
	return err
}

const insertScannerPairingCode = `-- name: InsertScannerPairingCode :exec
INSERT INTO scanner_pairing_codes (code_hash, org_id, user_id, pass_id, created_by_user_id, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

// InsertScannerPairingCodeParams are the columns of a new pairing code.
// Exactly one of UserID and PassID is set.
type InsertScannerPairingCodeParams struct {
	CodeHash        string     `json:"code_hash"`
	OrgID           uuid.UUID  `json:"org_id"`
	UserID          *uuid.UUID `json:"user_id"`
	PassID          *uuid.UUID `json:"pass_id"`
	CreatedByUserID *uuid.UUID `json:"created_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
}

// InsertScannerPairingCode stores a pairing code.
func (q *Queries) InsertScannerPairingCode(ctx context.Context, arg InsertScannerPairingCodeParams) error {
	_, err := q.db.Exec(ctx, insertScannerPairingCode, arg.CodeHash, arg.OrgID, arg.UserID, arg.PassID,
		arg.CreatedByUserID, arg.CreatedAt, arg.ExpiresAt)
	return err
}

// ScannerPairingCodeLockRow is a pairing code read under its row lock.
type ScannerPairingCodeLockRow struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	UserID      *uuid.UUID `json:"user_id"`
	PassID      *uuid.UUID `json:"pass_id"`
	ExpiresAt   time.Time  `json:"expires_at"`
	UsedAt      *time.Time `json:"used_at"`
	OrgArchived bool       `json:"org_archived"`
}

const getScannerPairingCode = `-- name: GetScannerPairingCode :one
SELECT c.id, c.org_id, c.user_id, c.pass_id, c.expires_at, c.used_at, (o.deleted_at IS NOT NULL)
FROM   scanner_pairing_codes c
JOIN   organizations o ON o.id = c.org_id
WHERE  c.code_hash = $1
`

// GetScannerPairingCode reads a code by its hash WITHOUT a lock, with
// whether its organization is archived. Unknown hash: pgx.ErrNoRows. Pair
// reads it first to learn the lock order (a pass code locks the pass before
// the code).
func (q *Queries) GetScannerPairingCode(ctx context.Context, codeHash string) (ScannerPairingCodeLockRow, error) {
	var r ScannerPairingCodeLockRow
	err := q.db.QueryRow(ctx, getScannerPairingCode, codeHash).Scan(
		&r.ID, &r.OrgID, &r.UserID, &r.PassID, &r.ExpiresAt, &r.UsedAt, &r.OrgArchived)
	return r, err
}

const lockScannerPairingCode = `-- name: LockScannerPairingCode :one
SELECT c.id, c.org_id, c.user_id, c.pass_id, c.expires_at, c.used_at, (o.deleted_at IS NOT NULL)
FROM   scanner_pairing_codes c
JOIN   organizations o ON o.id = c.org_id
WHERE  c.code_hash = $1
FOR UPDATE OF c
`

// LockScannerPairingCode reads a code by its hash under FOR UPDATE, with
// whether its organization is archived. Unknown hash: pgx.ErrNoRows.
func (q *Queries) LockScannerPairingCode(ctx context.Context, codeHash string) (ScannerPairingCodeLockRow, error) {
	var r ScannerPairingCodeLockRow
	err := q.db.QueryRow(ctx, lockScannerPairingCode, codeHash).Scan(
		&r.ID, &r.OrgID, &r.UserID, &r.PassID, &r.ExpiresAt, &r.UsedAt, &r.OrgArchived)
	return r, err
}

const markScannerPairingCodeUsed = `-- name: MarkScannerPairingCodeUsed :exec
UPDATE scanner_pairing_codes SET used_at = $2, used_device_id = $3 WHERE id = $1
`

// MarkScannerPairingCodeUsed spends a one-time code on a device.
func (q *Queries) MarkScannerPairingCodeUsed(ctx context.Context, id uuid.UUID, at time.Time, deviceID uuid.UUID) error {
	_, err := q.db.Exec(ctx, markScannerPairingCodeUsed, id, at, deviceID)
	return err
}

const insertPairedScannerDevice = `-- name: InsertPairedScannerDevice :one
INSERT INTO scanner_devices (org_id, user_id, pass_id, label, platform, controller_name,
                             access_token_hash, access_expires_at, refresh_token_hash, refresh_expires_at,
                             pair_confirmed, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, $11)
RETURNING id
`

// InsertPairedScannerDeviceParams are the columns of a device made by a
// pairing-code exchange. Exactly one of UserID and PassID is set.
type InsertPairedScannerDeviceParams struct {
	OrgID            uuid.UUID  `json:"org_id"`
	UserID           *uuid.UUID `json:"user_id"`
	PassID           *uuid.UUID `json:"pass_id"`
	Label            string     `json:"label"`
	Platform         string     `json:"platform"`
	ControllerName   string     `json:"controller_name"`
	AccessTokenHash  string     `json:"access_token_hash"`
	AccessExpiresAt  time.Time  `json:"access_expires_at"`
	RefreshTokenHash string     `json:"refresh_token_hash"`
	RefreshExpiresAt time.Time  `json:"refresh_expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// InsertPairedScannerDevice stores a paired device with both its tokens and
// returns its id.
func (q *Queries) InsertPairedScannerDevice(ctx context.Context, arg InsertPairedScannerDeviceParams) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, insertPairedScannerDevice, arg.OrgID, arg.UserID, arg.PassID, arg.Label,
		arg.Platform, arg.ControllerName, arg.AccessTokenHash, arg.AccessExpiresAt,
		arg.RefreshTokenHash, arg.RefreshExpiresAt, arg.CreatedAt).Scan(&id)
	return id, err
}

// ── pass management (migration 0229) ────────────────────────────────────────

// ScannerPassFullLockRow is a pass read under its row lock, every column the
// pass routes change.
type ScannerPassFullLockRow struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	Kind       string     `json:"kind"`
	ValidFrom  *time.Time `json:"valid_from"`
	ValidUntil *time.Time `json:"valid_until"`
	MaxDevices int32      `json:"max_devices"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

const lockScannerPassRow = `-- name: LockScannerPassRow :one
SELECT id, label, kind, valid_from, valid_until, max_devices, revoked_at
FROM   scanner_passes
WHERE  id = $1 AND org_id = $2
FOR UPDATE
`

// LockScannerPassRow reads a pass of orgID under FOR UPDATE. A pass of
// another organization is pgx.ErrNoRows.
func (q *Queries) LockScannerPassRow(ctx context.Context, passID, orgID uuid.UUID) (ScannerPassFullLockRow, error) {
	var r ScannerPassFullLockRow
	err := q.db.QueryRow(ctx, lockScannerPassRow, passID, orgID).Scan(
		&r.ID, &r.Label, &r.Kind, &r.ValidFrom, &r.ValidUntil, &r.MaxDevices, &r.RevokedAt)
	return r, err
}

const reenableScannerPass = `-- name: ReenableScannerPass :exec
UPDATE scanner_passes SET revoked_at = NULL, revoked_by_user_id = NULL WHERE id = $1
`

// ReenableScannerPass turns a revoked pass back on.
func (q *Queries) ReenableScannerPass(ctx context.Context, passID uuid.UUID) error {
	_, err := q.db.Exec(ctx, reenableScannerPass, passID)
	return err
}

const updateScannerPassRow = `-- name: UpdateScannerPassRow :exec
UPDATE scanner_passes
SET    label = $2, valid_from = $3, valid_until = $4, max_devices = $5
WHERE  id = $1
`

// UpdateScannerPassRow writes the label, window and device limit of a pass.
func (q *Queries) UpdateScannerPassRow(ctx context.Context, passID uuid.UUID, label string, validFrom, validUntil *time.Time, maxDevices int32) error {
	_, err := q.db.Exec(ctx, updateScannerPassRow, passID, label, validFrom, validUntil, maxDevices)
	return err
}

const deleteScannerPassSessions = `-- name: DeleteScannerPassSessions :exec
DELETE FROM scanner_pass_sessions WHERE pass_id = $1
`

// DeleteScannerPassSessions deletes every date of a pass (its category rows
// go with them by the cascade).
func (q *Queries) DeleteScannerPassSessions(ctx context.Context, passID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteScannerPassSessions, passID)
	return err
}

const revokeScannerDevicesOfPass = `-- name: RevokeScannerDevicesOfPass :many
UPDATE scanner_devices
SET    revoked_at = $2, revoked_by_user_id = $3, revoke_reason = $4
WHERE  pass_id = $1 AND revoked_at IS NULL
RETURNING id
`

// RevokeScannerDevicesOfPass takes back every live device of a pass and
// returns their ids.
func (q *Queries) RevokeScannerDevicesOfPass(ctx context.Context, passID uuid.UUID, at time.Time, by *uuid.UUID, reason string) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, revokeScannerDevicesOfPass, passID, at, by, reason)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ScannerPassFullRow is a pass as the organizer sees it, with the number of
// its live devices.
type ScannerPassFullRow struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"org_id"`
	Label      string     `json:"label"`
	Kind       string     `json:"kind"`
	ValidFrom  *time.Time `json:"valid_from"`
	ValidUntil *time.Time `json:"valid_until"`
	MaxDevices int32      `json:"max_devices"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	Accepted   int64      `json:"accepted"`
}

func scanScannerPassFull(row interface{ Scan(dest ...any) error }) (ScannerPassFullRow, error) {
	var r ScannerPassFullRow
	err := row.Scan(&r.ID, &r.OrgID, &r.Label, &r.Kind, &r.ValidFrom, &r.ValidUntil,
		&r.MaxDevices, &r.CreatedAt, &r.RevokedAt, &r.Accepted)
	return r, err
}

const listScannerPassesOfOrg = `-- name: ListScannerPassesOfOrg :many
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
LIMIT  $3
`

// ListScannerPassesOfOrg lists the passes of orgID, newest first, revoked
// ones included. A non-nil eventID keeps only the passes with a live date of
// that event of orgID.
func (q *Queries) ListScannerPassesOfOrg(ctx context.Context, orgID uuid.UUID, eventID *uuid.UUID, limit int32) ([]ScannerPassFullRow, error) {
	rows, err := q.db.Query(ctx, listScannerPassesOfOrg, orgID, eventID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScannerPassFullRow
	for rows.Next() {
		r, err := scanScannerPassFull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getScannerPassFull = `-- name: GetScannerPassFull :one
SELECT p.id, p.org_id, p.label, p.kind, p.valid_from, p.valid_until, p.max_devices, p.created_at, p.revoked_at,
       (SELECT count(*) FROM scanner_devices d WHERE d.pass_id = p.id AND d.revoked_at IS NULL)
FROM   scanner_passes p
WHERE  p.id = $1 AND p.org_id = $2
`

// GetScannerPassFull reads one pass of orgID; a foreign pass is
// pgx.ErrNoRows.
func (q *Queries) GetScannerPassFull(ctx context.Context, passID, orgID uuid.UUID) (ScannerPassFullRow, error) {
	return scanScannerPassFull(q.db.QueryRow(ctx, getScannerPassFull, passID, orgID))
}

// ScannerPassSessionOfPassRow is one date of one of several passes.
type ScannerPassSessionOfPassRow struct {
	PassID uuid.UUID `json:"pass_id"`
	ScannerPassSessionRow
}

const listScannerPassSessionsOfPasses = `-- name: ListScannerPassSessionsOfPasses :many
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
ORDER  BY ps.pass_id, s.start_at, ps.session_id
`

// ListScannerPassSessionsOfPasses is ListScannerPassSessions for several
// passes at once, with the same organization re-join.
func (q *Queries) ListScannerPassSessionsOfPasses(ctx context.Context, passIDs []uuid.UUID, orgID uuid.UUID) ([]ScannerPassSessionOfPassRow, error) {
	rows, err := q.db.Query(ctx, listScannerPassSessionsOfPasses, passIDs, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScannerPassSessionOfPassRow
	for rows.Next() {
		var r ScannerPassSessionOfPassRow
		if err := rows.Scan(&r.PassID, &r.SessionID, &r.EventID, &r.AllTiers, &r.TierIDs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScannerPassDeviceRow is a live device of a pass.
type ScannerPassDeviceRow struct {
	PassID         uuid.UUID  `json:"pass_id"`
	ID             uuid.UUID  `json:"id"`
	ControllerName string     `json:"controller_name"`
	Label          string     `json:"label"`
	Platform       string     `json:"platform"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}

const listLiveScannerDevicesOfPasses = `-- name: ListLiveScannerDevicesOfPasses :many
SELECT pass_id, id, controller_name, label, platform, created_at, last_seen_at
FROM   scanner_devices
WHERE  pass_id = ANY($1::uuid[]) AND org_id = $2 AND revoked_at IS NULL
ORDER  BY pass_id, created_at, id
`

// ListLiveScannerDevicesOfPasses lists the non-revoked devices of the passes.
func (q *Queries) ListLiveScannerDevicesOfPasses(ctx context.Context, passIDs []uuid.UUID, orgID uuid.UUID) ([]ScannerPassDeviceRow, error) {
	rows, err := q.db.Query(ctx, listLiveScannerDevicesOfPasses, passIDs, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScannerPassDeviceRow
	for rows.Next() {
		var r ScannerPassDeviceRow
		if err := rows.Scan(&r.PassID, &r.ID, &r.ControllerName, &r.Label, &r.Platform, &r.CreatedAt, &r.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScannerSessionsWindowRow is how many of the asked dates are live dates of
// the organization, the earliest doors (or start) and the latest start.
type ScannerSessionsWindowRow struct {
	Count       int64      `json:"count"`
	EarliestIn  *time.Time `json:"earliest_in"`
	LatestStart *time.Time `json:"latest_start"`
}

const scannerSessionsWindow = `-- name: ScannerSessionsWindow :one
SELECT count(DISTINCT s.id), min(COALESCE(s.doors_open_at, s.start_at)), max(s.start_at)
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  s.id = ANY($2::uuid[])
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL
`

// ScannerSessionsWindow reads the times a default pass window is built from.
func (q *Queries) ScannerSessionsWindow(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (ScannerSessionsWindowRow, error) {
	var r ScannerSessionsWindowRow
	err := q.db.QueryRow(ctx, scannerSessionsWindow, orgID, sessionIDs).Scan(&r.Count, &r.EarliestIn, &r.LatestStart)
	return r, err
}

// ── refresh (migration 0229) ────────────────────────────────────────────────

const findScannerDeviceByRefresh = `-- name: FindScannerDeviceByRefresh :one
SELECT id FROM scanner_devices
WHERE  refresh_token_hash = $1 OR prev_refresh_token_hash = $1
LIMIT  1
`

// FindScannerDeviceByRefresh finds, without a lock, the device whose current
// or previous refresh token has this hash. Unknown: pgx.ErrNoRows.
func (q *Queries) FindScannerDeviceByRefresh(ctx context.Context, hash string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, findScannerDeviceByRefresh, hash).Scan(&id)
	return id, err
}

const findRetiredScannerRefreshToken = `-- name: FindRetiredScannerRefreshToken :one
SELECT r.device_id, d.org_id, d.revoke_reason
FROM   scanner_retired_refresh_tokens r
JOIN   scanner_devices d ON d.id = r.device_id
WHERE  r.token_hash = $1
`

// FindRetiredScannerRefreshToken finds the device of a replaced refresh
// token with its organization and current revoke reason. Unknown:
// pgx.ErrNoRows.
func (q *Queries) FindRetiredScannerRefreshToken(ctx context.Context, hash string) (deviceID, orgID uuid.UUID, revokeReason *string, err error) {
	err = q.db.QueryRow(ctx, findRetiredScannerRefreshToken, hash).Scan(&deviceID, &orgID, &revokeReason)
	return deviceID, orgID, revokeReason, err
}

// ScannerDeviceRefreshRow is a device read under its row lock with
// everything a refresh decides on.
type ScannerDeviceRefreshRow struct {
	ID                   uuid.UUID  `json:"id"`
	OrgID                uuid.UUID  `json:"org_id"`
	UserID               *uuid.UUID `json:"user_id"`
	PassID               *uuid.UUID `json:"pass_id"`
	RefreshTokenHash     *string    `json:"refresh_token_hash"`
	PrevRefreshTokenHash *string    `json:"prev_refresh_token_hash"`
	PairConfirmed        bool       `json:"pair_confirmed"`
	RefreshExpiresAt     *time.Time `json:"refresh_expires_at"`
	RevokedAt            *time.Time `json:"revoked_at"`
	RevokeReason         *string    `json:"revoke_reason"`
	OrgArchived          bool       `json:"org_archived"`
	UserCanScan          bool       `json:"user_can_scan"`
	PassRevoked          bool       `json:"pass_revoked"`
	PassKind             *string    `json:"pass_kind"`
	PassValidUntil       *time.Time `json:"pass_valid_until"`
	PassRevokedAt        *time.Time `json:"pass_revoked_at"`
}

const lockScannerDeviceForRefresh = `-- name: LockScannerDeviceForRefresh :one
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
       COALESCE(sp.revoked_at IS NOT NULL, false), sp.kind, sp.valid_until, sp.revoked_at
FROM   scanner_devices d
JOIN   organizations o ON o.id = d.org_id
LEFT   JOIN scanner_passes sp ON sp.id = d.pass_id AND sp.org_id = d.org_id
WHERE  d.id = $1
FOR UPDATE OF d
`

// LockScannerDeviceForRefresh reads a device by id under FOR UPDATE: two
// refreshes of one device serialize on it.
func (q *Queries) LockScannerDeviceForRefresh(ctx context.Context, id uuid.UUID) (ScannerDeviceRefreshRow, error) {
	var r ScannerDeviceRefreshRow
	err := q.db.QueryRow(ctx, lockScannerDeviceForRefresh, id).Scan(
		&r.ID, &r.OrgID, &r.UserID, &r.PassID, &r.RefreshTokenHash, &r.PrevRefreshTokenHash,
		&r.PairConfirmed, &r.RefreshExpiresAt, &r.RevokedAt, &r.RevokeReason,
		&r.OrgArchived, &r.UserCanScan, &r.PassRevoked, &r.PassKind, &r.PassValidUntil, &r.PassRevokedAt)
	return r, err
}

const insertRetiredScannerRefreshToken = `-- name: InsertRetiredScannerRefreshToken :exec
INSERT INTO scanner_retired_refresh_tokens (token_hash, device_id, retired_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
`

// InsertRetiredScannerRefreshToken records a replaced refresh token.
func (q *Queries) InsertRetiredScannerRefreshToken(ctx context.Context, hash string, deviceID uuid.UUID, at time.Time) error {
	_, err := q.db.Exec(ctx, insertRetiredScannerRefreshToken, hash, deviceID, at)
	return err
}

const rotateScannerDeviceTokens = `-- name: RotateScannerDeviceTokens :exec
UPDATE scanner_devices
SET    access_token_hash = $2, access_expires_at = $3,
       refresh_token_hash = $4, refresh_expires_at = $5,
       prev_refresh_token_hash = $6, pair_confirmed = false, refreshed_at = $7
WHERE  id = $1
`

// RotateScannerDeviceTokensParams are the columns a refresh writes.
type RotateScannerDeviceTokensParams struct {
	ID                   uuid.UUID `json:"id"`
	AccessTokenHash      string    `json:"access_token_hash"`
	AccessExpiresAt      time.Time `json:"access_expires_at"`
	RefreshTokenHash     string    `json:"refresh_token_hash"`
	RefreshExpiresAt     time.Time `json:"refresh_expires_at"`
	PrevRefreshTokenHash *string   `json:"prev_refresh_token_hash"`
	RefreshedAt          time.Time `json:"refreshed_at"`
}

// RotateScannerDeviceTokens writes a new token pair; the pair is unconfirmed
// until the device uses its access token.
func (q *Queries) RotateScannerDeviceTokens(ctx context.Context, arg RotateScannerDeviceTokensParams) error {
	_, err := q.db.Exec(ctx, rotateScannerDeviceTokens, arg.ID, arg.AccessTokenHash, arg.AccessExpiresAt,
		arg.RefreshTokenHash, arg.RefreshExpiresAt, arg.PrevRefreshTokenHash, arg.RefreshedAt)
	return err
}

const deleteOldRetiredScannerRefreshTokens = `-- name: DeleteOldRetiredScannerRefreshTokens :exec
DELETE FROM scanner_retired_refresh_tokens WHERE device_id = $1 AND retired_at < $2
`

// DeleteOldRetiredScannerRefreshTokens deletes the retired tokens of a device
// replaced before the given moment.
func (q *Queries) DeleteOldRetiredScannerRefreshTokens(ctx context.Context, deviceID uuid.UUID, before time.Time) error {
	_, err := q.db.Exec(ctx, deleteOldRetiredScannerRefreshTokens, deviceID, before)
	return err
}

const confirmScannerDevicePair = `-- name: ConfirmScannerDevicePair :one
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
SELECT count(*) FROM upd
`

// ConfirmScannerDevicePair marks the current pair of a device confirmed when
// accessTokenHash is still its access token, and moves the previous refresh
// token into the retired ones, in ONE statement. Returns the rows confirmed.
func (q *Queries) ConfirmScannerDevicePair(ctx context.Context, id uuid.UUID, accessTokenHash string, at time.Time) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, confirmScannerDevicePair, id, accessTokenHash, at).Scan(&n)
	return n, err
}

// ── device management (migration 0229) ──────────────────────────────────────

// ScannerDeviceListRow is a live device of an organization.
type ScannerDeviceListRow struct {
	ID             uuid.UUID  `json:"id"`
	UserID         *uuid.UUID `json:"user_id"`
	PassID         *uuid.UUID `json:"pass_id"`
	PassLabel      *string    `json:"pass_label"`
	Label          string     `json:"label"`
	Platform       string     `json:"platform"`
	ControllerName string     `json:"controller_name"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}

const listScannerDevicesOfOrg = `-- name: ListScannerDevicesOfOrg :many
SELECT d.id, d.user_id, d.pass_id, sp.label, d.label, d.platform, d.controller_name, d.created_at, d.last_seen_at
FROM   scanner_devices d
LEFT   JOIN scanner_passes sp ON sp.id = d.pass_id AND sp.org_id = d.org_id
WHERE  d.org_id = $1 AND d.revoked_at IS NULL
  AND  ($2::uuid IS NULL OR d.user_id = $2::uuid)
ORDER  BY d.created_at DESC, d.id
`

// ListScannerDevicesOfOrg lists the live devices of orgID, newest first; a
// non-nil userID keeps only that person's organizer devices.
func (q *Queries) ListScannerDevicesOfOrg(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID) ([]ScannerDeviceListRow, error) {
	rows, err := q.db.Query(ctx, listScannerDevicesOfOrg, orgID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScannerDeviceListRow
	for rows.Next() {
		var r ScannerDeviceListRow
		if err := rows.Scan(&r.ID, &r.UserID, &r.PassID, &r.PassLabel, &r.Label, &r.Platform,
			&r.ControllerName, &r.CreatedAt, &r.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const revokeScannerDeviceOfOrg = `-- name: RevokeScannerDeviceOfOrg :execrows
UPDATE scanner_devices
SET    revoked_at = $3, revoked_by_user_id = $5, revoke_reason = $6
WHERE  id = $1 AND org_id = $2 AND revoked_at IS NULL
  AND  ($4::uuid IS NULL OR user_id = $4::uuid)
`

// RevokeScannerDeviceOfOrg takes back a live device of orgID; a non-nil
// onlyUser limits it to that person's devices. Zero rows: unknown, foreign,
// not the person's or already revoked.
func (q *Queries) RevokeScannerDeviceOfOrg(ctx context.Context, id, orgID uuid.UUID, at time.Time, onlyUser, by *uuid.UUID, reason string) (int64, error) {
	tag, err := q.db.Exec(ctx, revokeScannerDeviceOfOrg, id, orgID, at, onlyUser, by, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const scannerDeviceOfOrgExists = `-- name: ScannerDeviceOfOrgExists :one
SELECT EXISTS (
    SELECT 1 FROM scanner_devices
    WHERE  id = $1 AND org_id = $2 AND ($3::uuid IS NULL OR user_id = $3::uuid))
`

// ScannerDeviceOfOrgExists reports whether the device is a device of orgID
// (and of onlyUser when it is set), revoked or not.
func (q *Queries) ScannerDeviceOfOrgExists(ctx context.Context, id, orgID uuid.UUID, onlyUser *uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, scannerDeviceOfOrgExists, id, orgID, onlyUser).Scan(&ok)
	return ok, err
}

// ── pair attempts (migration 0229) ──────────────────────────────────────────

const insertScannerPairAttempt = `-- name: InsertScannerPairAttempt :exec
INSERT INTO scanner_pair_attempts (ip_hash, code_hash, outcome, created_at) VALUES ($1, $2, $3, $4)
`

// InsertScannerPairAttempt records one exchange of a pairing code.
func (q *Queries) InsertScannerPairAttempt(ctx context.Context, ipHash, codeHash, outcome string, at time.Time) error {
	_, err := q.db.Exec(ctx, insertScannerPairAttempt, ipHash, codeHash, outcome, at)
	return err
}

const deleteOldScannerPairAttempts = `-- name: DeleteOldScannerPairAttempts :exec
DELETE FROM scanner_pair_attempts WHERE created_at < $1
`

// DeleteOldScannerPairAttempts deletes the attempts made before the moment.
func (q *Queries) DeleteOldScannerPairAttempts(ctx context.Context, before time.Time) error {
	_, err := q.db.Exec(ctx, deleteOldScannerPairAttempts, before)
	return err
}

// ScannerPairAttemptsByIPRow counts the attempts of one address in a window.
type ScannerPairAttemptsByIPRow struct {
	Failed       int64      `json:"failed"`
	All          int64      `json:"all"`
	OldestFailed *time.Time `json:"oldest_failed"`
	OldestAll    *time.Time `json:"oldest_all"`
}

const scannerPairAttemptsByIP = `-- name: ScannerPairAttemptsByIP :one
SELECT count(*) FILTER (WHERE outcome = 'not_found'), count(*),
       min(created_at) FILTER (WHERE outcome = 'not_found'), min(created_at)
FROM   scanner_pair_attempts
WHERE  ip_hash = $1 AND created_at > $2
`

// ScannerPairAttemptsByIP counts the attempts of an address made after since.
func (q *Queries) ScannerPairAttemptsByIP(ctx context.Context, ipHash string, since time.Time) (ScannerPairAttemptsByIPRow, error) {
	var r ScannerPairAttemptsByIPRow
	err := q.db.QueryRow(ctx, scannerPairAttemptsByIP, ipHash, since).Scan(&r.Failed, &r.All, &r.OldestFailed, &r.OldestAll)
	return r, err
}

const scannerPairAttemptsByCode = `-- name: ScannerPairAttemptsByCode :one
SELECT count(*), min(created_at)
FROM   scanner_pair_attempts
WHERE  code_hash = $1 AND created_at > $2 AND outcome IN ('not_found', 'pass_full')
`

// ScannerPairAttemptsByCode counts the refused attempts of a code made after
// since.
func (q *Queries) ScannerPairAttemptsByCode(ctx context.Context, codeHash string, since time.Time) (int64, *time.Time, error) {
	var (
		n      int64
		oldest *time.Time
	)
	err := q.db.QueryRow(ctx, scannerPairAttemptsByCode, codeHash, since).Scan(&n, &oldest)
	return n, oldest, err
}

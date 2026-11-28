// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: scanner_app.sql (migration 0227, spec 69 MA-10)

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
	Revoked         bool       `json:"revoked"`
	AccessExpiresAt time.Time  `json:"access_expires_at"`
	OrgArchived     bool       `json:"org_archived"`
	UserCanScan     bool       `json:"user_can_scan"`
	PassRevoked     bool       `json:"pass_revoked"`
	PassKind        *string    `json:"pass_kind"`
	PassValidFrom   *time.Time `json:"pass_valid_from"`
	PassValidUntil  *time.Time `json:"pass_valid_until"`
}

const getScannerDeviceForAuth = `-- name: GetScannerDeviceForAuth :one
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
WHERE  d.access_token_hash = $1
`

// GetScannerDeviceForAuth reads a device by the SHA-256 of its token in ONE
// query, with its organization, the person's right to scan in the device's
// organization and its pass. Unknown hash: pgx.ErrNoRows.
func (q *Queries) GetScannerDeviceForAuth(ctx context.Context, accessTokenHash string) (ScannerDeviceAuthRow, error) {
	var r ScannerDeviceAuthRow
	err := q.db.QueryRow(ctx, getScannerDeviceForAuth, accessTokenHash).Scan(
		&r.ID, &r.OrgID, &r.UserID, &r.PassID, &r.Label, &r.Platform, &r.LastSeenAt,
		&r.Revoked, &r.AccessExpiresAt, &r.OrgArchived, &r.UserCanScan,
		&r.PassRevoked, &r.PassKind, &r.PassValidFrom, &r.PassValidUntil)
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

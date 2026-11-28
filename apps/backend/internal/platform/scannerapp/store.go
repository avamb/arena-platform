package scannerapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Device kinds.
const (
	KindOrganizer = "organizer"
	KindPass      = "pass"
)

// Pass kinds (scanner_passes.kind).
const (
	PassKindStandard = "standard"
	PassKindDemo     = "demo"
)

// Device platforms (scanner_devices.platform).
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
	PlatformOther   = "other"
)

// MaxLabelLen is the longest label of a pass or a device, in characters.
const MaxLabelLen = 80

// LastSeenThrottle is how often TouchLastSeen writes for one device at most.
const LastSeenThrottle = time.Minute

// Authenticate refusals.
var (
	// ErrMalformed: the token does not have the shape of a device token.
	ErrMalformed = errors.New("scannerapp: malformed device token")
	// ErrUnknown: no device has this token.
	ErrUnknown = errors.New("scannerapp: unknown device token")
	// ErrRevoked: the device was taken back.
	ErrRevoked = errors.New("scannerapp: device revoked")
	// ErrExpired: the access token is past access_expires_at; the device
	// may refresh it.
	ErrExpired = errors.New("scannerapp: device access token expired")
	// ErrAccessEnded: the right to scan is gone (organization archived, the
	// person lost scan.perform in the organization, the pass was revoked or
	// its window ended). A refresh does not help.
	ErrAccessEnded = errors.New("scannerapp: access to scanning ended")
)

// Creation refusals.
var (
	// ErrInvalidPass: label, kind, window or max_devices of a pass is wrong.
	ErrInvalidPass = errors.New("scannerapp: invalid pass")
	// ErrInvalidDevice: label or platform of a device is wrong.
	ErrInvalidDevice = errors.New("scannerapp: invalid device")
	// ErrForeignSession: a date of the pass is not a live date of the
	// organization.
	ErrForeignSession = errors.New("scannerapp: session does not belong to the organization")
	// ErrForeignTier: a category of the pass is not a category of that date.
	ErrForeignTier = errors.New("scannerapp: tier does not belong to the session")
	// ErrPassNotFound: no pass with this id in the organization.
	ErrPassNotFound = errors.New("scannerapp: pass not found")
	// ErrPassRevoked: the pass was revoked.
	ErrPassRevoked = errors.New("scannerapp: pass revoked")
	// ErrTooManyDevices: the pass already has max_devices non-revoked devices.
	ErrTooManyDevices = errors.New("scannerapp: pass has the maximum number of devices")
)

// TxDB is a database handle that can open a nested transaction: a
// *pgxpool.Pool (a new transaction) or a pgx.Tx (a SAVEPOINT).
type TxDB interface {
	gen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Device is an authenticated scanner device.
type Device struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	UserID     *uuid.UUID
	PassID     *uuid.UUID
	Kind       string // KindOrganizer | KindPass
	Label      string
	Platform   string
	LastSeenAt *time.Time
}

// Me is what a device is told about itself.
type Me struct {
	OrgID   uuid.UUID
	OrgName string
	Kind    string // KindOrganizer | KindPass
	Pass    *MePass
}

// MePass is the pass of a pass device.
type MePass struct {
	ID         uuid.UUID
	Label      string
	Kind       string // PassKindStandard | PassKindDemo
	ValidFrom  *time.Time
	ValidUntil *time.Time
	Sessions   []MePassSession
}

// MePassSession is one date a pass may check. AllTiers true: every category
// of the date (TierIDs empty). AllTiers false: ONLY the categories in
// TierIDs; when TierIDs is empty the pass checks nothing on the date.
type MePassSession struct {
	SessionID uuid.UUID
	EventID   uuid.UUID
	AllTiers  bool
	TierIDs   []uuid.UUID
}

// Authenticate finds the device of a raw access token with ONE query and
// refuses it when the device is revoked (ErrRevoked), when the right to scan
// ended (ErrAccessEnded) or when the access token expired (ErrExpired, checked
// last: a refresh helps only when nothing else is wrong). A pass device whose
// window has not started yet still authenticates.
func Authenticate(ctx context.Context, db gen.DBTX, raw string, now time.Time) (Device, error) {
	if !LooksLikeDeviceToken(raw) {
		return Device{}, ErrMalformed
	}
	row, err := gen.New(db).GetScannerDeviceForAuth(ctx, HashToken(raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrUnknown
	}
	if err != nil {
		return Device{}, fmt.Errorf("scannerapp: read device: %w", err)
	}
	if row.Revoked {
		return Device{}, ErrRevoked
	}
	if row.OrgArchived {
		return Device{}, ErrAccessEnded
	}
	d := Device{
		ID:         row.ID,
		OrgID:      row.OrgID,
		UserID:     row.UserID,
		PassID:     row.PassID,
		Label:      row.Label,
		Platform:   row.Platform,
		LastSeenAt: row.LastSeenAt,
	}
	switch {
	case row.UserID != nil:
		if !row.UserCanScan {
			return Device{}, ErrAccessEnded
		}
		d.Kind = KindOrganizer
	case row.PassID != nil:
		if row.PassKind == nil || row.PassRevoked {
			return Device{}, ErrAccessEnded
		}
		if *row.PassKind != PassKindDemo && (row.PassValidUntil == nil || !now.Before(*row.PassValidUntil)) {
			return Device{}, ErrAccessEnded
		}
		d.Kind = KindPass
	default:
		return Device{}, ErrAccessEnded
	}
	if !row.AccessExpiresAt.After(now) {
		return Device{}, ErrExpired
	}
	return d, nil
}

// TouchLastSeen records that the device was seen at now, writing at most once
// per LastSeenThrottle. It reports whether it wrote.
func TouchLastSeen(ctx context.Context, db gen.DBTX, deviceID uuid.UUID, now time.Time) (bool, error) {
	n, err := gen.New(db).TouchScannerDeviceLastSeen(ctx, deviceID, now, now.Add(-LastSeenThrottle))
	if err != nil {
		return false, fmt.Errorf("scannerapp: touch last seen: %w", err)
	}
	return n > 0, nil
}

// Describe tells a device its organization and, for a pass device, its pass
// and the dates of the pass. Every date is re-read through sessions -> events
// and kept only when it belongs to the device's organization.
func Describe(ctx context.Context, db gen.DBTX, d Device) (Me, error) {
	q := gen.New(db)
	name, err := q.GetOrganizationNameForScanner(ctx, d.OrgID)
	if err != nil {
		return Me{}, fmt.Errorf("scannerapp: read organization: %w", err)
	}
	me := Me{OrgID: d.OrgID, OrgName: name, Kind: d.Kind}
	if d.Kind != KindPass || d.PassID == nil {
		return me, nil
	}
	p, err := q.GetScannerPassOfOrg(ctx, *d.PassID, d.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Me{}, ErrPassNotFound
	}
	if err != nil {
		return Me{}, fmt.Errorf("scannerapp: read pass: %w", err)
	}
	rows, err := q.ListScannerPassSessions(ctx, p.ID, d.OrgID)
	if err != nil {
		return Me{}, fmt.Errorf("scannerapp: read pass sessions: %w", err)
	}
	mp := &MePass{
		ID: p.ID, Label: p.Label, Kind: p.Kind,
		ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil,
		Sessions: make([]MePassSession, 0, len(rows)),
	}
	for _, r := range rows {
		tiers := r.TierIDs
		if tiers == nil || r.AllTiers {
			tiers = []uuid.UUID{}
		}
		mp.Sessions = append(mp.Sessions, MePassSession{SessionID: r.SessionID, EventID: r.EventID, AllTiers: r.AllTiers, TierIDs: tiers})
	}
	me.Pass = mp
	return me, nil
}

// CreatePassParams describe a new controller pass.
type CreatePassParams struct {
	OrgID      uuid.UUID
	Label      string
	Kind       string // PassKindStandard (default when empty) | PassKindDemo
	ValidFrom  *time.Time
	ValidUntil *time.Time
	MaxDevices int
	CreatedBy  *uuid.UUID
	Sessions   []CreatePassSession
}

// CreatePassSession is one date of a new pass; no TierIDs means every
// category of the date (all_tiers true), otherwise ONLY the listed categories
// (all_tiers false: deleting them later never widens the pass).
type CreatePassSession struct {
	SessionID uuid.UUID
	TierIDs   []uuid.UUID
}

// CreatePass stores a controller pass with its dates and categories in one
// nested transaction. Every date must be a live date of params.OrgID
// (sessions -> events) and every category a category of that date, else
// ErrForeignSession / ErrForeignTier and nothing is written.
func CreatePass(ctx context.Context, db TxDB, params CreatePassParams, now time.Time) (uuid.UUID, error) {
	label := strings.TrimSpace(params.Label)
	kind := params.Kind
	if kind == "" {
		kind = PassKindStandard
	}
	if label == "" || len([]rune(label)) > MaxLabelLen || params.MaxDevices < 1 || params.MaxDevices > 1<<20 {
		return uuid.Nil, ErrInvalidPass
	}
	switch kind {
	case PassKindDemo:
		if params.ValidFrom != nil || params.ValidUntil != nil {
			return uuid.Nil, ErrInvalidPass
		}
	case PassKindStandard:
		if params.ValidFrom == nil || params.ValidUntil == nil || !params.ValidUntil.After(*params.ValidFrom) {
			return uuid.Nil, ErrInvalidPass
		}
	default:
		return uuid.Nil, ErrInvalidPass
	}

	sessionIDs := make([]uuid.UUID, 0, len(params.Sessions))
	seen := make(map[uuid.UUID]bool, len(params.Sessions))
	var tierIDs, tierSessionIDs []uuid.UUID
	seenTier := map[[2]uuid.UUID]bool{}
	// A date named twice is restricted when any of its entries lists
	// categories: the narrower reading wins.
	restricted := map[uuid.UUID]bool{}
	for _, s := range params.Sessions {
		if !seen[s.SessionID] {
			seen[s.SessionID] = true
			sessionIDs = append(sessionIDs, s.SessionID)
		}
		if len(s.TierIDs) > 0 {
			restricted[s.SessionID] = true
		}
		for _, t := range s.TierIDs {
			k := [2]uuid.UUID{t, s.SessionID}
			if seenTier[k] {
				continue
			}
			seenTier[k] = true
			tierIDs = append(tierIDs, t)
			tierSessionIDs = append(tierSessionIDs, s.SessionID)
		}
	}

	var passID uuid.UUID
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		if len(sessionIDs) > 0 {
			n, err := q.CountScannerSessionsOfOrg(ctx, params.OrgID, sessionIDs)
			if err != nil {
				return fmt.Errorf("scannerapp: check sessions: %w", err)
			}
			if n != int64(len(sessionIDs)) {
				return ErrForeignSession
			}
		}
		if len(tierIDs) > 0 {
			n, err := q.CountScannerTiersOfOrg(ctx, params.OrgID, tierIDs, tierSessionIDs)
			if err != nil {
				return fmt.Errorf("scannerapp: check tiers: %w", err)
			}
			if n != int64(len(tierIDs)) {
				return ErrForeignTier
			}
		}
		id, err := q.InsertScannerPass(ctx, gen.InsertScannerPassParams{
			OrgID: params.OrgID, Label: label, Kind: kind,
			ValidFrom: params.ValidFrom, ValidUntil: params.ValidUntil,
			MaxDevices: int32(params.MaxDevices), CreatedByUserID: params.CreatedBy, CreatedAt: now, //nolint:gosec // 1..1<<20, checked above
		})
		if err != nil {
			return fmt.Errorf("scannerapp: insert pass: %w", err)
		}
		for _, sid := range sessionIDs {
			if err := q.InsertScannerPassSession(ctx, id, sid, !restricted[sid]); err != nil {
				return fmt.Errorf("scannerapp: insert pass session: %w", err)
			}
		}
		for i, tid := range tierIDs {
			if err := q.InsertScannerPassTier(ctx, id, tierSessionIDs[i], tid); err != nil {
				return fmt.Errorf("scannerapp: insert pass tier: %w", err)
			}
		}
		passID = id
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return passID, nil
}

// DeviceParams describe a new device. UserID is read by
// CreateOrganizerDevice, PassID by CreatePassDevice.
type DeviceParams struct {
	OrgID    uuid.UUID
	UserID   uuid.UUID
	PassID   uuid.UUID
	Label    string
	Platform string
}

func checkDevice(p DeviceParams) (string, error) {
	label := strings.TrimSpace(p.Label)
	if len([]rune(label)) > MaxLabelLen {
		return "", ErrInvalidDevice
	}
	switch p.Platform {
	case PlatformIOS, PlatformAndroid, PlatformOther:
	default:
		return "", ErrInvalidDevice
	}
	return label, nil
}

// CreateOrganizerDevice stores a device of a person in params.OrgID and
// returns it with its raw access token (shown once). The person must have an
// active membership in the organization whose role holds scan.perform, else
// ErrAccessEnded.
func CreateOrganizerDevice(ctx context.Context, db gen.DBTX, params DeviceParams, now time.Time) (Device, string, error) {
	label, err := checkDevice(params)
	if err != nil {
		return Device{}, "", err
	}
	q := gen.New(db)
	ok, err := q.UserCanScanInOrg(ctx, params.UserID, params.OrgID)
	if err != nil {
		return Device{}, "", fmt.Errorf("scannerapp: check membership: %w", err)
	}
	if !ok {
		return Device{}, "", ErrAccessEnded
	}
	raw, hash, err := NewToken()
	if err != nil {
		return Device{}, "", fmt.Errorf("scannerapp: new token: %w", err)
	}
	userID := params.UserID
	id, err := q.InsertScannerDevice(ctx, gen.InsertScannerDeviceParams{
		OrgID: params.OrgID, UserID: &userID, Label: label, Platform: params.Platform,
		AccessTokenHash: hash, AccessExpiresAt: now.Add(AccessTokenTTL), CreatedAt: now,
	})
	if err != nil {
		return Device{}, "", fmt.Errorf("scannerapp: insert device: %w", err)
	}
	return Device{ID: id, OrgID: params.OrgID, UserID: &userID, Kind: KindOrganizer, Label: label, Platform: params.Platform}, raw, nil
}

// CreatePassDevice stores a device of a pass of params.OrgID and returns it
// with its raw access token (shown once). The pass row is locked FOR UPDATE,
// so two creations of the same pass serialize. Refused: a pass not of the
// organization (ErrPassNotFound), a revoked pass (ErrPassRevoked), a
// standard pass whose window ended (ErrAccessEnded) and a pass that already
// has max_devices non-revoked devices (ErrTooManyDevices).
func CreatePassDevice(ctx context.Context, db TxDB, params DeviceParams, now time.Time) (Device, string, error) {
	label, err := checkDevice(params)
	if err != nil {
		return Device{}, "", err
	}
	var (
		d   Device
		raw string
	)
	err = inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		p, err := q.LockScannerPass(ctx, params.PassID, params.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPassNotFound
		}
		if err != nil {
			return fmt.Errorf("scannerapp: lock pass: %w", err)
		}
		if p.RevokedAt != nil {
			return ErrPassRevoked
		}
		if p.Kind != PassKindDemo && (p.ValidUntil == nil || !now.Before(*p.ValidUntil)) {
			return ErrAccessEnded
		}
		n, err := q.CountLiveScannerDevicesOfPass(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("scannerapp: count devices: %w", err)
		}
		if n >= int64(p.MaxDevices) {
			return ErrTooManyDevices
		}
		var hash string
		raw, hash, err = NewToken()
		if err != nil {
			return fmt.Errorf("scannerapp: new token: %w", err)
		}
		passID := p.ID
		id, err := q.InsertScannerDevice(ctx, gen.InsertScannerDeviceParams{
			OrgID: params.OrgID, PassID: &passID, Label: label, Platform: params.Platform,
			AccessTokenHash: hash, AccessExpiresAt: now.Add(AccessTokenTTL), CreatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("scannerapp: insert device: %w", err)
		}
		d = Device{ID: id, OrgID: params.OrgID, PassID: &passID, Kind: KindPass, Label: label, Platform: params.Platform}
		return nil
	})
	if err != nil {
		return Device{}, "", err
	}
	return d, raw, nil
}

// RevokeDevice takes a device of orgID back. It reports false when there was
// nothing to revoke (unknown, foreign or already revoked device).
func RevokeDevice(ctx context.Context, db gen.DBTX, orgID, deviceID uuid.UUID, by *uuid.UUID, reason string, now time.Time) (bool, error) {
	var r *string
	if reason = strings.TrimSpace(reason); reason != "" {
		if len([]rune(reason)) > 200 {
			reason = string([]rune(reason)[:200])
		}
		r = &reason
	}
	n, err := gen.New(db).RevokeScannerDevice(ctx, deviceID, orgID, now, by, r)
	if err != nil {
		return false, fmt.Errorf("scannerapp: revoke device: %w", err)
	}
	return n > 0, nil
}

// RevokePass takes a pass of orgID back; its devices stop authenticating
// (ErrAccessEnded). It reports false when there was nothing to revoke.
func RevokePass(ctx context.Context, db gen.DBTX, orgID, passID uuid.UUID, by *uuid.UUID, now time.Time) (bool, error) {
	n, err := gen.New(db).RevokeScannerPass(ctx, passID, orgID, now, by)
	if err != nil {
		return false, fmt.Errorf("scannerapp: revoke pass: %w", err)
	}
	return n > 0, nil
}

// inTx runs fn in a nested transaction of db (a transaction on a pool, a
// SAVEPOINT on a transaction) and rolls it back when fn fails.
func inTx(ctx context.Context, db TxDB, fn func(pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("scannerapp: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("scannerapp: commit: %w", err)
	}
	return nil
}

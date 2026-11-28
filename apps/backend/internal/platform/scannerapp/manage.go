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

// MaxPassDevices is the highest max_devices of a pass the routes accept.
const MaxPassDevices = 50

// DefaultPassDevices is max_devices of a pass when the request names none.
const DefaultPassDevices = 2

// PassListLimit is how many passes ListPasses returns at most.
const PassListLimit = 100

// The default window of a pass: from the earliest doors (or start) of its
// dates minus PassWindowBefore until the latest start plus PassWindowAfter.
const (
	PassWindowBefore = 4 * time.Hour
	PassWindowAfter  = 6 * time.Hour
)

// DeviceInfo is a live device of an organization as the organizer sees it.
type DeviceInfo struct {
	ID             uuid.UUID
	Kind           string // KindOrganizer | KindPass
	Label          string
	Platform       string
	ControllerName string
	PassID         *uuid.UUID
	PassLabel      *string
	UserID         *uuid.UUID
	CreatedAt      time.Time
	LastSeenAt     *time.Time
}

// ListDevices lists the live (not revoked) devices of orgID, newest first.
// A non-nil onlyUser keeps only that person's organizer devices (a caller
// with scan.perform only); nil lists every device (scanner_pass.manage).
func ListDevices(ctx context.Context, db gen.DBTX, orgID uuid.UUID, onlyUser *uuid.UUID) ([]DeviceInfo, error) {
	rows, err := gen.New(db).ListScannerDevicesOfOrg(ctx, orgID, onlyUser)
	if err != nil {
		return nil, fmt.Errorf("scannerapp: list devices: %w", err)
	}
	out := make([]DeviceInfo, 0, len(rows))
	for _, r := range rows {
		kind := KindOrganizer
		if r.PassID != nil {
			kind = KindPass
		}
		out = append(out, DeviceInfo{
			ID: r.ID, Kind: kind, Label: r.Label, Platform: r.Platform, ControllerName: r.ControllerName,
			PassID: r.PassID, PassLabel: r.PassLabel, UserID: r.UserID, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt,
		})
	}
	return out, nil
}

// RevokeDeviceOf takes back a device of orgID by a person (reason
// revoked_by_user, revoked_by_user_id = by). A non-nil onlyUser lets only
// that person's own devices be revoked. A device that is not of the
// organization, or not the person's, is ErrDeviceNotFound; an already
// revoked one is (false, nil). justRevoked is true when this call revoked
// it (the caller writes the audit event).
func RevokeDeviceOf(ctx context.Context, db gen.DBTX, orgID, deviceID uuid.UUID, onlyUser, by *uuid.UUID, now time.Time) (justRevoked bool, err error) {
	q := gen.New(db)
	n, err := q.RevokeScannerDeviceOfOrg(ctx, deviceID, orgID, now, onlyUser, by, ReasonRevokedByUser)
	if err != nil {
		return false, fmt.Errorf("scannerapp: revoke device: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	ok, err := q.ScannerDeviceOfOrgExists(ctx, deviceID, orgID, onlyUser)
	if err != nil {
		return false, fmt.Errorf("scannerapp: read device: %w", err)
	}
	if !ok {
		return false, ErrDeviceNotFound
	}
	return false, nil
}

// Pass is a controller pass as the organizer sees it.
type Pass struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Label      string
	Kind       string // PassKindStandard | PassKindDemo
	ValidFrom  *time.Time
	ValidUntil *time.Time
	MaxDevices int
	// Accepted is the number of live devices of the pass.
	Accepted  int
	CreatedAt time.Time
	RevokedAt *time.Time
	// Sessions are the live dates of the pass in the organization (see
	// MePassSession); never nil.
	Sessions []MePassSession
	// Devices are the live devices of the pass, oldest first; never nil.
	Devices []PassDevice
}

// PassDevice is a live device of a pass.
type PassDevice struct {
	ID             uuid.UUID
	ControllerName string
	Label          string
	Platform       string
	CreatedAt      time.Time
	LastSeenAt     *time.Time
}

// ListPasses lists up to PassListLimit passes of orgID, newest first,
// revoked ones included, each with its dates and live devices. A non-nil
// eventID keeps only the passes with a live date of that event.
func ListPasses(ctx context.Context, db gen.DBTX, orgID uuid.UUID, eventID *uuid.UUID) ([]Pass, error) {
	rows, err := gen.New(db).ListScannerPassesOfOrg(ctx, orgID, eventID, PassListLimit)
	if err != nil {
		return nil, fmt.Errorf("scannerapp: list passes: %w", err)
	}
	return fillPasses(ctx, db, orgID, rows)
}

// GetPass reads one pass of orgID with its dates and live devices; a pass
// of another organization is ErrPassNotFound.
func GetPass(ctx context.Context, db gen.DBTX, orgID, passID uuid.UUID) (Pass, error) {
	row, err := gen.New(db).GetScannerPassFull(ctx, passID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Pass{}, ErrPassNotFound
	}
	if err != nil {
		return Pass{}, fmt.Errorf("scannerapp: read pass: %w", err)
	}
	out, err := fillPasses(ctx, db, orgID, []gen.ScannerPassFullRow{row})
	if err != nil {
		return Pass{}, err
	}
	return out[0], nil
}

func fillPasses(ctx context.Context, db gen.DBTX, orgID uuid.UUID, rows []gen.ScannerPassFullRow) ([]Pass, error) {
	out := make([]Pass, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	at := make(map[uuid.UUID]int, len(rows))
	for i, r := range rows {
		ids = append(ids, r.ID)
		at[r.ID] = i
		out = append(out, Pass{
			ID: r.ID, OrgID: r.OrgID, Label: r.Label, Kind: r.Kind,
			ValidFrom: r.ValidFrom, ValidUntil: r.ValidUntil,
			MaxDevices: int(r.MaxDevices), Accepted: int(r.Accepted),
			CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt,
			Sessions: []MePassSession{}, Devices: []PassDevice{},
		})
	}
	q := gen.New(db)
	sessions, err := q.ListScannerPassSessionsOfPasses(ctx, ids, orgID)
	if err != nil {
		return nil, fmt.Errorf("scannerapp: read pass sessions: %w", err)
	}
	for _, s := range sessions {
		i, ok := at[s.PassID]
		if !ok {
			continue
		}
		tiers := s.TierIDs
		if tiers == nil || s.AllTiers {
			tiers = []uuid.UUID{}
		}
		out[i].Sessions = append(out[i].Sessions, MePassSession{SessionID: s.SessionID, EventID: s.EventID, AllTiers: s.AllTiers, TierIDs: tiers})
	}
	devices, err := q.ListLiveScannerDevicesOfPasses(ctx, ids, orgID)
	if err != nil {
		return nil, fmt.Errorf("scannerapp: read pass devices: %w", err)
	}
	for _, d := range devices {
		i, ok := at[d.PassID]
		if !ok {
			continue
		}
		out[i].Devices = append(out[i].Devices, PassDevice{
			ID: d.ID, ControllerName: d.ControllerName, Label: d.Label, Platform: d.Platform,
			CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt,
		})
	}
	return out, nil
}

// UpdatePassParams change a pass; a nil field stays as it is.
type UpdatePassParams struct {
	OrgID      uuid.UUID
	PassID     uuid.UUID
	Label      *string
	ValidFrom  *time.Time
	ValidUntil *time.Time
	MaxDevices *int
	// Sessions, when non-nil, replaces the dates of the pass as a whole; it
	// must name at least one date.
	Sessions *[]CreatePassSession
}

// UpdatePass changes a pass of orgID in ONE nested transaction under the
// pass row lock. Refused: ErrPassNotFound (not of the organization),
// ErrPassRevoked, ErrInvalidPass (blank or long label, max_devices outside
// 1..MaxPassDevices, an empty date list), ErrInvalidWindow (valid_until not
// after valid_from, or any window on a demo pass), ErrMaxDevicesBelowAccepted,
// ErrForeignSession and ErrForeignTier. Nothing is written on a refusal.
func UpdatePass(ctx context.Context, db TxDB, p UpdatePassParams) error {
	var label *string
	if p.Label != nil {
		l := strings.TrimSpace(*p.Label)
		if l == "" || len([]rune(l)) > MaxLabelLen {
			return ErrInvalidPass
		}
		label = &l
	}
	if p.MaxDevices != nil && (*p.MaxDevices < 1 || *p.MaxDevices > MaxPassDevices) {
		return ErrInvalidPass
	}
	var ps *passSessions
	if p.Sessions != nil {
		if len(*p.Sessions) == 0 {
			return ErrInvalidPass
		}
		v := preparePassSessions(*p.Sessions)
		ps = &v
	}
	return inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		row, err := q.LockScannerPassRow(ctx, p.PassID, p.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPassNotFound
		}
		if err != nil {
			return fmt.Errorf("scannerapp: lock pass: %w", err)
		}
		if row.RevokedAt != nil {
			return ErrPassRevoked
		}
		newLabel := row.Label
		if label != nil {
			newLabel = *label
		}
		from, until := row.ValidFrom, row.ValidUntil
		if p.ValidFrom != nil || p.ValidUntil != nil {
			if row.Kind == PassKindDemo {
				return ErrInvalidWindow
			}
			if p.ValidFrom != nil {
				from = p.ValidFrom
			}
			if p.ValidUntil != nil {
				until = p.ValidUntil
			}
			if from == nil || until == nil || !until.After(*from) {
				return ErrInvalidWindow
			}
		}
		maxDevices := row.MaxDevices
		if p.MaxDevices != nil {
			n, err := q.CountLiveScannerDevicesOfPass(ctx, row.ID)
			if err != nil {
				return fmt.Errorf("scannerapp: count devices: %w", err)
			}
			if int64(*p.MaxDevices) < n {
				return ErrMaxDevicesBelowAccepted
			}
			maxDevices = int32(*p.MaxDevices) //nolint:gosec // 1..MaxPassDevices, checked above
		}
		if ps != nil {
			if err := ps.check(ctx, q, p.OrgID); err != nil {
				return err
			}
			if err := q.DeleteScannerPassSessions(ctx, row.ID); err != nil {
				return fmt.Errorf("scannerapp: delete pass sessions: %w", err)
			}
			if err := ps.write(ctx, q, row.ID); err != nil {
				return err
			}
		}
		if err := q.UpdateScannerPassRow(ctx, row.ID, newLabel, from, until, maxDevices); err != nil {
			return fmt.Errorf("scannerapp: update pass: %w", err)
		}
		return nil
	})
}

// PassRevokeResult is what RevokePassWithDevices did.
type PassRevokeResult struct {
	// JustRevoked is true when this call turned the pass off (false: it was
	// already off).
	JustRevoked bool
	// RevokedDeviceIDs are the devices this call took back (pass_revoked).
	RevokedDeviceIDs []uuid.UUID
}

// RevokePassWithDevices turns a pass of orgID off, takes back every live
// device of it (reason pass_revoked, revoked_by_user_id = by) and deletes its
// pairing codes, in ONE nested transaction under the pass row lock. A pass
// that is already off is swept again and reports JustRevoked false. A pass
// of another organization is ErrPassNotFound.
func RevokePassWithDevices(ctx context.Context, db TxDB, orgID, passID uuid.UUID, by *uuid.UUID, now time.Time) (PassRevokeResult, error) {
	var res PassRevokeResult
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		row, err := q.LockScannerPassRow(ctx, passID, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPassNotFound
		}
		if err != nil {
			return fmt.Errorf("scannerapp: lock pass: %w", err)
		}
		if row.RevokedAt == nil {
			n, err := q.RevokeScannerPass(ctx, row.ID, orgID, now, by)
			if err != nil {
				return fmt.Errorf("scannerapp: revoke pass: %w", err)
			}
			res.JustRevoked = n > 0
		}
		ids, err := q.RevokeScannerDevicesOfPass(ctx, row.ID, now, by, ReasonPassRevoked)
		if err != nil {
			return fmt.Errorf("scannerapp: revoke pass devices: %w", err)
		}
		res.RevokedDeviceIDs = ids
		if err := q.DeleteScannerPassCodes(ctx, row.ID); err != nil {
			return fmt.Errorf("scannerapp: delete pass codes: %w", err)
		}
		return nil
	})
	if err != nil {
		return PassRevokeResult{}, err
	}
	if res.RevokedDeviceIDs == nil {
		res.RevokedDeviceIDs = []uuid.UUID{}
	}
	return res, nil
}

// DefaultPassWindow is the window of a new standard pass when the request
// names none: from the earliest doors_open_at (else start_at) of the dates
// minus PassWindowBefore until the latest start_at plus PassWindowAfter.
// Every date must be a live date of orgID, else ErrForeignSession; no dates
// is ErrInvalidPass.
func DefaultPassWindow(ctx context.Context, db gen.DBTX, orgID uuid.UUID, sessionIDs []uuid.UUID) (from, until time.Time, err error) {
	if len(sessionIDs) == 0 {
		return time.Time{}, time.Time{}, ErrInvalidPass
	}
	distinct := make(map[uuid.UUID]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		distinct[id] = true
	}
	w, err := gen.New(db).ScannerSessionsWindow(ctx, orgID, sessionIDs)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("scannerapp: read session times: %w", err)
	}
	if w.Count != int64(len(distinct)) || w.EarliestIn == nil || w.LatestStart == nil {
		return time.Time{}, time.Time{}, ErrForeignSession
	}
	return w.EarliestIn.Add(-PassWindowBefore), w.LatestStart.Add(PassWindowAfter), nil
}

package scannerapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Pair attempt outcomes (scanner_pair_attempts.outcome).
const (
	OutcomeOK       = "ok"
	OutcomeNotFound = "not_found"
	OutcomePassFull = "pass_full"
)

// The pairing attempt limit (spec 69 MA-11). Counted in the database, so it
// holds across API instances.
const (
	// PairAttemptWindow is the window the limit counts in.
	PairAttemptWindow = 15 * time.Minute
	// MaxFailedPairsPerIP: not_found attempts of one address in the window.
	MaxFailedPairsPerIP = 10
	// MaxPairsPerIP: all attempts of one address in the window.
	MaxPairsPerIP = 30
	// MaxFailedPairsPerCode: not_found and pass_full attempts of one code in
	// the window.
	MaxFailedPairsPerCode = 10
	// PairAttemptRetention: older attempts are deleted on every write.
	PairAttemptRetention = 24 * time.Hour
)

// codeInsertTries is how many fresh codes are tried when a random code hits
// an existing one (31^8 codes, practically never).
const codeInsertTries = 3

// IssuedCode is a pairing code shown once. Code is the raw normalized code;
// the link is PairLink(base, Code).
type IssuedCode struct {
	Code      string
	ExpiresAt time.Time
	// PassReenabled is true when IssuePassCode turned a revoked pass back on.
	PassReenabled bool
}

// IssueOrganizerCode issues a one-time pairing code for the person's own
// phone in orgID, working for OrganizerCodeTTL. The person needs an active
// membership in orgID whose role holds scan.perform (platform roles do not
// count), else ErrMembershipRequired. The person's other unused codes in the
// organization are deleted in the same transaction.
func IssueOrganizerCode(ctx context.Context, db TxDB, orgID, userID uuid.UUID, now time.Time) (IssuedCode, error) {
	var out IssuedCode
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		ok, err := q.UserCanScanInOrg(ctx, userID, orgID)
		if err != nil {
			return fmt.Errorf("scannerapp: check membership: %w", err)
		}
		if !ok {
			return ErrMembershipRequired
		}
		if err := q.DeleteOpenScannerOrganizerCodes(ctx, orgID, userID); err != nil {
			return fmt.Errorf("scannerapp: delete open codes: %w", err)
		}
		uid := userID
		exp := now.Add(OrganizerCodeTTL)
		code, err := insertCode(ctx, tx, gen.InsertScannerPairingCodeParams{
			OrgID: orgID, UserID: &uid, CreatedByUserID: &uid, CreatedAt: now, ExpiresAt: exp,
		})
		if err != nil {
			return err
		}
		out = IssuedCode{Code: code, ExpiresAt: exp}
		return nil
	})
	if err != nil {
		return IssuedCode{}, err
	}
	return out, nil
}

// IssuePassCode issues the pairing code of a pass of orgID: a code many
// phones may use until the pass is full, stored with PassCodeTTL (the window
// of the pass decides). Every earlier code of the pass is deleted, so one
// code is live per pass. A revoked pass is turned back on
// (IssuedCode.PassReenabled). Refused: a pass not of the organization
// (ErrPassNotFound) and a standard pass whose window is over
// (ErrPassWindowEnded). The pass row is locked FOR UPDATE.
func IssuePassCode(ctx context.Context, db TxDB, orgID, passID uuid.UUID, by *uuid.UUID, now time.Time) (IssuedCode, error) {
	var out IssuedCode
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		p, err := q.LockScannerPassRow(ctx, passID, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPassNotFound
		}
		if err != nil {
			return fmt.Errorf("scannerapp: lock pass: %w", err)
		}
		if windowEnded(p.Kind, p.ValidUntil, now) {
			return ErrPassWindowEnded
		}
		if p.RevokedAt != nil {
			if err := q.ReenableScannerPass(ctx, p.ID); err != nil {
				return fmt.Errorf("scannerapp: re-enable pass: %w", err)
			}
			out.PassReenabled = true
		}
		if err := q.DeleteScannerPassCodes(ctx, p.ID); err != nil {
			return fmt.Errorf("scannerapp: delete pass codes: %w", err)
		}
		pid := p.ID
		exp := now.Add(PassCodeTTL)
		code, err := insertCode(ctx, tx, gen.InsertScannerPairingCodeParams{
			OrgID: orgID, PassID: &pid, CreatedByUserID: by, CreatedAt: now, ExpiresAt: exp,
		})
		if err != nil {
			return err
		}
		out.Code, out.ExpiresAt = code, exp
		return nil
	})
	if err != nil {
		return IssuedCode{}, err
	}
	return out, nil
}

// insertCode stores a fresh code under a SAVEPOINT and tries another one
// when the random code is already taken.
func insertCode(ctx context.Context, tx pgx.Tx, p gen.InsertScannerPairingCodeParams) (string, error) {
	for i := 0; i < codeInsertTries; i++ {
		code, err := NewCode()
		if err != nil {
			return "", fmt.Errorf("scannerapp: new code: %w", err)
		}
		p.CodeHash = HashCode(code)
		err = inTx(ctx, tx, func(sp pgx.Tx) error { return gen.New(sp).InsertScannerPairingCode(ctx, p) })
		if err == nil {
			return code, nil
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			continue
		}
		return "", fmt.Errorf("scannerapp: insert code: %w", err)
	}
	return "", errors.New("scannerapp: insert code: no free code")
}

// windowEnded reports whether a pass of this kind and valid_until can no
// longer check at now (a demo pass has no window).
func windowEnded(kind string, validUntil *time.Time, now time.Time) bool {
	return kind != PassKindDemo && (validUntil == nil || !now.Before(*validUntil))
}

// PairParams is the body of a pairing-code exchange.
type PairParams struct {
	// Code is the code as typed; it is normalized here.
	Code     string
	Platform string // PlatformIOS | PlatformAndroid | PlatformOther
	// DeviceLabel may be empty; at most MaxLabelLen characters.
	DeviceLabel string
	// ControllerName is kept for a pass code and ignored for an organizer
	// code; at most MaxLabelLen characters.
	ControllerName string
}

// PairResult is a new device with its tokens, shown once.
type PairResult struct {
	Device           Device
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Pair exchanges a pairing code for a new device in ONE transaction. The code
// row is locked FOR UPDATE; for a pass code the pass row is locked too, so
// two phones on the last place serialize: one gets the device, the other
// ErrPassFull. Refused with ErrInvalidDevice (label, controller name or
// platform wrong: a bad body, not counted as an attempt) and ErrCodeNotFound
// (unknown, wrong shape, used or expired organizer code, the person lost
// scan.perform, pass revoked or ended, organization archived). The attempt
// limit and the attempt record are the caller's: CheckPairLimit before,
// RecordPairAttempt with PairOutcome(err) after.
func Pair(ctx context.Context, db TxDB, p PairParams, now time.Time) (PairResult, error) {
	label, err := checkDevice(DeviceParams{Label: p.DeviceLabel, Platform: p.Platform})
	if err != nil {
		return PairResult{}, err
	}
	controller := strings.TrimSpace(p.ControllerName)
	if len([]rune(controller)) > MaxLabelLen {
		return PairResult{}, ErrInvalidDevice
	}
	if _, ok := NormalizeCode(p.Code); !ok {
		return PairResult{}, ErrCodeNotFound
	}
	var res PairResult
	err = inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		c, err := q.LockScannerPairingCode(ctx, HashCode(p.Code))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCodeNotFound
		}
		if err != nil {
			return fmt.Errorf("scannerapp: lock code: %w", err)
		}
		if c.OrgArchived || !c.ExpiresAt.After(now) {
			return ErrCodeNotFound
		}
		ins := gen.InsertPairedScannerDeviceParams{
			OrgID: c.OrgID, Label: label, Platform: p.Platform, CreatedAt: now,
			AccessExpiresAt: now.Add(AccessTokenTTL), RefreshExpiresAt: now.Add(RefreshTokenTTL),
		}
		d := Device{OrgID: c.OrgID, Label: label, Platform: p.Platform, PairConfirmed: true}
		switch {
		case c.UserID != nil:
			if c.UsedAt != nil {
				return ErrCodeNotFound
			}
			ok, err := q.UserCanScanInOrg(ctx, *c.UserID, c.OrgID)
			if err != nil {
				return fmt.Errorf("scannerapp: check membership: %w", err)
			}
			if !ok {
				return ErrCodeNotFound
			}
			ins.UserID = c.UserID
			d.UserID, d.Kind = c.UserID, KindOrganizer
		case c.PassID != nil:
			ps, err := q.LockScannerPass(ctx, *c.PassID, c.OrgID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrCodeNotFound
			}
			if err != nil {
				return fmt.Errorf("scannerapp: lock pass: %w", err)
			}
			if ps.RevokedAt != nil || windowEnded(ps.Kind, ps.ValidUntil, now) {
				return ErrCodeNotFound
			}
			n, err := q.CountLiveScannerDevicesOfPass(ctx, ps.ID)
			if err != nil {
				return fmt.Errorf("scannerapp: count devices: %w", err)
			}
			if n >= int64(ps.MaxDevices) {
				return ErrPassFull
			}
			ins.PassID, ins.ControllerName = c.PassID, controller
			d.PassID, d.Kind, d.ControllerName = c.PassID, KindPass, controller
		default:
			return ErrCodeNotFound
		}
		access, accessHash, err := NewToken()
		if err != nil {
			return fmt.Errorf("scannerapp: new token: %w", err)
		}
		refresh, refreshHash, err := NewRefreshToken()
		if err != nil {
			return fmt.Errorf("scannerapp: new refresh token: %w", err)
		}
		ins.AccessTokenHash, ins.RefreshTokenHash = accessHash, refreshHash
		id, err := q.InsertPairedScannerDevice(ctx, ins)
		if err != nil {
			return fmt.Errorf("scannerapp: insert device: %w", err)
		}
		if c.UserID != nil {
			if err := q.MarkScannerPairingCodeUsed(ctx, c.ID, now, id); err != nil {
				return fmt.Errorf("scannerapp: spend code: %w", err)
			}
		}
		d.ID = id
		res = PairResult{
			Device: d, AccessToken: access, AccessExpiresAt: ins.AccessExpiresAt,
			RefreshToken: refresh, RefreshExpiresAt: ins.RefreshExpiresAt,
		}
		return nil
	})
	if err != nil {
		return PairResult{}, err
	}
	return res, nil
}

// PairOutcome maps the result of Pair to the outcome RecordPairAttempt
// stores. counted is false for an error that is not an attempt of a code
// (a bad body, a database failure).
func PairOutcome(err error) (outcome string, counted bool) {
	switch {
	case err == nil:
		return OutcomeOK, true
	case errors.Is(err, ErrCodeNotFound):
		return OutcomeNotFound, true
	case errors.Is(err, ErrPassFull):
		return OutcomePassFull, true
	default:
		return "", false
	}
}

// CheckPairLimit refuses a pairing attempt before the code is looked at,
// with *TooManyAttemptsError (errors.Is ErrTooManyAttempts), when in the
// last PairAttemptWindow the address (ipHash = HashIP) made
// MaxFailedPairsPerIP not_found attempts or MaxPairsPerIP attempts of any
// outcome, or the code (codeHash = HashCode of the typed code) had
// MaxFailedPairsPerCode not_found / pass_full attempts. RetryAfter is the
// time until the oldest counted attempt leaves the window, at least one
// second. A refusal is not recorded.
func CheckPairLimit(ctx context.Context, db gen.DBTX, ipHash, codeHash string, now time.Time) error {
	q := gen.New(db)
	since := now.Add(-PairAttemptWindow)
	ip, err := q.ScannerPairAttemptsByIP(ctx, ipHash, since)
	if err != nil {
		return fmt.Errorf("scannerapp: count attempts of address: %w", err)
	}
	nCode, oldestCode, err := q.ScannerPairAttemptsByCode(ctx, codeHash, since)
	if err != nil {
		return fmt.Errorf("scannerapp: count attempts of code: %w", err)
	}
	var wait time.Duration
	hit := false
	consider := func(over bool, oldest *time.Time) {
		if !over {
			return
		}
		hit = true
		if d := retryAfter(oldest, now); d > wait {
			wait = d
		}
	}
	consider(ip.Failed >= MaxFailedPairsPerIP, ip.OldestFailed)
	consider(ip.All >= MaxPairsPerIP, ip.OldestAll)
	consider(nCode >= MaxFailedPairsPerCode, oldestCode)
	if !hit {
		return nil
	}
	return &TooManyAttemptsError{RetryAfter: wait}
}

// retryAfter is the time until the attempt made at oldest leaves the window,
// rounded up to a whole second and at least one second.
func retryAfter(oldest *time.Time, now time.Time) time.Duration {
	if oldest == nil {
		return time.Second
	}
	d := oldest.Add(PairAttemptWindow).Sub(now)
	if r := d % time.Second; r != 0 {
		d += time.Second - r
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}

// RecordPairAttempt records one exchange of a code (outcome OutcomeOK,
// OutcomeNotFound or OutcomePassFull) and deletes the attempts older than
// PairAttemptRetention.
func RecordPairAttempt(ctx context.Context, db gen.DBTX, ipHash, codeHash, outcome string, now time.Time) error {
	switch outcome {
	case OutcomeOK, OutcomeNotFound, OutcomePassFull:
	default:
		return ErrInvalidOutcome
	}
	q := gen.New(db)
	if err := q.InsertScannerPairAttempt(ctx, ipHash, codeHash, outcome, now); err != nil {
		return fmt.Errorf("scannerapp: record attempt: %w", err)
	}
	if err := q.DeleteOldScannerPairAttempts(ctx, now.Add(-PairAttemptRetention)); err != nil {
		return fmt.Errorf("scannerapp: delete old attempts: %w", err)
	}
	return nil
}

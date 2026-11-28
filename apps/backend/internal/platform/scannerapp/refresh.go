package scannerapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// RetiredRefreshRetention is how long a replaced refresh token is kept to
// catch a stolen copy; older rows of a device are deleted on its refresh.
const RetiredRefreshRetention = RefreshTokenTTL

// RefreshResult is a new token pair of a device, shown once.
type RefreshResult struct {
	DeviceID         uuid.UUID
	OrgID            uuid.UUID
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Refresh exchanges a refresh token for a new token pair in ONE transaction;
// the device row is locked FOR UPDATE, so refreshes of one device
// serialize. The device keeps its previous refresh token alive until it
// uses the new access token (ConfirmPair), so a lost answer can be retried
// with the old token; two refreshes with the same token both succeed and
// only the later pair works.
//
// Refusals (every one is 401 scanner_app.device_revoked for the device):
//   - ErrMalformed: not the shape of a refresh token;
//   - ErrUnknown: no device has this token, current, previous or retired;
//   - *RevokedError (errors.Is ErrRevoked): the device is revoked. When this
//     call revoked it, JustRevoked is true and the revoke is COMMITTED with
//     the reason: refresh_token_reuse (a retired token came back),
//     refresh_expired, org_archived, membership_ended, pass_revoked or
//     pass_ended. A closed pass revokes its devices only once its upload
//     term ended (DeviceStateOf): pass_revoked when it was turned off,
//     pass_ended when its window ended.
//
// Inside the upload term of a closed pass the refresh succeeds and the new
// access token expires at the end of the term at the latest.
func Refresh(ctx context.Context, db TxDB, raw string, now time.Time) (RefreshResult, error) {
	if !LooksLikeRefreshToken(raw) {
		return RefreshResult{}, ErrMalformed
	}
	h := HashToken(raw)
	var (
		res     RefreshResult
		refusal error
	)
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		q := gen.New(tx)
		id, err := q.FindScannerDeviceByRefresh(ctx, h)
		if errors.Is(err, pgx.ErrNoRows) {
			refusal, err = revokeOnRetired(ctx, q, h, now)
			return err
		}
		if err != nil {
			return fmt.Errorf("scannerapp: find refresh token: %w", err)
		}
		row, err := q.LockScannerDeviceForRefresh(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			refusal = ErrUnknown
			return nil
		}
		if err != nil {
			return fmt.Errorf("scannerapp: lock device: %w", err)
		}
		if row.RevokedAt != nil {
			refusal = &RevokedError{DeviceID: row.ID, OrgID: row.OrgID, Reason: deref(row.RevokeReason)}
			return nil
		}
		// Decided again under the lock: a refresh that waited sees the
		// state the other one left.
		isCurrent := row.RefreshTokenHash != nil && *row.RefreshTokenHash == h
		isPrev := row.PrevRefreshTokenHash != nil && *row.PrevRefreshTokenHash == h
		if !isCurrent && !isPrev {
			refusal, err = revokeOnRetired(ctx, q, h, now)
			return err
		}
		reason, uploadUntil := refreshRefusal(row, now)
		if reason != "" {
			n, err := q.RevokeScannerDevice(ctx, row.ID, row.OrgID, now, nil, &reason)
			if err != nil {
				return fmt.Errorf("scannerapp: revoke device: %w", err)
			}
			refusal = &RevokedError{DeviceID: row.ID, OrgID: row.OrgID, Reason: reason, JustRevoked: n > 0}
			return nil
		}

		retire := func(hash string) error {
			if err := q.InsertRetiredScannerRefreshToken(ctx, hash, row.ID, now); err != nil {
				return fmt.Errorf("scannerapp: retire refresh token: %w", err)
			}
			return nil
		}
		var prev *string
		if isCurrent {
			// The previous token is replaced by the one just presented.
			if row.PrevRefreshTokenHash != nil {
				if err := retire(*row.PrevRefreshTokenHash); err != nil {
					return err
				}
			}
			cur := h
			prev = &cur
		} else {
			// The previous token came back before the new pair was used:
			// the unconfirmed pair is dropped, the previous token stays.
			if row.RefreshTokenHash != nil {
				if err := retire(*row.RefreshTokenHash); err != nil {
					return err
				}
			}
			prev = row.PrevRefreshTokenHash
		}
		access, accessHash, err := NewToken()
		if err != nil {
			return fmt.Errorf("scannerapp: new token: %w", err)
		}
		refresh, refreshHash, err := NewRefreshToken()
		if err != nil {
			return fmt.Errorf("scannerapp: new refresh token: %w", err)
		}
		accessExp := now.Add(AccessTokenTTL)
		if uploadUntil != nil && uploadUntil.Before(accessExp) {
			// A device of a closed pass: its access token never outlives
			// the upload term.
			accessExp = *uploadUntil
		}
		rot := gen.RotateScannerDeviceTokensParams{
			ID: row.ID, AccessTokenHash: accessHash, AccessExpiresAt: accessExp,
			RefreshTokenHash: refreshHash, RefreshExpiresAt: now.Add(RefreshTokenTTL),
			PrevRefreshTokenHash: prev, RefreshedAt: now,
		}
		if err := q.RotateScannerDeviceTokens(ctx, rot); err != nil {
			return fmt.Errorf("scannerapp: rotate tokens: %w", err)
		}
		if err := q.DeleteOldRetiredScannerRefreshTokens(ctx, row.ID, now.Add(-RetiredRefreshRetention)); err != nil {
			return fmt.Errorf("scannerapp: delete old retired tokens: %w", err)
		}
		res = RefreshResult{
			DeviceID: row.ID, OrgID: row.OrgID,
			AccessToken: access, AccessExpiresAt: rot.AccessExpiresAt,
			RefreshToken: refresh, RefreshExpiresAt: rot.RefreshExpiresAt,
		}
		return nil
	})
	if err != nil {
		return RefreshResult{}, err
	}
	if refusal != nil {
		return RefreshResult{}, refusal
	}
	return res, nil
}

// revokeOnRetired revokes the device of a retired refresh token
// (refresh_token_reuse) and returns the refusal; ErrUnknown when the token
// is not retired either.
func revokeOnRetired(ctx context.Context, q *gen.Queries, h string, now time.Time) (error, error) {
	deviceID, orgID, stored, err := q.FindRetiredScannerRefreshToken(ctx, h)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknown, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scannerapp: find retired token: %w", err)
	}
	reason := ReasonRefreshTokenReuse
	n, err := q.RevokeScannerDevice(ctx, deviceID, orgID, now, nil, &reason)
	if err != nil {
		return nil, fmt.Errorf("scannerapp: revoke device: %w", err)
	}
	if n == 0 {
		return &RevokedError{DeviceID: deviceID, OrgID: orgID, Reason: deref(stored)}, nil
	}
	return &RevokedError{DeviceID: deviceID, OrgID: orgID, Reason: reason, JustRevoked: true}, nil
}

// refreshRefusal is the revoke reason when the device may no longer refresh,
// "" when it may. For a device of a closed pass inside its upload term it
// also returns the end of that term, the latest access_expires_at the new
// pair may get (nil: no cap).
func refreshRefusal(row gen.ScannerDeviceRefreshRow, now time.Time) (string, *time.Time) {
	switch {
	case row.RefreshExpiresAt == nil || !row.RefreshExpiresAt.After(now):
		return ReasonRefreshExpired, nil
	case row.OrgArchived:
		return ReasonOrgArchived, nil
	case row.UserID != nil:
		if !row.UserCanScan {
			return ReasonMembershipEnded, nil
		}
	case row.PassID != nil:
		if row.PassKind == nil {
			return ReasonPassRevoked, nil
		}
		_, c, err := DeviceStateOf(*row.PassKind, row.PassValidUntil, row.PassRevokedAt, now)
		if err != nil {
			if c != nil && c.Revoked {
				return ReasonPassRevoked, nil
			}
			return ReasonPassEnded, nil
		}
		if c != nil {
			upload := c.UploadUntil
			return "", &upload
		}
	default:
		return ReasonMembershipEnded, nil
	}
	return "", nil
}

// ConfirmPair marks the current token pair of a device confirmed once the
// device used its access token (raw), and retires the previous refresh
// token, in ONE statement. It reports false when there was nothing to
// confirm (already confirmed, or raw is no longer the device's access
// token). The device-token middleware calls it when Device.PairConfirmed is
// false; a failure is logged and the request goes on.
func ConfirmPair(ctx context.Context, db gen.DBTX, deviceID uuid.UUID, rawAccessToken string, now time.Time) (bool, error) {
	n, err := gen.New(db).ConfirmScannerDevicePair(ctx, deviceID, HashToken(rawAccessToken), now)
	if err != nil {
		return false, fmt.Errorf("scannerapp: confirm pair: %w", err)
	}
	return n > 0, nil
}

// RevokeSelf takes back the device itself (DELETE /v1/scanner-app/device,
// reason self_unpaired); a pass device frees its place. It reports false
// when the device was already revoked.
func RevokeSelf(ctx context.Context, db gen.DBTX, orgID, deviceID uuid.UUID, now time.Time) (bool, error) {
	return RevokeDevice(ctx, db, orgID, deviceID, nil, ReasonSelfUnpaired, now)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

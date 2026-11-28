package scannerapp

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Pairing, refresh and management refusals (MA-11 / MA-12).
var (
	// ErrMembershipRequired: the person has no active membership in the
	// organization whose role holds scan.perform (platform roles do not
	// count), so no organizer pairing code is issued.
	ErrMembershipRequired = errors.New("scannerapp: membership with scan.perform required")
	// ErrCodeNotFound: the pairing code is unknown, of the wrong shape, used,
	// expired, or the right it stands for is gone (no membership, pass
	// revoked or ended, organization archived). One answer for all of them.
	ErrCodeNotFound = errors.New("scannerapp: pairing code not found")
	// ErrPassFull: the pass already has max_devices live devices.
	ErrPassFull = errors.New("scannerapp: pass is full")
	// ErrPassWindowEnded: a new link was asked for a standard pass whose
	// window is over; the window must be moved first.
	ErrPassWindowEnded = errors.New("scannerapp: pass window ended")
	// ErrInvalidWindow: valid_until is not after valid_from, or a window was
	// given for a demo pass.
	ErrInvalidWindow = errors.New("scannerapp: invalid pass window")
	// ErrMaxDevicesBelowAccepted: max_devices would be lower than the number
	// of live devices of the pass.
	ErrMaxDevicesBelowAccepted = errors.New("scannerapp: max_devices below the accepted devices")
	// ErrDeviceNotFound: no such device in the organization, or not a device
	// of the person when only their own devices may be touched.
	ErrDeviceNotFound = errors.New("scannerapp: device not found")
	// ErrTooManyAttempts: the pairing attempt limit is reached; errors.As
	// gives *TooManyAttemptsError with the wait.
	ErrTooManyAttempts = errors.New("scannerapp: too many pairing attempts")
	// ErrInvalidOutcome: RecordPairAttempt got an outcome it does not know.
	ErrInvalidOutcome = errors.New("scannerapp: invalid pair attempt outcome")
)

// Revoke reasons (scanner_devices.revoke_reason, metadata.reason of the
// v1.scanner_app.device_revoke audit event).
const (
	ReasonRefreshTokenReuse = "refresh_token_reuse"
	ReasonRefreshExpired    = "refresh_expired"
	ReasonOrgArchived       = "org_archived"
	ReasonMembershipEnded   = "membership_ended"
	ReasonPassRevoked       = "pass_revoked"
	ReasonPassEnded         = "pass_ended"
	ReasonSelfUnpaired      = "self_unpaired"
	ReasonRevokedByUser     = "revoked_by_user"
)

// RevokedError is a refresh refused because the device is revoked.
// JustRevoked is true when THIS call revoked it (the caller writes the
// device_revoke audit event); false when it was revoked before. Reason is
// the stored revoke reason (may be empty for an old row). errors.Is(err,
// ErrRevoked) holds.
type RevokedError struct {
	DeviceID    uuid.UUID
	OrgID       uuid.UUID
	Reason      string
	JustRevoked bool
}

func (e *RevokedError) Error() string {
	return fmt.Sprintf("scannerapp: device %s revoked (%s)", e.DeviceID, e.Reason)
}

// Is makes errors.Is(err, ErrRevoked) true.
func (e *RevokedError) Is(target error) bool { return target == ErrRevoked }

// TooManyAttemptsError is a pairing refused by the attempt limit.
// RetryAfter is at least one second. errors.Is(err, ErrTooManyAttempts)
// holds.
type TooManyAttemptsError struct {
	RetryAfter time.Duration
}

func (e *TooManyAttemptsError) Error() string {
	return fmt.Sprintf("scannerapp: too many pairing attempts, retry after %s", e.RetryAfter)
}

// Is makes errors.Is(err, ErrTooManyAttempts) true.
func (e *TooManyAttemptsError) Is(target error) bool { return target == ErrTooManyAttempts }

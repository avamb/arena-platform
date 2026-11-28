package scannerapp

import "time"

// PassUploadGrace is how long the devices of a closed pass keep working in
// the "upload only" state after the pass closed (spec 69 B.1): long enough
// to upload the scans a phone kept offline.
const PassUploadGrace = 24 * time.Hour

// DeviceState is what a device that authenticates may do.
type DeviceState string

const (
	// StateActive: the device works in full.
	StateActive DeviceState = "active"
	// StateUploadOnly: the pass of the device is closed; until
	// PassUploadUntil the device may refresh its tokens, read GET /me and
	// upload its queue, nothing else.
	StateUploadOnly DeviceState = "upload_only"
)

// Pass states as the app and the organizer see them.
const (
	PassStateActive = "active"
	PassStateClosed = "closed"
)

// PassClosure tells when a closed pass closed.
type PassClosure struct {
	// ClosedAt is revoked_at for a pass that was turned off, valid_until for
	// a standard pass whose window ended; the earlier one when both apply.
	ClosedAt time.Time
	// UploadUntil is ClosedAt + PassUploadGrace.
	UploadUntil time.Time
	// Revoked is true when the pass is closed because it was turned off
	// (revoked_at is ClosedAt); false when its window ended first.
	Revoked bool
}

// ClosureOf reports whether a pass of this kind, window end and revoke time
// is closed at now and, when it is, when it closed. A pass is closed when it
// is turned off, or when it is a standard pass and now is at or after
// valid_until; a demo pass closes only by being turned off. ok is true for a
// closed pass. A standard pass with no valid_until is closed since ever
// (zero ClosedAt): its devices get no upload term.
func ClosureOf(kind string, validUntil, revokedAt *time.Time, now time.Time) (PassClosure, bool) {
	var (
		c      PassClosure
		closed bool
	)
	if kind != PassKindDemo {
		switch {
		case validUntil == nil:
			closed = true // zero ClosedAt: no upload term
		case !now.Before(*validUntil):
			c.ClosedAt, closed = *validUntil, true
		}
	}
	if revokedAt != nil && (!closed || revokedAt.Before(c.ClosedAt)) {
		c.ClosedAt, c.Revoked, closed = *revokedAt, true, true
	}
	if !closed {
		return PassClosure{}, false
	}
	c.UploadUntil = c.ClosedAt.Add(PassUploadGrace)
	return c, true
}

// DeviceStateOf is THE predicate of a pass device's state: StateActive while
// the pass is open, StateUploadOnly from the close of the pass until its
// upload term ends (the closure is returned), and ErrAccessEnded after it.
// Authenticate, Refresh and every write that may reopen a pass decide by
// it. An organizer device has no pass: it is StateActive here, and its
// membership is checked apart.
func DeviceStateOf(kind string, validUntil, revokedAt *time.Time, now time.Time) (DeviceState, *PassClosure, error) {
	c, closed := ClosureOf(kind, validUntil, revokedAt, now)
	if !closed {
		return StateActive, nil, nil
	}
	if !now.Before(c.UploadUntil) {
		return "", &c, ErrAccessEnded
	}
	return StateUploadOnly, &c, nil
}

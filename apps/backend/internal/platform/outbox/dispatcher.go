package outbox

import (
	"context"
	"errors"
)

// Dispatcher delivers outbox events to external targets.
//
// The real worker loop (FOR UPDATE SKIP LOCKED fan-out) is out of scope for
// the foundation milestone.  Wire NoopDispatcher until the OutboxDispatcher
// worker is implemented in a later milestone.
//
// Implementations must be safe for concurrent use.
type Dispatcher interface {
	// Dispatch delivers a single outbox event.  Returning a non-nil error
	// signals that the event could not be delivered; the worker loop will
	// increment the attempts counter and retry up to its configured limit.
	Dispatch(ctx context.Context, event Event) error
}

// -----------------------------------------------------------------------------
// NoopDispatcher — placeholder for the foundation milestone
// -----------------------------------------------------------------------------

// NoopDispatcher is a Dispatcher that does nothing and always returns nil.
// It satisfies the interface so the application can start without a real
// delivery backend.
type NoopDispatcher struct{}

// Dispatch is a no-op that always succeeds.
func (NoopDispatcher) Dispatch(_ context.Context, _ Event) error { return nil }

// Compile-time interface guard.
var _ Dispatcher = NoopDispatcher{}

// =============================================================================
// ErrDispatchDisabled / DisabledDispatcher — explicit disabled mode (PR-04)
// =============================================================================

// ErrDispatchDisabled is returned by DisabledDispatcher.Dispatch.
// OutboxEventsDispatcher uses this sentinel to distinguish disabled-mode
// calls from transient failures: neither MarkDispatched nor MarkFailed is
// called, leaving the row completely untouched in the database.
var ErrDispatchDisabled = errors.New("outbox: dispatch is explicitly disabled (OUTBOX_MODE=disabled)")

// DisabledModeDispatcher is an optional interface that Dispatcher implementations
// can implement to signal to OutboxEventsDispatcher.Run that no rows should be
// claimed from the queue. This ensures disabled mode never consumes or locks any
// outbox_events rows, even transiently.
type DisabledModeDispatcher interface {
	IsDisabled() bool
}

// DisabledDispatcher is the production Dispatcher returned when OUTBOX_MODE=disabled.
//
//   - Dispatch always returns ErrDispatchDisabled.
//   - IsDisabled returns true so OutboxEventsDispatcher.Run skips ClaimNext
//     entirely (rows accumulate unprocessed until the mode is changed).
//
// This guarantees that disabled mode never causes any row to receive
// processed_at — the dual contract of PR-04.
type DisabledDispatcher struct{}

// Dispatch returns ErrDispatchDisabled. OutboxEventsDispatcher treats this
// sentinel by skipping both MarkDispatched and MarkFailed, leaving the row
// completely untouched.
func (DisabledDispatcher) Dispatch(_ context.Context, _ Event) error {
	return ErrDispatchDisabled
}

// IsDisabled implements DisabledModeDispatcher. OutboxEventsDispatcher.Run
// checks this before each ClaimNext call and skips it when true.
func (DisabledDispatcher) IsDisabled() bool { return true }

// Compile-time interface guards.
var _ Dispatcher = DisabledDispatcher{}
var _ DisabledModeDispatcher = DisabledDispatcher{}

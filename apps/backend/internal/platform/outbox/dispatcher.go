package outbox

import "context"

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

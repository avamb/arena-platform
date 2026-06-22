package audit

import (
	"context"
	"time"
)

// WriteEvent is a convenience helper for workflows and command handlers. It
// constructs an Event from the supplied named fields and calls w.Write. The
// caller does not need to assemble the Event struct directly.
//
// OccurredAt is always set to the current UTC time by WriteEvent; pass a custom
// Event directly if you need a different timestamp.
//
// Returns any error from the underlying Writer implementation.
func WriteEvent(ctx context.Context, w Writer, actorType, actorID, action, resourceType, resourceID string, metadata map[string]any) error {
	ev := Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     metadata,
	}
	return w.Write(ctx, ev)
}

// NewEvent constructs a fully-populated Event value with OccurredAt set to
// the current UTC time. Use this as a convenience constructor inside command
// handlers before passing the event to Writer.WriteTx.
func NewEvent(actorType, actorID, action, resourceType, resourceID string) Event {
	return Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     map[string]any{},
	}
}

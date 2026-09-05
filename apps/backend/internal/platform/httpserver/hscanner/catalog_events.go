// catalog_events.go — catalog-change outbox events (W1-B7c, feature #506).
//
// The migrated WordPress sites mirror our catalog. They cannot poll it cheaply
// (spec §9.2 has them re-read a session through the gateway on notification),
// so an event or session that changes shape must SAY so. These are the
// producers of that notification; the bil24_wp dispatcher turns them into
// Bil24's event.created / event.changed / event.deleted.
//
// They live in hscanner next to the ticket and session events because that is
// where PublishScannerEvent — the best-effort short-transaction outbox append —
// already lives. hcatalog reaches them through an injected callback rather than
// an import, which is what keeps the two HTTP sub-packages acyclic.
package hscanner

import (
	"context"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

const (
	// EventPublishedEventType is emitted when an event first becomes publicly
	// visible. A subscribed site materialises it as a new product.
	EventPublishedEventType = "v1.event.published"

	// EventUpdatedEventType is emitted when an already-published event's
	// description or status changes in a way a mirror must re-read.
	EventUpdatedEventType = "v1.event.updated"

	// SessionUpdatedEventType is emitted when one session of an event changes
	// (time, venue, status) without being cancelled — a cancellation is the
	// separate SessionCancelledEventType, which the sites read as a deletion.
	SessionUpdatedEventType = "v1.session.updated"

	// EventAggregateType is the aggregate_type of the v1.event.* rows.
	EventAggregateType = "event"
)

// BuildCatalogEventPayload is the canonical payload of every catalog
// notification: WHICH event changed, whose it is, and — when the producer
// knows — exactly which sessions are affected.
//
// It deliberately carries no catalog CONTENT. The consumer re-reads the
// sessions through the gateway, so a payload that sat in the outbox through a
// retry backoff can never deliver a stale name or price.
func BuildCatalogEventPayload(eventID, orgID string, sessionIDs []string) map[string]any {
	payload := map[string]any{"event_id": eventID}
	if orgID != "" {
		payload["org_id"] = orgID
	}
	if len(sessionIDs) > 0 {
		payload["session_ids"] = sessionIDs
	}
	return payload
}

// PublishCatalogEvent emits one v1.event.* / v1.session.updated outbox row.
// Best-effort, like every other publisher here: a catalog edit must not fail
// because a mirror could not be told about it.
//
// An empty session_ids list is meaningful rather than lossy — the dispatcher
// then resolves every session of the event, which is the right answer for a
// newly published event whose sessions are all new.
func (h *Handler) PublishCatalogEvent(ctx context.Context, eventType, eventID, orgID string, sessionIDs []string) {
	if eventType == "" || eventID == "" {
		return
	}
	h.PublishScannerEvent(ctx, outbox.Event{
		AggregateType: EventAggregateType,
		AggregateID:   eventID,
		EventType:     eventType,
		Payload:       BuildCatalogEventPayload(eventID, orgID, sessionIDs),
	})
}

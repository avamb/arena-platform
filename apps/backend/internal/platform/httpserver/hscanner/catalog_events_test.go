package hscanner

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
)

// The payload is the dispatcher's entire input, so its key set is a contract
// between the two packages, not a formatting detail.
func TestBuildCatalogEventPayload_CarriesIdentityAndNoContent(t *testing.T) {
	const eventID = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	const orgID = "1b9d6bcd-bbfd-4b2d-9b5d-ab8dfbbd4bed"
	sessions := []string{"aaaaaaaa-0000-4000-8000-000000000001"}

	got := BuildCatalogEventPayload(eventID, orgID, sessions)

	if got["event_id"] != eventID {
		t.Errorf("event_id = %v, want %s", got["event_id"], eventID)
	}
	if got["org_id"] != orgID {
		t.Errorf("org_id = %v, want %s", got["org_id"], orgID)
	}
	ids, ok := got["session_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != sessions[0] {
		t.Errorf("session_ids = %v, want %v", got["session_ids"], sessions)
	}
	// No catalog CONTENT: a payload that sat in the outbox through a retry
	// backoff must not be able to deliver a stale name or price.
	for _, forbidden := range []string{"name", "title", "price", "start_at", "status"} {
		if _, present := got[forbidden]; present {
			t.Errorf("payload carries %q — mirrors must re-read instead", forbidden)
		}
	}
}

// An absent org or session list is omitted rather than emitted as an empty
// string / empty array: the dispatcher reads a missing session_ids as "every
// session of this event", which is the right answer for a newly published one.
func TestBuildCatalogEventPayload_OmitsWhatTheProducerDoesNotKnow(t *testing.T) {
	got := BuildCatalogEventPayload("evt", "", nil)

	if len(got) != 1 || got["event_id"] != "evt" {
		t.Fatalf("payload = %v, want only event_id", got)
	}
}

// The three types this file declares must all be types the bil24_wp dispatcher
// actually maps; an unmapped one would be delivered nowhere, silently.
func TestCatalogEventTypes_AreDeliverable(t *testing.T) {
	for _, eventType := range []string{
		EventPublishedEventType,
		EventUpdatedEventType,
		SessionUpdatedEventType,
		SessionCancelledEventType,
	} {
		if bil24wire.SiteEventTypeFor(eventType) == "" {
			t.Errorf("%q maps to no site event type", eventType)
		}
	}
}

// A publisher with no event id has nothing to say; it must not append a row
// the dispatcher would then have to defend against.
func TestPublishCatalogEvent_IgnoresIncompleteInput(t *testing.T) {
	h := &Handler{}
	// A zero Handler has no outbox writer at all, so reaching PublishScannerEvent
	// would be observable as a panic or a nil-deref; returning early is the
	// behaviour under test.
	h.PublishCatalogEvent(t.Context(), "", "evt", "org", nil)
	h.PublishCatalogEvent(t.Context(), EventUpdatedEventType, "", "org", nil)
}

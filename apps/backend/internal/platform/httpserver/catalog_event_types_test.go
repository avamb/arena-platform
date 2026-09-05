// catalog_event_types_test.go — the seam guard for W1-B7c (#506).
//
// hcatalog fires the catalog outbox events; hscanner writes them. Neither
// imports the other (the publisher is injected as a callback), so the event
// type strings are declared twice. That duplication is only safe if something
// checks it — and this package is the natural place, because it is the one
// that legitimately imports both halves in order to wire them together.
//
// If this test fails, a mirror stopped being told about a catalog change: the
// producer would emit a type no dispatcher maps, and the failure would be
// silent in production.
package httpserver

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hscanner"
)

func TestCatalogEventTypes_ProducerAndOutboxAgree(t *testing.T) {
	pairs := []struct {
		name     string
		producer string
		outbox   string
	}{
		{"event published", hcatalog.EventPublishedEventType, hscanner.EventPublishedEventType},
		{"event updated", hcatalog.EventUpdatedEventType, hscanner.EventUpdatedEventType},
		{"session updated", hcatalog.SessionUpdatedEventType, hscanner.SessionUpdatedEventType},
	}
	for _, p := range pairs {
		if p.producer != p.outbox {
			t.Errorf("%s: hcatalog emits %q but hscanner writes %q", p.name, p.producer, p.outbox)
		}
	}
}

func TestCatalogEventTypes_DispatcherMapsEveryProducedType(t *testing.T) {
	// The other end of the same seam: a type the producer emits that the
	// bil24_wp dispatcher does not recognise is delivered nowhere.
	produced := []string{
		hcatalog.EventPublishedEventType,
		hcatalog.EventUpdatedEventType,
		hcatalog.SessionUpdatedEventType,
		// Cancellation is produced by the older S-1 publisher, but the
		// dispatcher must map it too — it is the site's "delete".
		hscanner.SessionCancelledEventType,
	}
	for _, eventType := range produced {
		if got := bil24wire.SiteEventTypeFor(eventType); got == "" {
			t.Errorf("dispatcher maps %q to nothing — the site would never hear about it", eventType)
		}
	}
}

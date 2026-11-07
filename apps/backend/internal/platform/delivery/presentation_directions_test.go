package delivery

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Coordinates are resolved at render time and only as a pair; a hint wins.
func TestApplyPresentation_VenueCoordinates(t *testing.T) {
	lat, lng := 32.0853, 34.7818

	var p Payload
	applyPresentation(&p, gen.TicketPresentationRow{VenueGeoLat: &lat, VenueGeoLng: &lng})
	if p.VenueLat == nil || p.VenueLng == nil || *p.VenueLat != lat || *p.VenueLng != lng {
		t.Fatalf("pair not applied: %+v", p)
	}

	var half Payload
	applyPresentation(&half, gen.TicketPresentationRow{VenueGeoLat: &lat})
	if half.VenueLat != nil || half.VenueLng != nil {
		t.Fatalf("a lone latitude must not be applied: %+v", half)
	}

	hl, hn := 1.0, 2.0
	hint := Payload{VenueLat: &hl, VenueLng: &hn}
	applyPresentation(&hint, gen.TicketPresentationRow{VenueGeoLat: &lat, VenueGeoLng: &lng})
	if *hint.VenueLat != 1.0 || *hint.VenueLng != 2.0 {
		t.Fatalf("hint must win: %+v", hint)
	}
}

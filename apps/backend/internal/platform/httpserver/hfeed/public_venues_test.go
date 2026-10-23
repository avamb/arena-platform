package hfeed

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestPublicVenueFromRow_OnlyFilledParts(t *testing.T) {
	v := publicVenueFromRow(gen.EventVenueDetailRow{
		Name:         "Hall",
		AddressLine1: sp("  Main 1 "),
		AddressLine2: sp("Floor 2"),
		PostalCode:   sp(" "),
		Country:      sp("CZ"),
		Starts:       []time.Time{time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC)},
	})
	if v.Address == nil || *v.Address != "Main 1, Floor 2" {
		t.Fatalf("address = %v", v.Address)
	}
	if v.PostalCode != nil || v.City != nil || v.Timezone != nil {
		t.Fatalf("blank/absent parts must stay null: %+v", v)
	}
	if len(v.SessionStarts) != 1 || v.SessionStarts[0] != "2026-12-01T18:00:00Z" {
		t.Fatalf("starts = %v", v.SessionStarts)
	}
}

func TestPublicVenueFromRow_LegacyAddressFallbackAndNothingInvented(t *testing.T) {
	v := publicVenueFromRow(gen.EventVenueDetailRow{Name: "Old", Address: sp("101 Rothschild Blvd")})
	if v.Address == nil || *v.Address != "101 Rothschild Blvd" {
		t.Fatalf("legacy address = %v", v.Address)
	}
	empty := publicVenueFromRow(gen.EventVenueDetailRow{Name: "Bare"})
	if empty.Address != nil || empty.Country != nil || len(empty.SessionStarts) != 0 || empty.SessionStarts == nil {
		t.Fatalf("a bare venue must carry no address and an empty (non-null) starts list: %+v", empty)
	}
}

func TestPublicVenueFromRow_CapsSessionStarts(t *testing.T) {
	starts := make([]time.Time, 100)
	for i := range starts {
		starts[i] = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	}
	v := publicVenueFromRow(gen.EventVenueDetailRow{Name: "Tour", Starts: starts})
	if len(v.SessionStarts) != maxVenueSessionStarts {
		t.Fatalf("len = %d", len(v.SessionStarts))
	}
}

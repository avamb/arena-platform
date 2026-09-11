// import_wire_524_test.go — identifier-regime coverage for the event bundle
// (feature #524, W1-E1b; event-bundle spec §3.1).
package bil24compat

import (
	"errors"
	"strings"
	"testing"
)

func arenaRequest() ImportSessionRequest {
	return ImportSessionRequest{
		Source:      SourceArena,
		ExternalRef: "wp:lampyris-staging:product:4711",
		Action:      ImportSessionAction{ActionName: "Lampyris Live"},
		ActionEvent: ImportSessionActionEvent{Day: "26.10.2026", Time: "19:00", Currency: "CZK"},
		Venue:       ImportSessionVenue{VenueName: "Palác Akropolis", Timezone: "Europe/Prague"},
		CategoryList: []ImportSessionCategory{
			{CategoryPriceName: "Standard", Price: 450, Availability: 300},
		},
	}
}

// TestValidateArenaIDs_AllOptional proves the core §3.1 difference from
// ValidateExternalIDs: a bundle carrying no ids at all is valid, because a
// missing id means "mint one".
func TestValidateArenaIDs_AllOptional(t *testing.T) {
	if err := arenaRequest().ValidateArenaIDs(); err != nil {
		t.Fatalf("ValidateArenaIDs on an id-less bundle: %v", err)
	}
}

// TestValidateArenaIDs_AcceptsMintedIDs covers the edit path: the caller
// returns the ids arena issued, all at or above the ceiling.
func TestValidateArenaIDs_AcceptsMintedIDs(t *testing.T) {
	r := arenaRequest()
	r.Action.ActionID = ExternalIDCeiling
	r.ActionEvent.ActionEventID = ExternalIDCeiling + 1
	r.Venue.VenueID = ExternalIDCeiling + 2
	r.CategoryList[0].CategoryPriceID = ExternalIDCeiling + 3
	if err := r.ValidateArenaIDs(); err != nil {
		t.Fatalf("ValidateArenaIDs on minted ids: %v", err)
	}
}

func TestValidateArenaIDs_RejectsBil24RangeIDs(t *testing.T) {
	cases := []struct {
		name  string
		mutar func(*ImportSessionRequest)
		field string
	}{
		{"actionId", func(r *ImportSessionRequest) { r.Action.ActionID = 267271 }, "action.actionId"},
		{"actionEventId", func(r *ImportSessionRequest) { r.ActionEvent.ActionEventID = 703872 }, "actionEvent.actionEventId"},
		{"venueId", func(r *ImportSessionRequest) { r.Venue.VenueID = 9619 }, "venue.venueId"},
		{"categoryPriceId", func(r *ImportSessionRequest) { r.CategoryList[0].CategoryPriceID = 12345 }, "categoryList[0].categoryPriceId"},
		{"negative id", func(r *ImportSessionRequest) { r.Venue.VenueID = -5 }, "venue.venueId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := arenaRequest()
			tc.mutar(&r)
			err := r.ValidateArenaIDs()
			if err == nil {
				t.Fatalf("ValidateArenaIDs: want an error")
			}
			if !errors.Is(err, ErrArenaIDOutOfRange) {
				t.Fatalf("error %v does not wrap ErrArenaIDOutOfRange", err)
			}
			if got := err.Error(); !strings.Contains(got, tc.field) {
				t.Fatalf("error %q does not name the offending field %q", got, tc.field)
			}
		})
	}
}

// TestValidateArenaIDs_IgnoresSeatIDs — seating is out of scope for
// source=arena in this wave, so a stray seatList must not fail validation.
func TestValidateArenaIDs_IgnoresSeatIDs(t *testing.T) {
	r := arenaRequest()
	r.SeatList = []ImportSessionSeat{{SeatID: 2873098559, CategoryPriceID: 12345}}
	if err := r.ValidateArenaIDs(); err != nil {
		t.Fatalf("ValidateArenaIDs with a seatList: %v", err)
	}
}

// TestValidateExternalIDs_Unchanged pins the bil24 regime: the #517 rules must
// not have drifted while the arena ones were added.
func TestValidateExternalIDs_Unchanged(t *testing.T) {
	r := arenaRequest()
	r.Source = SourceBil24
	// No ids at all is INVALID for bil24 — they are mandatory there.
	if err := r.ValidateExternalIDs(); !errors.Is(err, ErrExternalIDOutOfRange) {
		t.Fatalf("ValidateExternalIDs on an id-less bil24 payload = %v, want ErrExternalIDOutOfRange", err)
	}
	r.Action.ActionID = 267271
	r.ActionEvent.ActionEventID = 703872
	r.Venue.VenueID = 9619
	r.CategoryList[0].CategoryPriceID = 12345
	if err := r.ValidateExternalIDs(); err != nil {
		t.Fatalf("ValidateExternalIDs on a well-formed bil24 payload: %v", err)
	}
	r.Action.ActionID = ExternalIDCeiling
	if err := r.ValidateExternalIDs(); !errors.Is(err, ErrExternalIDOutOfRange) {
		t.Fatalf("ValidateExternalIDs at the ceiling = %v, want ErrExternalIDOutOfRange", err)
	}
}

func TestKnownImportSourceAndExternalRefNormalisation(t *testing.T) {
	if !KnownImportSource(SourceBil24) || !KnownImportSource(SourceArena) {
		t.Fatalf("KnownImportSource rejects a known source")
	}
	for _, bad := range []string{"", "Arena", "bil24 ", "woocommerce"} {
		if KnownImportSource(bad) {
			t.Errorf("KnownImportSource(%q) = true, want false", bad)
		}
	}
	r := ImportSessionRequest{ExternalRef: "  wp:site:product:1  "}
	if got := r.NormalizedExternalRef(); got != "wp:site:product:1" {
		t.Fatalf("NormalizedExternalRef = %q", got)
	}
	if got := (ImportSessionRequest{ExternalRef: "   "}).NormalizedExternalRef(); got != "" {
		t.Fatalf("NormalizedExternalRef on whitespace = %q, want empty", got)
	}
}

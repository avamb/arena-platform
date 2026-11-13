package notify

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worldgeo"
)

func TestReasonWordsCoverEveryReason(t *testing.T) {
	for _, r := range venueplan.EscalationReasons {
		if w := reasonWords[string(r)]; strings.TrimSpace(w) == "" {
			t.Errorf("reason %q has no words for the administrator", r)
		}
	}
}

func TestPayloadSubject(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		name string
		p    Payload
		ok   bool
	}{
		{"intake kind with intake id", Payload{Kind: KindIntakeCreated, IntakeID: id, Nonce: "n"}, true},
		{"venue kind with venue id", Payload{Kind: KindVenueCreated, VenueID: id, Nonce: "n"}, true},
		{"venue kind with only an intake id", Payload{Kind: KindVenueCreated, IntakeID: id, Nonce: "n"}, false},
		{"intake kind with only a venue id", Payload{Kind: KindPlanApproved, VenueID: id, Nonce: "n"}, false},
		{"no nonce", Payload{Kind: KindPlanApproved, IntakeID: id}, false},
		{"unknown kind", Payload{Kind: "other", IntakeID: id, Nonce: "n"}, false},
		{"bad id", Payload{Kind: KindIntakeEscalated, IntakeID: "x", Nonce: "n"}, false},
		{"country kind with country id", Payload{Kind: KindCountryCreated, CountryID: id, CountryIso2: "MN", VenueID: id, Nonce: "n"}, true},
		{"country kind with only a venue id", Payload{Kind: KindCountryCreated, VenueID: id, Nonce: "n"}, false},
	}
	for _, c := range cases {
		if _, err := c.p.subject(); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
}

func TestHandlerRefusesABrokenPayload(t *testing.T) {
	h := NewHandler(Options{})
	for _, raw := range []string{`not json`, `{"kind":"venue_created","nonce":"n"}`, `{"kind":"x","nonce":"n"}`} {
		if err := h(context.Background(), []byte(raw)); err == nil {
			t.Errorf("payload %s was accepted", raw)
		}
	}
}

func TestRegionWordsCoverEveryRegion(t *testing.T) {
	for _, r := range worldgeo.Regions() {
		if strings.TrimSpace(regionWords[r]) == "" {
			t.Errorf("region %q has no words for the administrator", r)
		}
	}
}

func TestClipKeepsShortTextAndCutsLongText(t *testing.T) {
	if got := clip("short"); got != "short" {
		t.Errorf("clip(short) = %q", got)
	}
	long := strings.Repeat("я", maxTextRunes+10)
	if got := []rune(clip(long)); len(got) != maxTextRunes+1 {
		t.Errorf("clipped length = %d runes", len(got))
	}
}

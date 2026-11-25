package hsite

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
)

const trackEventUUID = "11111111-2222-3333-4444-555555555555"

func trackChannel() Channel {
	return Channel{Origins: []string{"https://lampyrisevents.com"}}
}

func TestParseTrackBody(t *testing.T) {
	if _, err := ParseTrackBody([]byte(`{"event":"page_view","event_id":"a1"}`)); err != nil {
		t.Fatalf("valid body: %v", err)
	}
	for _, bad := range []string{``, `{`, `[]`, `{"event":"page_view"} {}`, `{"nope":1}`, `{"event":5}`} {
		if _, err := ParseTrackBody([]byte(bad)); err == nil {
			t.Errorf("body %q must be unreadable", bad)
		}
	}
}

func TestCleanTrack_DropsWhatIsNotCounted(t *testing.T) {
	ch := trackChannel()
	ok := TrackRequest{Event: TrackPageView, EventID: "ev-1_x", PageURL: "https://lampyrisevents.com/tours/a"}
	if _, good := CleanTrack(ok, ch); !good {
		t.Fatal("a plain page view must pass")
	}
	cases := map[string]func(r *TrackRequest){
		"unknown event":  func(r *TrackRequest) { r.Event = "purchase" },
		"empty event id": func(r *TrackRequest) { r.EventID = "" },
		"bad event id":   func(r *TrackRequest) { r.EventID = "a b" },
		"long event id":  func(r *TrackRequest) { r.EventID = strings.Repeat("a", 65) },
		"foreign page":   func(r *TrackRequest) { r.PageURL = "https://evil.test/" },
		"http page":      func(r *TrackRequest) { r.PageURL = "http://lampyrisevents.com/" },
		"no page":        func(r *TrackRequest) { r.PageURL = "" },
		"click without platform event": func(r *TrackRequest) {
			r.Event = TrackOutboundClick
		},
		"click with bad platform event": func(r *TrackRequest) {
			r.Event = TrackOutboundClick
			r.Data.EventID = "not-a-uuid"
		},
	}
	for name, mut := range cases {
		r := ok
		mut(&r)
		if _, good := CleanTrack(r, ch); good {
			t.Errorf("%s must not be counted", name)
		}
	}
}

func TestCleanTrack_OutboundClickFields(t *testing.T) {
	r := TrackRequest{
		Event: TrackOutboundClick, EventID: "e1", PageURL: "https://lampyrisevents.com/t",
		UTMSource: "meta", FBP: "fb.1.2.3", GAClientID: "123.456\x00", VID: strings.Repeat("v", 65),
		TrackingDeclined: true,
	}
	r.Data.EventID = trackEventUUID
	r.Data.SessionID = "garbage"
	r.Data.Seller = "Karabas"
	r.Data.Value = 25.5
	r.Data.Currency = "eur"
	r.Data.CheckoutType = "own" // ignored: the kind decides
	r.Data.Email = "a@b.co"     // lead only
	e, ok := CleanTrack(r, trackChannel())
	if !ok {
		t.Fatal("click must pass")
	}
	if e.CheckoutType != "external" || e.Seller != "Karabas" || e.Currency != "EUR" || e.Value != 25.5 {
		t.Errorf("unexpected fields: %+v", e)
	}
	if e.GAClientID != "" || e.VisitorID != "" || e.SessionID != nil || e.Email != "" || !e.Declined {
		t.Errorf("dirty values must be dropped: %+v", e)
	}
	se, ok := e.ToSiteEvent("203.0.113.9", "Mozilla/5.0")
	if !ok || se.Kind != conversions.KindInitiateCheckout || se.ContentID != trackEventUUID || se.Context.ClientIP != "203.0.113.9" {
		t.Errorf("site event: %+v ok=%v", se, ok)
	}
}

func TestCleanTrack_LeadKeepsContactOnly(t *testing.T) {
	r := TrackRequest{Event: TrackLead, EventID: "l1", PageURL: "https://lampyrisevents.com/partners"}
	r.Data.Email = " Anna@Example.com "
	r.Data.Phone = "+972 50 123 4567"
	e, ok := CleanTrack(r, trackChannel())
	if !ok || e.Email != "anna@example.com" || e.Phone == "" {
		t.Fatalf("lead: %+v ok=%v", e, ok)
	}
	se, _ := e.ToSiteEvent("", "")
	if se.Kind != conversions.KindLead {
		t.Errorf("kind = %s", se.Kind)
	}
}

func TestCleanTrack_ValueNeedsNumberAndCurrency(t *testing.T) {
	base := TrackRequest{Event: TrackPageView, EventID: "p1", PageURL: "https://lampyrisevents.com/"}
	for name, v := range map[string]any{"string": "10", "negative": -1.0, "nil": nil} {
		r := base
		r.Data.Value, r.Data.Currency = v, "EUR"
		if e, _ := CleanTrack(r, trackChannel()); e.Value != 0 || e.Currency != "" {
			t.Errorf("%s value kept: %+v", name, e)
		}
	}
	r := base
	r.Data.Value, r.Data.Currency = 10.0, "EURO"
	if e, _ := CleanTrack(r, trackChannel()); e.Currency != "" || e.Value != 0 {
		t.Errorf("bad currency kept: %+v", e)
	}
}

func TestSiteKeyValid(t *testing.T) {
	sum := sha256.Sum256([]byte("secret-key"))
	hash := hex.EncodeToString(sum[:])
	if !SiteKeyValid(hash, "secret-key") || !SiteKeyValid(strings.ToUpper(hash), " secret-key ") {
		t.Error("the right key must pass")
	}
	for _, bad := range [][2]string{{hash, "other"}, {hash, ""}, {"", "secret-key"}, {"", ""}, {hash, strings.Repeat("k", 300)}} {
		if SiteKeyValid(bad[0], bad[1]) {
			t.Errorf("key %q against %q must not pass", bad[1], bad[0])
		}
	}
}

func TestClientIPFromHeader(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9": "203.0.113.9", " 2001:db8::1 ": "2001:db8::1", "": "", "1.2.3.4, 5.6.7.8": "", "evil": "",
	} {
		if got := clientIPFromHeader(in); got != want {
			t.Errorf("clientIPFromHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

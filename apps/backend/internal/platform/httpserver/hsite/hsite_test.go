package hsite

import (
	"reflect"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contactform"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
)

func TestValidPhone(t *testing.T) {
	for s, want := range map[string]bool{
		"+972 50 123 4567": true, "972501234567": true, "(02) 555-1234": true,
		"call me": false, "123": false, "": false, "+++++-----": false,
		"1234567890123456789012345678901": false,
	} {
		if got := ValidPhone(s); got != want {
			t.Errorf("ValidPhone(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestChannel_AllowsPageByOrigin(t *testing.T) {
	c := Channel{Origins: []string{OriginOf("https://Vinoandco.events/anything"), "https://new.vinoandco.events"}}
	for u, want := range map[string]bool{
		"https://vinoandco.events/club/":     true,
		"https://new.vinoandco.events":       true,
		"http://vinoandco.events/club/":      false,
		"https://vinoandco.events.evil.test": false,
		"javascript:alert(1)":                false,
		"/club/":                             false,
		"":                                   false,
	} {
		if got := c.AllowsPage(u); got != want {
			t.Errorf("AllowsPage(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestHostnamesFollowTheChannelOrigins(t *testing.T) {
	got := honboarding.HostnamesOfOrigins([]string{"https://vinoandco.events", "https://new.vinoandco.events:8443", "ftp://x.test", "junk"})
	if want := []string{"vinoandco.events", "new.vinoandco.events"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hostnames = %v, want %v", got, want)
	}
}

func TestRecipientsForTopic(t *testing.T) {
	raw := []byte(`{"contact":["A@x.example","a@x.example","bad"],"private_event":"p@x.example"}`)
	if got, want := contactform.RecipientsFor(raw, "contact"), []string{"a@x.example"}; !reflect.DeepEqual(got, want) {
		t.Errorf("contact = %v", got)
	}
	if got := contactform.RecipientsFor(raw, "private_event"); len(got) != 1 || got[0] != "p@x.example" {
		t.Errorf("private_event = %v", got)
	}
	if got := contactform.RecipientsFor(raw, "other"); got != nil {
		t.Errorf("unmapped topic = %v", got)
	}
	if got := contactform.RecipientsFor(nil, "contact"); got != nil {
		t.Errorf("no settings = %v", got)
	}
}

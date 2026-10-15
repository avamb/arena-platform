package config

// EVENT_CENTER_PUBLIC_URL receives organizers' password links (spec
// 08_architecture/37_web_event_center_ru.md §4.2), so production refuses a
// plaintext value, and an empty value is allowed (links stay on
// APP_PUBLIC_URL).

import (
	"strings"
	"testing"
)

func TestEventCenterPublicURL_ProductionRule(t *testing.T) {
	cases := []struct {
		name, value string
		wantErr     bool
	}{
		{"empty is allowed", "", false},
		{"https is allowed", "https://events.example.com", false},
		{"http is refused", "http://events.example.com", true},
		{"https with a port is allowed", "https://events.example.com:8443", false},
		{"upper-case scheme is allowed", "HTTPS://events.example.com", false},
		{"no host is refused", "https://", true},
		{"empty host with a path is refused", "https:///reset-password", true},
		{"missing colon is refused", "https//events.example.com", true},
		{"relative value is refused", "events.example.com", true},
		{"user info is refused", "https://user@events.example.com", true},
		{"a path is refused", "https://events.example.com/app", true},
		{"a query is refused", "https://events.example.com?x=1", true},
		{"a fragment is refused", "https://events.example.com#x", true},
		{"opaque value is refused", "https:events.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProductionBase()
			cfg.EventCenterPublicURL = tc.value
			err := cfg.Validate()
			got := err != nil && strings.Contains(err.Error(), "EVENT_CENTER_PUBLIC_URL")
			if got != tc.wantErr {
				t.Fatalf("value %q: error %v, want an EVENT_CENTER_PUBLIC_URL error = %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

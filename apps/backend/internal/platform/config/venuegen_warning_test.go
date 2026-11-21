package config

import (
	"strings"
	"testing"
)

func venueGenWarnings(ws []string) []string {
	var out []string
	for _, w := range ws {
		if strings.Contains(w, "VENUE_GEN_") && strings.Contains(w, "generator is OFF") {
			out = append(out, w)
		}
	}
	return out
}

func TestWarnings_VenueGenHalfConfigured(t *testing.T) {
	const secret = "s3cr3t-token-value"
	cases := []struct {
		name, url, token, wantMissing string
	}{
		{"both empty", "", "", ""},
		{"both set", "http://venue-gen:8090", secret, ""},
		{"token missing", "http://venue-gen:8090", "", "VENUE_GEN_TOKEN is not set"},
		{"url missing", "", secret, "VENUE_GEN_URL is not set"},
	}
	for _, tc := range cases {
		cfg := validBase()
		cfg.Role = RoleWorker
		cfg.VenueGenURL, cfg.VenueGenToken = tc.url, tc.token
		got := venueGenWarnings(cfg.Warnings())
		if tc.wantMissing == "" {
			if len(got) != 0 || cfg.VenueGenHalfConfigured() != "" {
				t.Errorf("%s: unexpected warning %v", tc.name, got)
			}
			continue
		}
		if len(got) != 1 || !strings.HasPrefix(got[0], tc.wantMissing) || got[0] != cfg.VenueGenHalfConfigured() {
			t.Errorf("%s: warnings %v", tc.name, got)
		}
		if strings.Contains(got[0], secret) {
			t.Errorf("%s: the warning carries the token", tc.name)
		}
		if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "VENUE_GEN") {
			t.Errorf("%s: a half-configured pair must not fail Validate: %v", tc.name, err)
		}
	}
}

func TestWarnings_VenueGenHalfConfiguredOnlyForTheWorker(t *testing.T) {
	cfg := validBase()
	cfg.Role = RoleAPI
	cfg.VenueGenURL = "http://venue-gen:8090"
	if got := venueGenWarnings(cfg.Warnings()); len(got) != 0 {
		t.Errorf("api role warned: %v", got)
	}
}

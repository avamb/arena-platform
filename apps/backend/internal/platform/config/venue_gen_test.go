package config

import (
	"strings"
	"testing"
)

// The venue SVG generator belongs to arena-worker only (spec 64 wave 2): the
// worker reads VENUE_GEN_URL and VENUE_GEN_TOKEN, the api holding the token
// is a foreign secret.
func TestLoadFor_VenueGenBelongsToTheWorker(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleWorker])
	t.Setenv("VENUE_GEN_URL", "http://venue-gen:8090/")
	t.Setenv("VENUE_GEN_TOKEN", "  gen-token-should-not-be-printed  ")
	cfg, err := LoadFor(RoleWorker)
	if err != nil {
		t.Fatalf("worker with a generator: %v", err)
	}
	if cfg.VenueGenURL != "http://venue-gen:8090" {
		t.Errorf("VenueGenURL = %q, want the origin without the trailing slash", cfg.VenueGenURL)
	}
	if cfg.VenueGenToken != "gen-token-should-not-be-printed" {
		t.Errorf("VenueGenToken = %q, want the trimmed token", cfg.VenueGenToken)
	}
	if strings.Contains(strings.Join(cfg.Warnings(), "\n"), "VENUE_GEN_TOKEN") {
		t.Errorf("the worker owns VENUE_GEN_TOKEN, got: %v", cfg.Warnings())
	}

	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	t.Setenv("VENUE_GEN_TOKEN", "gen-token-should-not-be-printed")
	cfg, err = LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("a generator token must not stop the api: %v", err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	if !strings.Contains(joined, "VENUE_GEN_TOKEN") {
		t.Errorf("the api must be warned that VENUE_GEN_TOKEN is not its own, got: %s", joined)
	}
	if strings.Contains(joined, "gen-token-should-not-be-printed") {
		t.Errorf("warnings must not print a secret value, got: %s", joined)
	}
}

func TestLoadFor_VenueGenEmptyIsOff(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleWorker])
	cfg, err := LoadFor(RoleWorker)
	if err != nil {
		t.Fatalf("worker without a generator: %v", err)
	}
	if cfg.VenueGenURL != "" || cfg.VenueGenToken != "" {
		t.Errorf("the generator must default to off, got %q / %q", cfg.VenueGenURL, cfg.VenueGenToken)
	}
}

func TestLoadFor_VenueGenURLMustBeHTTP(t *testing.T) {
	for _, bad := range []string{"venue-gen:8090", "ftp://venue-gen", "http://", "://x"} {
		clearConfigEnv(t)
		setEnv(t, productionCommon)
		setEnv(t, roleMinimum[RoleWorker])
		t.Setenv("VENUE_GEN_URL", bad)
		if _, err := LoadFor(RoleWorker); err == nil || !strings.Contains(err.Error(), "VENUE_GEN_URL") {
			t.Errorf("VENUE_GEN_URL=%q: want a VENUE_GEN_URL error, got %v", bad, err)
		}
	}
	// The api never reads it: a bad value there is not its problem.
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	t.Setenv("VENUE_GEN_URL", "venue-gen:8090")
	if _, err := LoadFor(RoleAPI); err != nil {
		t.Errorf("the api must ignore VENUE_GEN_URL, got %v", err)
	}
}

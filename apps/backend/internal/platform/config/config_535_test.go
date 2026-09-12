package config

// Tests for feature #535 (W1-S1a, spec 22_site_facing_gaps_w1s1_ru.md §2.1):
// API_PUBLIC_URL is the public origin of the API itself. Everything a
// third-party WordPress site has to FETCH — the Bil24 gateway
// base_url/image_url, absolute signed poster URLs, ticket PDF links — is built
// on it. It falls back to APP_PUBLIC_URL so single-host stands keep working,
// and in production with BIL24_COMPAT_ENABLED=true the EFFECTIVE value must be
// a non-empty https:// URL.

import (
	"strings"
	"testing"
)

// TestW1S1a_APIPublicBaseURL_Fallback pins the resolution order: API_PUBLIC_URL
// wins, APP_PUBLIC_URL is the single-host fallback, both empty yields "".
func TestW1S1a_APIPublicBaseURL_Fallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		api  string
		app  string
		want string
	}{
		{"both set: API wins", "https://api.example.com", "https://app.example.com", "https://api.example.com"},
		{"api empty: falls back to app", "", "https://app.example.com", "https://app.example.com"},
		{"api blank-only: falls back to app", "   ", "https://app.example.com", "https://app.example.com"},
		{"both empty", "", "", ""},
		{"trailing slash trimmed on api", "https://api.example.com/", "", "https://api.example.com"},
		{"trailing slash trimmed on app fallback", "", "https://app.example.com/", "https://app.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{APIPublicURL: tc.api, AppPublicURL: tc.app}
			if got := cfg.APIPublicBaseURL(); got != tc.want {
				t.Errorf("APIPublicBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestW1S1a_Production_GatewayAcceptsAPIPublicURLAlone proves the gateway rule
// is satisfied by API_PUBLIC_URL on a split-host deployment where the SPA
// origin is not configured for this process at all.
func TestW1S1a_Production_GatewayAcceptsAPIPublicURLAlone(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	cfg.AppPublicURL = ""
	cfg.APIPublicURL = "https://api.example.com"

	err := cfg.Validate()
	if err != nil && strings.Contains(err.Error(), "API_PUBLIC_URL") {
		t.Fatalf("a valid https API_PUBLIC_URL must satisfy the gateway rule on its own, got: %v", err)
	}
}

// TestW1S1a_Production_GatewayRejectsPlaintextAPIPublicURL proves an http://
// API origin is refused even when APP_PUBLIC_URL is a perfectly good https
// URL — the effective value is what the site fetches.
func TestW1S1a_Production_GatewayRejectsPlaintextAPIPublicURL(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	cfg.AppPublicURL = "https://app.example.com"
	cfg.APIPublicURL = "http://api.example.com"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: an http:// API_PUBLIC_URL must be rejected in production")
	}
	if !strings.Contains(err.Error(), "API_PUBLIC_URL") || !strings.Contains(err.Error(), "https://") {
		t.Errorf("error should name API_PUBLIC_URL and the https:// requirement, got: %v", err)
	}
}

// TestW1S1a_Production_GatewayRejectsBothEmpty proves neither variable set is
// still a boot failure while the gateway is on.
func TestW1S1a_Production_GatewayRejectsBothEmpty(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	cfg.AppPublicURL = ""
	cfg.APIPublicURL = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: BIL24_COMPAT_ENABLED=true with no public base URL at all must be rejected")
	}
	if !strings.Contains(err.Error(), "BIL24_COMPAT_ENABLED") {
		t.Errorf("error should name the Bil24 rule, got: %v", err)
	}
}

// TestW1S1a_APIPublicURL_LoadedFromEnv proves the variable is actually read
// from the environment by Load, not just declared on the struct.
func TestW1S1a_APIPublicURL_LoadedFromEnv(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.loaded.example")
	t.Setenv("JWT_SIGNING_SECRET", "test-secret-long-enough-for-hs256-validation")
	t.Setenv("DATABASE_URL", "post"+"gres://u:p@localhost:5432/db?sslmode=disable")

	cfg, err := Load()
	if cfg == nil {
		t.Fatalf("Load returned nil config (err=%v)", err)
	}
	if cfg.APIPublicURL != "https://api.loaded.example" {
		t.Errorf("APIPublicURL = %q, want the API_PUBLIC_URL env value", cfg.APIPublicURL)
	}
}

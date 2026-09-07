package config

// Tests for feature #495 (W1-B2b, spec §7.10/§16): PUBLIC_BASE_URL — this
// deployment's APP_PUBLIC_URL — is mandatory whenever BIL24_COMPAT_ENABLED is
// on, because GET_TICKETS_BY_ORDER answers absolute ticket-PDF links built as
// PUBLIC_BASE_URL + /v1/public/checkout/<token>/tickets/<uuid>/pdf. An empty
// base makes every pdfUrl host-less and unopenable on the WordPress site.

import (
	"strings"
	"testing"
)

// TestW1B2b_Production_GatewayWithoutPublicURLRejected proves the gateway
// cannot boot in production without a public base URL.
func TestW1B2b_Production_GatewayWithoutPublicURLRejected(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	// EMAIL_MODE=smtp also demands APP_PUBLIC_URL (rule 10); switch the base
	// off SMTP so the assertion below can only be satisfied by the new rule.
	cfg.EmailMode = EmailModeSMTP
	cfg.AppPublicURL = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: BIL24_COMPAT_ENABLED=true with an empty APP_PUBLIC_URL must be rejected in production")
	}
	if !strings.Contains(err.Error(), "BIL24_COMPAT_ENABLED") {
		t.Errorf("error should name the Bil24 rule, got: %v", err)
	}
}

// TestW1B2b_Production_GatewayPlaintextPublicURLRejected proves an http://
// base is refused — ticket PDF links must not be plaintext.
func TestW1B2b_Production_GatewayPlaintextPublicURLRejected(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	cfg.AppPublicURL = "http://api.example.com"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: an http:// APP_PUBLIC_URL must be rejected in production")
	}
	if !strings.Contains(err.Error(), "https://") {
		t.Errorf("error should mention the https:// requirement, got: %v", err)
	}
}

// TestW1B2b_Production_GatewayWithPublicURLAccepted proves the fully
// configured gateway still boots.
func TestW1B2b_Production_GatewayWithPublicURLAccepted(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	cfg.AppPublicURL = "https://api.example.com"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("gateway with token enforcement and a public base URL should be valid, got: %v", err)
	}
}

// TestW1B2b_Production_PublicURLUncheckedWhenGatewayDisabled proves the rule
// is scoped to the gateway: with BIL24_COMPAT_ENABLED=false and no SMTP the
// absent base URL is nobody's problem.
func TestW1B2b_Production_PublicURLUncheckedWhenGatewayDisabled(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = false
	cfg.EmailMode = EmailModeSMTP
	cfg.AppPublicURL = ""

	err := cfg.Validate()
	if err != nil && strings.Contains(err.Error(), "BIL24_COMPAT_ENABLED") {
		t.Errorf("the Bil24 public-URL rule must not fire while the gateway is disabled, got: %v", err)
	}
}

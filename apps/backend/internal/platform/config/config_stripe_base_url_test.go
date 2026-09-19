package config

// STRIPE_API_BASE_URL redirects every hosted Checkout Session call away from
// api.stripe.com. It exists only so an end-to-end test can run a real
// arena-api binary against a stub, because the Stripe adapter is built per
// request from each organizer's own secret key and there is no object to
// inject.
//
// Left reachable in production it would be the worst kind of misconfiguration:
// the deployment would keep working, while every checkout posted the
// organizer's live secret key to somebody else's endpoint and every buyer
// followed whatever payment URL came back. So the point of these tests is not
// that the field parses — it is that production refuses to boot with it set.

import (
	"strings"
	"testing"
)

func TestStripeAPIBaseURL_DefaultsToEmpty(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	es.unset("STRIPE_API_BASE_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.StripeAPIBaseURL != "" {
		t.Errorf("StripeAPIBaseURL = %q with the env var unset; want empty so the adapter "+
			"talks to the real api.stripe.com", cfg.StripeAPIBaseURL)
	}
}

func TestStripeAPIBaseURL_IsParsedAndTrimmed(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	// A trailing slash here would produce "…/v1//checkout/sessions", because
	// the adapter concatenates rather than joins.
	es.set("STRIPE_API_BASE_URL", "  http://localhost:12111/v1/  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.StripeAPIBaseURL != "http://localhost:12111/v1" {
		t.Errorf("StripeAPIBaseURL = %q; want the trimmed value without a trailing slash",
			cfg.StripeAPIBaseURL)
	}
}

// TestStripeAPIBaseURL_ProductionRejectsAnyValue is the one that matters.
func TestStripeAPIBaseURL_ProductionRejectsAnyValue(t *testing.T) {
	for _, value := range []string{
		"http://localhost:12111/v1",
		"https://api.stripe.example.com/v1",
		// Even the real endpoint is refused: there is no legitimate reason to
		// state it, and allowing "the safe one" would mean parsing attacker-
		// shaped URLs to decide which is which.
		"https://api.stripe.com/v1",
	} {
		cfg := validProductionBase()
		cfg.StripeAPIBaseURL = value

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("STRIPE_API_BASE_URL=%q was accepted in production; it must be a hard "+
				"boot failure", value)
		}
		if !strings.Contains(err.Error(), "STRIPE_API_BASE_URL") {
			t.Errorf("the error for %q must name STRIPE_API_BASE_URL so an operator can find "+
				"it, got: %v", value, err)
		}
	}
}

func TestStripeAPIBaseURL_ProductionAcceptsEmpty(t *testing.T) {
	cfg := validProductionBase()
	cfg.StripeAPIBaseURL = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a production config with STRIPE_API_BASE_URL empty must validate, got: %v", err)
	}

	// Whitespace only is the same as unset — an operator who "cleared" the
	// variable by blanking it must not trip the guard.
	cfg.StripeAPIBaseURL = "   "
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a whitespace-only STRIPE_API_BASE_URL must count as unset, got: %v", err)
	}
}

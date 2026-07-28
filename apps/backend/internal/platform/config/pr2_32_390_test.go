package config

// Tests for feature #390 (PR2-32): close the Bil24 gateway configuration gaps.
//
// The 2026-07-19 adversarial audit found two config-level holes:
//
//  1. BIL24_REQUIRE_TOKEN was documented (struct tag default:"true") but never
//     parsed by config.Load — the field silently stayed false, so any
//     deployment that enabled the gateway ran it without authentication.
//  2. BIL24_COMPAT_ENABLED=true + BIL24_REQUIRE_TOKEN=false was env-reachable
//     in production; only a doc comment discouraged it.

import (
	"strings"
	"testing"
)

// TestPR232_Load_RequireTokenDefaultsTrue proves BIL24_REQUIRE_TOKEN is now
// actually parsed and defaults to TRUE when unset — before #390 the field was
// never assigned and silently stayed false.
func TestPR232_Load_RequireTokenDefaultsTrue(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	es.unset("BIL24_REQUIRE_TOKEN")
	es.unset("BIL24_COMPAT_ENABLED")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if !cfg.Bil24RequireToken {
		t.Error("Bil24RequireToken must default to TRUE when BIL24_REQUIRE_TOKEN is unset — " +
			"an unparsed default leaves the gateway unauthenticated")
	}
}

// TestPR232_Load_RequireTokenFalseIsParsed proves the explicit opt-out is
// honoured in non-production environments.
func TestPR232_Load_RequireTokenFalseIsParsed(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	es.set("BIL24_REQUIRE_TOKEN", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Bil24RequireToken {
		t.Error("BIL24_REQUIRE_TOKEN=false must be parsed into Bil24RequireToken=false")
	}
}

// TestPR232_Production_GatewayWithoutTokenRejected proves the unsafe
// combination is a hard boot failure in production.
func TestPR232_Production_GatewayWithoutTokenRejected(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = false
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: BIL24_COMPAT_ENABLED=true + BIL24_REQUIRE_TOKEN=false " +
			"must be rejected in production")
	}
	if !strings.Contains(err.Error(), "BIL24_REQUIRE_TOKEN") {
		t.Errorf("error should mention BIL24_REQUIRE_TOKEN, got: %v", err)
	}
}

// TestPR232_Production_GatewayWithTokenAccepted proves the safe combination
// still boots.
func TestPR232_Production_GatewayWithTokenAccepted(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = true
	cfg.Bil24RequireToken = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("gateway with token enforcement should be valid in production, got: %v", err)
	}
}

// TestPR232_Production_GatewayDisabledIgnoresToken proves the token flag is
// irrelevant while the gateway is off (routes are not mounted at all).
func TestPR232_Production_GatewayDisabledIgnoresToken(t *testing.T) {
	cfg := validProductionBase()
	cfg.Bil24CompatEnabled = false
	cfg.Bil24RequireToken = false
	if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "BIL24_REQUIRE_TOKEN") {
		t.Errorf("BIL24_REQUIRE_TOKEN must not be checked when the gateway is disabled, got: %v", err)
	}
}

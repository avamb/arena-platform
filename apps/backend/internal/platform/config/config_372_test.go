package config

// Tests for PR2-16: Validate APP_PUBLIC_URL and refuse debug flags in production.
//
// Step 1: Require a non-empty absolute https APP_PUBLIC_URL in production when
//         EMAIL_MODE=smtp.
// Step 2: Route DEBUG_ROUTES_ENABLED and FAULT_INJECT_* through config and
//         hard-refuse them when IsProduction().
// Step 3: Warn on insecure non-localhost OTLP (Warnings() method).
// Step 4: Test production rejection of each unsafe flag and empty public URL.

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Step 1: APP_PUBLIC_URL validation in production with EMAIL_MODE=smtp
// ---------------------------------------------------------------------------

// TestPR216_Step1_ProductionSMTP_RequiresPublicURL rejects an empty APP_PUBLIC_URL
// when EMAIL_MODE=smtp in production.
func TestPR216_Step1_ProductionSMTP_RequiresPublicURL(t *testing.T) {
	cfg := validProductionBase()
	cfg.EmailMode = EmailModeSMTP
	cfg.AppPublicURL = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when APP_PUBLIC_URL is empty in production with EMAIL_MODE=smtp")
	}
	if !strings.Contains(err.Error(), "APP_PUBLIC_URL") {
		t.Errorf("error should mention APP_PUBLIC_URL, got: %v", err)
	}
}

// TestPR216_Step1_ProductionSMTP_RequiresHTTPS rejects a non-https APP_PUBLIC_URL.
func TestPR216_Step1_ProductionSMTP_RequiresHTTPS(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"http scheme", "http://app.example.com"},
		{"relative path", "/app"},
		{"bare hostname", "app.example.com"},
		{"ftp scheme", "ftp://app.example.com"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProductionBase()
			cfg.EmailMode = EmailModeSMTP
			cfg.AppPublicURL = tc.url
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for non-https APP_PUBLIC_URL %q, got nil", tc.url)
			}
			if !strings.Contains(err.Error(), "APP_PUBLIC_URL") {
				t.Errorf("error should mention APP_PUBLIC_URL, got: %v", err)
			}
		})
	}
}

// TestPR216_Step1_ProductionSMTP_ValidHTTPS passes when APP_PUBLIC_URL is a valid https URL.
func TestPR216_Step1_ProductionSMTP_ValidHTTPS(t *testing.T) {
	cfg := validProductionBase()
	cfg.EmailMode = EmailModeSMTP
	cfg.AppPublicURL = "https://app.example.com"
	cfg.SMTPHost = "smtp.example.com"
	cfg.SMTPFrom = "no-reply@example.com"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config with https APP_PUBLIC_URL, got: %v", err)
	}
}

// TestPR216_Step1_ProductionNonSMTP_URLNotRequired skips the APP_PUBLIC_URL check
// when EMAIL_MODE is not smtp (e.g. webhook-only deployment).
func TestPR216_Step1_ProductionNonSMTP_URLNotRequired(t *testing.T) {
	cfg := validProductionBase()
	cfg.EmailMode = EmailModeLog // will be rejected for other reasons in production
	cfg.AppPublicURL = ""
	// The EMAIL_MODE=log error will be present; we only verify that the
	// APP_PUBLIC_URL error is NOT present when email mode is not smtp.
	err := cfg.Validate()
	// There will be an error (log mode forbidden), but it should not mention APP_PUBLIC_URL.
	if err != nil && strings.Contains(err.Error(), "APP_PUBLIC_URL") {
		t.Errorf("APP_PUBLIC_URL should not be checked when EMAIL_MODE!=smtp, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Step 2: DEBUG_ROUTES_ENABLED and FAULT_INJECT_* hard-refused in production
// ---------------------------------------------------------------------------

// TestPR216_Step2_DebugRoutesRefusedInProduction rejects DEBUG_ROUTES_ENABLED=true
// in production.
func TestPR216_Step2_DebugRoutesRefusedInProduction(t *testing.T) {
	cfg := validProductionBase()
	cfg.DebugRoutesEnabled = true
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when DEBUG_ROUTES_ENABLED=true in production")
	}
	if !strings.Contains(err.Error(), "DEBUG_ROUTES_ENABLED") {
		t.Errorf("error should mention DEBUG_ROUTES_ENABLED, got: %v", err)
	}
}

// TestPR216_Step2_FaultInjectRefusedInProduction rejects FAULT_INJECT_OUTBOX_AFTER_AUDIT=true
// in production.
func TestPR216_Step2_FaultInjectRefusedInProduction(t *testing.T) {
	cfg := validProductionBase()
	cfg.FaultInjectOutboxAfterAudit = true
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when FAULT_INJECT_OUTBOX_AFTER_AUDIT=true in production")
	}
	if !strings.Contains(err.Error(), "FAULT_INJECT_OUTBOX_AFTER_AUDIT") {
		t.Errorf("error should mention FAULT_INJECT_OUTBOX_AFTER_AUDIT, got: %v", err)
	}
}

// TestPR216_Step2_DebugRoutesFalse_ProductionOK verifies that DEBUG_ROUTES_ENABLED=false
// (the default) does not trigger a production error.
func TestPR216_Step2_DebugRoutesFalse_ProductionOK(t *testing.T) {
	cfg := validProductionBase()
	cfg.DebugRoutesEnabled = false
	cfg.FaultInjectOutboxAfterAudit = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid production config with debug flags disabled, got: %v", err)
	}
}

// TestPR216_Step2_DebugRoutes_AllowedInDevelopment verifies that DEBUG_ROUTES_ENABLED=true
// is allowed in development.
func TestPR216_Step2_DebugRoutes_AllowedInDevelopment(t *testing.T) {
	cfg := validBase()
	cfg.DebugRoutesEnabled = true
	cfg.FaultInjectOutboxAfterAudit = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("debug/fault-inject flags should be allowed in development, got: %v", err)
	}
}

// TestPR216_Step2_DebugRoutes_AllFieldsInConfig verifies that the new fields are
// routed through Config (not raw os.Getenv) — i.e. they exist on the struct.
func TestPR216_Step2_DebugRoutes_AllFieldsInConfig(t *testing.T) {
	// If these fields don't exist this test won't compile.
	cfg := &Config{
		DebugRoutesEnabled:          true,
		FaultInjectOutboxAfterAudit: true,
	}
	if !cfg.DebugRoutesEnabled {
		t.Error("DebugRoutesEnabled should be true")
	}
	if !cfg.FaultInjectOutboxAfterAudit {
		t.Error("FaultInjectOutboxAfterAudit should be true")
	}
}

// TestPR216_Step2_FieldTags_DebugRoutesEnabled verifies the struct tags on the
// new Config fields so documentation tooling and .env.example stay in sync.
func TestPR216_Step2_FieldTags_DebugRoutesEnabled(t *testing.T) {
	envTag, ok := fieldTag("DebugRoutesEnabled", "env")
	if !ok {
		t.Fatal("Config.DebugRoutesEnabled is missing the env struct tag")
	}
	if envTag != "DEBUG_ROUTES_ENABLED" {
		t.Errorf("env tag: want DEBUG_ROUTES_ENABLED, got %q", envTag)
	}

	defTag, ok := fieldTag("DebugRoutesEnabled", "default")
	if !ok {
		t.Fatal("Config.DebugRoutesEnabled is missing the default struct tag")
	}
	if defTag != "false" {
		t.Errorf("default tag: want \"false\", got %q", defTag)
	}
}

// TestPR216_Step2_FieldTags_FaultInjectOutboxAfterAudit verifies struct tags on
// the fault-inject field.
func TestPR216_Step2_FieldTags_FaultInjectOutboxAfterAudit(t *testing.T) {
	envTag, ok := fieldTag("FaultInjectOutboxAfterAudit", "env")
	if !ok {
		t.Fatal("Config.FaultInjectOutboxAfterAudit is missing the env struct tag")
	}
	if envTag != "FAULT_INJECT_OUTBOX_AFTER_AUDIT" {
		t.Errorf("env tag: want FAULT_INJECT_OUTBOX_AFTER_AUDIT, got %q", envTag)
	}

	defTag, ok := fieldTag("FaultInjectOutboxAfterAudit", "default")
	if !ok {
		t.Fatal("Config.FaultInjectOutboxAfterAudit is missing the default struct tag")
	}
	if defTag != "false" {
		t.Errorf("default tag: want \"false\", got %q", defTag)
	}
}

// ---------------------------------------------------------------------------
// Step 3: Warnings() — insecure OTLP endpoint warning
// ---------------------------------------------------------------------------

// TestPR216_Step3_Warnings_InsecureRemoteOTLP emits a warning when
// OTEL_EXPORTER_OTLP_INSECURE=true and the endpoint is remote (non-localhost).
func TestPR216_Step3_Warnings_InsecureRemoteOTLP(t *testing.T) {
	cfg := validBase()
	cfg.OTLPEndpoint = "otel-collector.example.com:4317"
	cfg.OTELInsecure = true

	warnings := cfg.Warnings()
	if len(warnings) == 0 {
		t.Fatal("expected at least one warning for insecure non-localhost OTLP endpoint")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "OTEL_EXPORTER_OTLP_INSECURE") && strings.Contains(w, "plaintext") {
			found = true
		}
	}
	if !found {
		t.Errorf("warning should mention OTEL_EXPORTER_OTLP_INSECURE and plaintext, got: %v", warnings)
	}
}

// TestPR216_Step3_Warnings_LocalhostOTLP_NoWarn suppresses the warning when the
// OTLP endpoint is localhost (sidecar/dev pattern).
func TestPR216_Step3_Warnings_LocalhostOTLP_NoWarn(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		{"localhost with port", "localhost:4317"},
		{"127.0.0.1 with port", "127.0.0.1:4317"},
		{"::1 with port", "[::1]:4317"},
		{"localhost no port", "localhost"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBase()
			cfg.OTLPEndpoint = tc.endpoint
			cfg.OTELInsecure = true

			warnings := cfg.Warnings()
			for _, w := range warnings {
				if strings.Contains(w, "OTEL_EXPORTER_OTLP_INSECURE") {
					t.Errorf("unexpected OTLP insecure warning for local endpoint %q: %s", tc.endpoint, w)
				}
			}
		})
	}
}

// TestPR216_Step3_Warnings_SecureRemoteOTLP_NoWarn suppresses the warning when
// OTELInsecure=false even with a remote endpoint.
func TestPR216_Step3_Warnings_SecureRemoteOTLP_NoWarn(t *testing.T) {
	cfg := validBase()
	cfg.OTLPEndpoint = "otel-collector.example.com:4317"
	cfg.OTELInsecure = false

	warnings := cfg.Warnings()
	for _, w := range warnings {
		if strings.Contains(w, "OTEL_EXPORTER_OTLP_INSECURE") {
			t.Errorf("unexpected OTLP insecure warning when OTELInsecure=false: %s", w)
		}
	}
}

// TestPR216_Step3_Warnings_EmptyOTLPEndpoint_NoWarn suppresses the warning when
// no OTLP endpoint is configured at all.
func TestPR216_Step3_Warnings_EmptyOTLPEndpoint_NoWarn(t *testing.T) {
	cfg := validBase()
	cfg.OTLPEndpoint = ""
	cfg.OTELInsecure = true // doesn't matter without an endpoint

	if warnings := cfg.Warnings(); len(warnings) != 0 {
		t.Errorf("expected no warnings with empty OTLP endpoint, got: %v", warnings)
	}
}

// TestPR216_Step3_WarningsMethod_ReturnsSlice verifies the Warnings() method exists
// and returns a []string (compile-time check via interface assertion not possible
// for concrete struct methods, but we verify the return type by assigning it).
func TestPR216_Step3_WarningsMethod_ReturnsSlice(_ *testing.T) {
	cfg := validBase()
	w := cfg.Warnings()
	_ = append(w, "") // compile-time proof Warnings() returns a []string
}

// ---------------------------------------------------------------------------
// Step 4: Table-driven production rejection tests for all new flags
// ---------------------------------------------------------------------------

// TestPR216_Step4_ProductionRejectsAllUnsafeFlags runs all new production
// rejection cases in a single table-driven test matching the pattern established
// by TestValidate_Production_TableDriven.
func TestPR216_Step4_ProductionRejectsAllUnsafeFlags(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*Config)
		wantInError string
	}{
		{
			name: "APP_PUBLIC_URL empty with EMAIL_MODE=smtp",
			mutate: func(c *Config) {
				c.AppPublicURL = ""
			},
			wantInError: "APP_PUBLIC_URL",
		},
		{
			name: "APP_PUBLIC_URL with http scheme",
			mutate: func(c *Config) {
				c.AppPublicURL = "http://app.example.com"
			},
			wantInError: "APP_PUBLIC_URL",
		},
		{
			name: "APP_PUBLIC_URL relative path",
			mutate: func(c *Config) {
				c.AppPublicURL = "/relative/path"
			},
			wantInError: "APP_PUBLIC_URL",
		},
		{
			name: "DEBUG_ROUTES_ENABLED=true",
			mutate: func(c *Config) {
				c.DebugRoutesEnabled = true
			},
			wantInError: "DEBUG_ROUTES_ENABLED",
		},
		{
			name: "FAULT_INJECT_OUTBOX_AFTER_AUDIT=true",
			mutate: func(c *Config) {
				c.FaultInjectOutboxAfterAudit = true
			},
			wantInError: "FAULT_INJECT_OUTBOX_AFTER_AUDIT",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProductionBase()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %q, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("expected error containing %q, got: %v", tc.wantInError, err)
			}
		})
	}
}

// TestPR216_Step4_MultipleUnsafeFlagsAggregated verifies that all three unsafe
// production flags are reported in a single joined error when set simultaneously.
func TestPR216_Step4_MultipleUnsafeFlagsAggregated(t *testing.T) {
	cfg := validProductionBase()
	cfg.AppPublicURL = "" // empty → APP_PUBLIC_URL error
	cfg.DebugRoutesEnabled = true
	cfg.FaultInjectOutboxAfterAudit = true

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected aggregated error for all unsafe flags, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"APP_PUBLIC_URL", "DEBUG_ROUTES_ENABLED", "FAULT_INJECT_OUTBOX_AFTER_AUDIT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error missing %q\nfull error:\n%s", want, msg)
		}
	}
}

// TestPR216_Step4_ProductionBaseIsStillValid re-runs the existing production
// base against the updated validator to confirm the new fields do not break
// existing valid production configs.
func TestPR216_Step4_ProductionBaseIsStillValid(t *testing.T) {
	cfg := validProductionBase()
	// New fields default to false (zero value) — valid.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validProductionBase() should still pass after PR2-16 changes, got: %v", err)
	}
}

// TestPR216_Step4_Load_DebugFlagsWiredFromEnv verifies that DEBUG_ROUTES_ENABLED
// and FAULT_INJECT_OUTBOX_AFTER_AUDIT are read from the environment via Load().
func TestPR216_Step4_Load_DebugFlagsWiredFromEnv(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	es.set("DEBUG_ROUTES_ENABLED", "true")
	es.set("FAULT_INJECT_OUTBOX_AFTER_AUDIT", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed in development with debug flags: %v", err)
	}
	if !cfg.DebugRoutesEnabled {
		t.Error("DebugRoutesEnabled should be true when DEBUG_ROUTES_ENABLED=true")
	}
	if !cfg.FaultInjectOutboxAfterAudit {
		t.Error("FaultInjectOutboxAfterAudit should be true when FAULT_INJECT_OUTBOX_AFTER_AUDIT=true")
	}
}

// TestPR216_Step4_Load_DebugFlagsDefaultToFalse verifies that the new flags
// default to false when the environment variables are absent.
func TestPR216_Step4_Load_DebugFlagsDefaultToFalse(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	es.unset("DEBUG_ROUTES_ENABLED")
	es.unset("FAULT_INJECT_OUTBOX_AFTER_AUDIT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed: %v", err)
	}
	if cfg.DebugRoutesEnabled {
		t.Error("DebugRoutesEnabled should default to false")
	}
	if cfg.FaultInjectOutboxAfterAudit {
		t.Error("FaultInjectOutboxAfterAudit should default to false")
	}
}

// ---------------------------------------------------------------------------
// Feature #362: Payment webhook signing secrets required in production
// ---------------------------------------------------------------------------

// TestPR206_WebhookSecretsMissingInProduction verifies that production startup
// is refused when both STRIPE_WEBHOOK_SECRET and ALLPAY_WEBHOOK_SECRET are empty.
func TestPR206_WebhookSecretsMissingInProduction(t *testing.T) {
	cfg := validProductionBase()
	cfg.StripeWebhookSecret = ""
	cfg.AllPayWebhookSecret = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when both webhook secrets are absent in production")
	}
	if !strings.Contains(err.Error(), "STRIPE_WEBHOOK_SECRET") && !strings.Contains(err.Error(), "ALLPAY_WEBHOOK_SECRET") {
		t.Errorf("error should mention STRIPE_WEBHOOK_SECRET or ALLPAY_WEBHOOK_SECRET; got: %v", err)
	}
}

// TestPR206_StripeSecretAloneIsEnoughInProduction verifies that providing only
// STRIPE_WEBHOOK_SECRET satisfies the production requirement.
func TestPR206_StripeSecretAloneIsEnoughInProduction(t *testing.T) {
	cfg := validProductionBase()
	cfg.StripeWebhookSecret = "whsec_valid_stripe_secret_only"
	cfg.AllPayWebhookSecret = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("STRIPE_WEBHOOK_SECRET alone should be enough in production; got: %v", err)
	}
}

// TestPR206_AllPaySecretAloneIsEnoughInProduction verifies that providing only
// ALLPAY_WEBHOOK_SECRET satisfies the production requirement.
func TestPR206_AllPaySecretAloneIsEnoughInProduction(t *testing.T) {
	cfg := validProductionBase()
	cfg.StripeWebhookSecret = ""
	cfg.AllPayWebhookSecret = "allpay-valid-secret-only"
	if err := cfg.Validate(); err != nil {
		t.Errorf("ALLPAY_WEBHOOK_SECRET alone should be enough in production; got: %v", err)
	}
}

// TestPR206_WebhookSecretsNotRequiredInDevelopment verifies that development
// configs without webhook secrets are accepted (dev/mock mode).
func TestPR206_WebhookSecretsNotRequiredInDevelopment(t *testing.T) {
	cfg := validBase()
	cfg.StripeWebhookSecret = ""
	cfg.AllPayWebhookSecret = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("dev config without webhook secrets should be valid; got: %v", err)
	}
}

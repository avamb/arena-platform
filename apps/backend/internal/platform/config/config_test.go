package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// validBase returns a Config that passes Validate() in development mode.
// Individual tests start from this baseline and mutate one or more fields to
// exercise the failure branches.
func validBase() *Config {
	return &Config{
		AppEnv:             EnvDevelopment,
		AppName:            "arena-api",
		AppVersion:         "0.0.0-test",
		AppCommit:          "test",
		HTTPListenAddr:     ":8080",
		BodyLimitBytes:     1 << 20,
		RequestTimeout:     30 * time.Second,
		CORSAllowedOrigins: []string{"*"}, // wildcard OK in development
		ShutdownTimeout:    20 * time.Second,
		DatabaseURL:        "postgres://arena:arena@localhost:5432/arena?sslmode=disable",
		DBPoolMinConns:     2,
		DBPoolMaxConns:     20,
		DBPoolMaxConnLife:  time.Hour,
		DBPoolMaxConnIdle:  30 * time.Minute,
		DBLogQueries:       false,
		RedisURL:           "redis://localhost:6379/0",
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en", "ru"},
		LogLevel:           "info",
		LogFormat:          "json",
		OTLPEndpoint:       "",
		JWTSecretStub:   "dev-secret",
		EnableStubAuth:     true,
		// New fields — zero values are valid in development
		OutboxMode: OutboxModeNoop, // dev default: noop
		EmailMode:  EmailModeLog,   // dev default: log
	}
}

// validProductionBase returns a Config that passes Validate() in production.
// Tests for production rejections start from this and corrupt a single field.
func validProductionBase() *Config {
	return &Config{
		AppEnv:             EnvProduction,
		AppName:            "arena-api",
		AppVersion:         "1.0.0",
		AppCommit:          "abc1234",
		HTTPListenAddr:     ":8080",
		BodyLimitBytes:     1 << 20,
		RequestTimeout:     30 * time.Second,
		CORSAllowedOrigins: []string{"https://app.example.com"},
		ShutdownTimeout:    20 * time.Second,
		DatabaseURL:        "postgres://arena:strongpw@db.example.com:5432/arena?sslmode=require",
		DBPoolMinConns:     2,
		DBPoolMaxConns:     20,
		DBPoolMaxConnLife:  time.Hour,
		DBPoolMaxConnIdle:  30 * time.Minute,
		DBLogQueries:       false,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en", "ru"},
		LogLevel:           "info",
		LogFormat:          "json",
		// Strong production JWT secret (>= 32 bytes)
		JWTSecretStub: "a-very-strong-secret-for-production-use-32b",
		EnableStubAuth:   false,
		// Explicit production modes
		OutboxMode:      OutboxModeDisabled,
		EmailMode:       EmailModeSMTP,
		SMTPHost:        "smtp.example.com",
		SMTPPort:        "587",
		SMTPFrom:        "no-reply@example.com",
		AppPublicURL:    "https://app.example.com",
		OTELTracesSampler: 0.1,
	}
}

// ---------------------------------------------------------------------------
// General validation tests (development profile)
// ---------------------------------------------------------------------------

func TestValidate_OK(t *testing.T) {
	cfg := validBase()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected nil error for valid config, got: %v", err)
	}
}

func TestValidate_MissingRequiredFieldsAggregated(t *testing.T) {
	// Empty struct triggers every required-field check at once. Validate must
	// return a single joined error that mentions all of them.
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty config, got nil")
	}

	msg := err.Error()
	wantSubstrings := []string{
		"APP_ENV",
		"HTTP_LISTEN_ADDR",
		"DATABASE_URL",
		"DB_POOL_MAX_CONNS",
		"BODY_LIMIT_BYTES",
		"REQUEST_TIMEOUT_SECONDS",
		"SHUTDOWN_TIMEOUT",
		"DEFAULT_LOCALE",
		"ACTIVE_LOCALES",
		"LOG_FORMAT",
		"LOG_LEVEL",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error missing %q\nfull error:\n%s", want, msg)
		}
	}
}

func TestValidate_InvalidAppEnv(t *testing.T) {
	cfg := validBase()
	cfg.AppEnv = AppEnv("circus")
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid APP_ENV")
	}
	if !strings.Contains(err.Error(), "APP_ENV") {
		t.Errorf("error should mention APP_ENV, got: %v", err)
	}
}

func TestValidate_InvalidLogLevel(t *testing.T) {
	cfg := validBase()
	cfg.LogLevel = "shout"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid LOG_LEVEL")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error should mention LOG_LEVEL, got: %v", err)
	}
}

func TestValidate_InvalidLogFormat(t *testing.T) {
	cfg := validBase()
	cfg.LogFormat = "xml"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid LOG_FORMAT")
	}
	if !strings.Contains(err.Error(), "LOG_FORMAT") {
		t.Errorf("error should mention LOG_FORMAT, got: %v", err)
	}
}

func TestValidate_InvalidDatabaseURLScheme(t *testing.T) {
	cfg := validBase()
	cfg.DatabaseURL = "mysql://nope"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for non-postgres DSN")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error should mention DATABASE_URL, got: %v", err)
	}
}

func TestValidate_DatabaseURLCredentialsRedacted(t *testing.T) {
	cfg := validBase()
	cfg.DatabaseURL = "mysql://supersecret_user:supersecret_pw@db.example.com:3306/arena"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for non-postgres DSN")
	}
	if strings.Contains(err.Error(), "supersecret_pw") {
		t.Errorf("DSN password leaked into validation error: %v", err)
	}
	if strings.Contains(err.Error(), "supersecret_user") {
		t.Errorf("DSN username leaked into validation error: %v", err)
	}
}

func TestValidate_DBPoolMinGreaterThanMax(t *testing.T) {
	cfg := validBase()
	cfg.DBPoolMinConns = 50
	cfg.DBPoolMaxConns = 10
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when min > max")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MIN_CONNS") {
		t.Errorf("error should mention DB_POOL_MIN_CONNS, got: %v", err)
	}
}

func TestValidate_DBPoolMaxZero(t *testing.T) {
	cfg := validBase()
	cfg.DBPoolMaxConns = 0
	cfg.DBPoolMinConns = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when DB_POOL_MAX_CONNS is 0")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MAX_CONNS") {
		t.Errorf("error should mention DB_POOL_MAX_CONNS, got: %v", err)
	}
}

func TestValidate_DBPoolMinNegative(t *testing.T) {
	cfg := validBase()
	cfg.DBPoolMinConns = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when DB_POOL_MIN_CONNS is negative")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MIN_CONNS") {
		t.Errorf("error should mention DB_POOL_MIN_CONNS, got: %v", err)
	}
}

func TestValidate_BodyLimitZero(t *testing.T) {
	cfg := validBase()
	cfg.BodyLimitBytes = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when BODY_LIMIT_BYTES is 0")
	}
	if !strings.Contains(err.Error(), "BODY_LIMIT_BYTES") {
		t.Errorf("error should mention BODY_LIMIT_BYTES, got: %v", err)
	}
}

func TestValidate_RequestTimeoutNonPositive(t *testing.T) {
	cfg := validBase()
	cfg.RequestTimeout = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when REQUEST_TIMEOUT_SECONDS is 0")
	}
	if !strings.Contains(err.Error(), "REQUEST_TIMEOUT_SECONDS") {
		t.Errorf("error should mention REQUEST_TIMEOUT_SECONDS, got: %v", err)
	}
}

func TestValidate_DefaultLocaleNotInActive(t *testing.T) {
	cfg := validBase()
	cfg.DefaultLocale = "es"
	cfg.ActiveLocales = []string{"en", "ru"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when DEFAULT_LOCALE is not in ACTIVE_LOCALES")
	}
	if !strings.Contains(err.Error(), "DEFAULT_LOCALE") {
		t.Errorf("error should mention DEFAULT_LOCALE, got: %v", err)
	}
}

func TestValidate_StubAuthRequiresSecret(t *testing.T) {
	cfg := validBase()
	cfg.EnableStubAuth = true
	cfg.JWTSecretStub = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when stub auth is enabled without a secret")
	}
	if !strings.Contains(err.Error(), "JWT_SIGNING_SECRET") {
		t.Errorf("error should mention JWT_SIGNING_SECRET, got: %v", err)
	}
}

func TestValidate_StubAuthForbiddenInProduction(t *testing.T) {
	cfg := validBase()
	cfg.AppEnv = EnvProduction
	cfg.EnableStubAuth = true
	cfg.JWTSecretStub = "real-secret-but-stub-still-not-allowed"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when stub auth is enabled in production")
	}
	if !strings.Contains(err.Error(), "ENABLE_DEV_AUTH") {
		t.Errorf("error should mention ENABLE_DEV_AUTH, got: %v", err)
	}
}

func TestValidate_StubAuthDisabledNeedsNoSecret(t *testing.T) {
	cfg := validBase()
	cfg.EnableStubAuth = false
	cfg.JWTSecretStub = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validation should pass when stub auth is disabled, got: %v", err)
	}
}

func TestValidate_ShutdownTimeoutNonPositive(t *testing.T) {
	cfg := validBase()
	cfg.ShutdownTimeout = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when SHUTDOWN_TIMEOUT is 0")
	}
	if !strings.Contains(err.Error(), "SHUTDOWN_TIMEOUT") {
		t.Errorf("error should mention SHUTDOWN_TIMEOUT, got: %v", err)
	}
}

func TestValidate_InvalidOutboxMode(t *testing.T) {
	cfg := validBase()
	cfg.OutboxMode = OutboxMode("magic")
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid OUTBOX_MODE")
	}
	if !strings.Contains(err.Error(), "OUTBOX_MODE") {
		t.Errorf("error should mention OUTBOX_MODE, got: %v", err)
	}
}

func TestValidate_OutboxWebhookRequiresURL(t *testing.T) {
	cfg := validBase()
	cfg.OutboxMode = OutboxModeWebhook
	cfg.OutboxWebhookURL = "" // missing
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when OUTBOX_MODE=webhook but URL is empty")
	}
	if !strings.Contains(err.Error(), "OUTBOX_WEBHOOK_URL") {
		t.Errorf("error should mention OUTBOX_WEBHOOK_URL, got: %v", err)
	}
}

func TestValidate_OutboxWebhookRequiresSigningSecret(t *testing.T) {
	cfg := validBase()
	cfg.OutboxMode = OutboxModeWebhook
	cfg.OutboxWebhookURL = "https://hooks.example.com/events"
	cfg.OutboxSigningSecret = "" // missing
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when OUTBOX_MODE=webhook but OUTBOX_SIGNING_SECRET is empty")
	}
	if !strings.Contains(err.Error(), "OUTBOX_SIGNING_SECRET") {
		t.Errorf("error should mention OUTBOX_SIGNING_SECRET, got: %v", err)
	}
}

func TestValidate_OutboxWebhookRejectsWeakSigningSecret(t *testing.T) {
	cfg := validBase()
	cfg.OutboxMode = OutboxModeWebhook
	cfg.OutboxWebhookURL = "https://hooks.example.com/events"
	cfg.OutboxSigningSecret = "short" // < 32 bytes
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when OUTBOX_SIGNING_SECRET is too short")
	}
	if !strings.Contains(err.Error(), "OUTBOX_SIGNING_SECRET") {
		t.Errorf("error should mention OUTBOX_SIGNING_SECRET, got: %v", err)
	}
}

func TestValidate_InvalidEmailMode(t *testing.T) {
	cfg := validBase()
	cfg.EmailMode = EmailMode("carrier_pigeon")
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid EMAIL_MODE")
	}
	if !strings.Contains(err.Error(), "EMAIL_MODE") {
		t.Errorf("error should mention EMAIL_MODE, got: %v", err)
	}
}

func TestValidate_SMTPModeRequiresHost(t *testing.T) {
	cfg := validBase()
	cfg.EmailMode = EmailModeSMTP
	cfg.SMTPHost = "" // missing
	cfg.SMTPFrom = "test@example.com"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when EMAIL_MODE=smtp but SMTP_HOST is empty")
	}
	if !strings.Contains(err.Error(), "SMTP_HOST") {
		t.Errorf("error should mention SMTP_HOST, got: %v", err)
	}
}

func TestValidate_SMTPModeRequiresFrom(t *testing.T) {
	cfg := validBase()
	cfg.EmailMode = EmailModeSMTP
	cfg.SMTPHost = "smtp.example.com"
	cfg.SMTPFrom = "" // missing
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when EMAIL_MODE=smtp but SMTP_FROM is empty")
	}
	if !strings.Contains(err.Error(), "SMTP_FROM") {
		t.Errorf("error should mention SMTP_FROM, got: %v", err)
	}
}

func TestDBDSNAlias(t *testing.T) {
	cfg := validBase()
	if cfg.DBDSN() != cfg.DatabaseURL {
		t.Fatalf("DBDSN() should equal DatabaseURL; got %q vs %q", cfg.DBDSN(), cfg.DatabaseURL)
	}
}

func TestIsProductionAndDevelopmentHelpers(t *testing.T) {
	cfg := validBase()
	cfg.AppEnv = EnvProduction
	if !cfg.IsProduction() {
		t.Error("expected IsProduction() to be true")
	}
	if cfg.IsDevelopment() {
		t.Error("expected IsDevelopment() to be false")
	}

	cfg.AppEnv = EnvDevelopment
	if !cfg.IsDevelopment() {
		t.Error("expected IsDevelopment() to be true")
	}
	if cfg.IsProduction() {
		t.Error("expected IsProduction() to be false")
	}
}

func TestEffectiveHealthAddr(t *testing.T) {
	cases := []struct {
		name       string
		healthAddr string
		role       string
		want       string
	}{
		{"explicit overrides api", ":9999", "api", ":9999"},
		{"explicit overrides worker", ":9999", "worker", ":9999"},
		{"api default", "", "api", ":8080"},
		{"worker default", "", "worker", ":9091"},
		{"unknown role falls back to HTTP", "", "other", ":8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				HTTPListenAddr:    ":8080",
				WorkerMetricsAddr: ":9091",
				HealthAddr:        tc.healthAddr,
			}
			got := cfg.EffectiveHealthAddr(tc.role)
			if got != tc.want {
				t.Errorf("EffectiveHealthAddr(%q): want %q, got %q", tc.role, tc.want, got)
			}
		})
	}
}

func TestMediaSigningKey(t *testing.T) {
	t.Run("uses MediaSigningSecret when set", func(t *testing.T) {
		cfg := &Config{
			MediaSigningSecret: "explicit-media-secret",
			JWTSecretStub:   "jwt-secret",
		}
		got := string(cfg.MediaSigningKey())
		if got != "explicit-media-secret" {
			t.Errorf("want explicit-media-secret, got %q", got)
		}
	})

	t.Run("falls back to JWT secret when MediaSigningSecret is empty", func(t *testing.T) {
		cfg := &Config{
			MediaSigningSecret: "",
			JWTSecretStub:   "jwt-secret",
		}
		got := string(cfg.MediaSigningKey())
		if got != "jwt-secret" {
			t.Errorf("want jwt-secret fallback, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// PR-00 production safety contract — table-driven rejection tests
// ---------------------------------------------------------------------------

func TestValidate_ProductionOK(t *testing.T) {
	cfg := validProductionBase()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid production config should pass, got: %v", err)
	}
}

func TestValidate_Production_TableDriven(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*Config)
		wantInError string
	}{
		{
			name: "missing JWT secret",
			mutate: func(c *Config) {
				c.JWTSecretStub = ""
			},
			wantInError: "JWT_SIGNING_SECRET",
		},
		{
			name: "weak JWT secret (too short)",
			mutate: func(c *Config) {
				c.JWTSecretStub = "short"
			},
			wantInError: "JWT_SIGNING_SECRET",
		},
		{
			name: "known dev JWT placeholder",
			mutate: func(c *Config) {
				c.JWTSecretStub = "dev-only-do-not-use-in-prod"
			},
			wantInError: "JWT_SIGNING_SECRET",
		},
		{
			name: "dev auth enabled",
			mutate: func(c *Config) {
				c.EnableStubAuth = true
			},
			wantInError: "ENABLE_DEV_AUTH",
		},
		{
			name: "wildcard CORS",
			mutate: func(c *Config) {
				c.CORSAllowedOrigins = []string{"*"}
			},
			wantInError: "CORS_ALLOWED_ORIGINS",
		},
		{
			name: "query logging enabled",
			mutate: func(c *Config) {
				c.DBLogQueries = true
			},
			wantInError: "DB_LOG_QUERIES",
		},
		{
			name: "unsafe DB TLS (sslmode=disable)",
			mutate: func(c *Config) {
				c.DatabaseURL = "postgres://arena:pw@host:5432/db?sslmode=disable"
			},
			wantInError: "unsafe TLS mode",
		},
		{
			name: "unsafe DB TLS (sslmode=allow)",
			mutate: func(c *Config) {
				c.DatabaseURL = "postgres://arena:pw@host:5432/db?sslmode=allow"
			},
			wantInError: "unsafe TLS mode",
		},
		{
			name: "local media without signing secret",
			mutate: func(c *Config) {
				c.MediaBackend = "local"
				c.MediaLocalRoot = "/tmp/media"
				c.MediaSigningSecret = ""
			},
			wantInError: "MEDIA_SIGNING_SECRET",
		},
		{
			name: "local media with weak signing secret",
			mutate: func(c *Config) {
				c.MediaBackend = "local"
				c.MediaLocalRoot = "/tmp/media"
				c.MediaSigningSecret = "short"
			},
			wantInError: "MEDIA_SIGNING_SECRET",
		},
		{
			name: "log-only email",
			mutate: func(c *Config) {
				c.EmailMode = EmailModeLog
			},
			wantInError: "EMAIL_MODE",
		},
		{
			name: "implicit email mode (empty)",
			mutate: func(c *Config) {
				c.EmailMode = ""
			},
			wantInError: "EMAIL_MODE",
		},
		{
			name: "noop outbox mode",
			mutate: func(c *Config) {
				c.OutboxMode = OutboxModeNoop
			},
			wantInError: "OUTBOX_MODE",
		},
		{
			name: "implicit outbox mode (empty)",
			mutate: func(c *Config) {
				c.OutboxMode = ""
			},
			wantInError: "OUTBOX_MODE",
		},
		{
			name: "text log format in production",
			mutate: func(c *Config) {
				c.LogFormat = "text"
			},
			wantInError: "LOG_FORMAT",
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

// TestValidate_ProductionWebhookOutbox ensures the webhook mode with URL is
// also a valid production profile (not just disabled).
func TestValidate_ProductionWebhookOutbox(t *testing.T) {
	cfg := validProductionBase()
	cfg.OutboxMode = OutboxModeWebhook
	cfg.OutboxWebhookURL = "https://webhook.example.com/arena"
	cfg.OutboxSigningSecret = "a-very-strong-webhook-signing-secret-32b+"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("webhook outbox production config should pass, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Redaction / log safety
// ---------------------------------------------------------------------------

func TestLogAttrs_RedactsSecrets(t *testing.T) {
	cfg := validProductionBase()
	cfg.JWTSecretStub = "super-secret-jwt-key-must-not-appear"
	cfg.SMTPPassword = "secret-smtp-password"
	cfg.OutboxSigningSecret = "secret-outbox-signing"
	cfg.MediaSigningSecret = "secret-media-signing"
	cfg.MediaS3SecretAccessKey = "secret-s3-key"
	cfg.MediaS3AccessKeyID = "AKIAIOSFODNN7EXAMPLE"

	attrs := cfg.LogAttrs()
	combined := make([]string, 0, len(attrs))
	for _, a := range attrs {
		combined = append(combined, a.Key+"="+a.Value.String())
	}
	log := strings.Join(combined, " ")

	secrets := []string{
		"super-secret-jwt-key-must-not-appear",
		"secret-smtp-password",
		"secret-outbox-signing",
		"secret-media-signing",
		"secret-s3-key",
	}
	for _, s := range secrets {
		if strings.Contains(log, s) {
			t.Errorf("secret %q leaked into LogAttrs output: %s", s, log)
		}
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Error("expected [REDACTED] marker in LogAttrs output")
	}
}

// ---------------------------------------------------------------------------
// Load() — environment-driven entry point.
// ---------------------------------------------------------------------------

// envSetter is a tiny helper that records prior env-var values so the test can
// restore them on Cleanup, leaving the test environment untouched for any
// other parallel package.
type envSetter struct {
	t        *testing.T
	previous map[string]*string // nil pointer = unset
}

func newEnvSetter(t *testing.T) *envSetter {
	t.Helper()
	es := &envSetter{t: t, previous: map[string]*string{}}
	t.Cleanup(es.restore)
	return es
}

func (e *envSetter) set(key, value string) {
	e.t.Helper()
	e.remember(key)
	if err := os.Setenv(key, value); err != nil {
		e.t.Fatalf("setenv %s: %v", key, err)
	}
}

func (e *envSetter) unset(key string) {
	e.t.Helper()
	e.remember(key)
	if err := os.Unsetenv(key); err != nil {
		e.t.Fatalf("unsetenv %s: %v", key, err)
	}
}

func (e *envSetter) remember(key string) {
	if _, recorded := e.previous[key]; recorded {
		return
	}
	if v, ok := os.LookupEnv(key); ok {
		e.previous[key] = &v
	} else {
		e.previous[key] = nil
	}
}

func (e *envSetter) restore() {
	for key, prev := range e.previous {
		if prev == nil {
			_ = os.Unsetenv(key)
		} else {
			_ = os.Setenv(key, *prev)
		}
	}
}

func TestLoad_MissingRequiredReportsAggregatedError(t *testing.T) {
	es := newEnvSetter(t)
	// Unset everything the validator cares about, then trigger Load.
	for _, k := range []string{
		"APP_ENV", "APP_NAME", "APP_VERSION", "APP_COMMIT",
		"HTTP_LISTEN_ADDR", "BODY_LIMIT_BYTES", "REQUEST_TIMEOUT_SECONDS",
		"CORS_ALLOWED_ORIGINS", "SHUTDOWN_TIMEOUT",
		"DATABASE_URL", "REDIS_URL",
		"DB_POOL_MIN_CONNS", "DB_POOL_MAX_CONNS",
		"DB_POOL_MAX_CONN_LIFETIME", "DB_POOL_MAX_CONN_IDLE_TIME", "DB_LOG_QUERIES",
		"DEFAULT_LOCALE", "ACTIVE_LOCALES",
		"LOG_LEVEL", "LOG_FORMAT",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"JWT_SIGNING_SECRET", "ENABLE_DEV_AUTH",
	} {
		es.unset(k)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with no env vars should fail (DATABASE_URL is required)")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("expected aggregated error to mention DATABASE_URL, got: %v", err)
	}
}

func TestLoad_InvalidIntegerReturnsAggregatedError(t *testing.T) {
	es := newEnvSetter(t)
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("BODY_LIMIT_BYTES", "not-an-int")
	// Set ENABLE_DEV_AUTH=false so the missing JWT_SIGNING_SECRET doesn't
	// dominate the assertion below.
	es.set("ENABLE_DEV_AUTH", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with malformed BODY_LIMIT_BYTES should fail")
	}
	if !strings.Contains(err.Error(), "BODY_LIMIT_BYTES") {
		t.Errorf("expected aggregated error to mention BODY_LIMIT_BYTES, got: %v", err)
	}
}

func TestLoad_HappyPath(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("JWT_SIGNING_SECRET", "dev-secret")
	es.set("ENABLE_DEV_AUTH", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected Load() to succeed, got: %v", err)
	}
	if cfg.AppEnv != EnvDevelopment {
		t.Errorf("AppEnv: want development, got %q", cfg.AppEnv)
	}
	if cfg.DatabaseURL == "" {
		t.Error("DatabaseURL should be populated from DATABASE_URL")
	}
	if !cfg.EnableStubAuth {
		t.Error("EnableStubAuth should be true")
	}
	if cfg.JWTSecretStub != "dev-secret" {
		t.Errorf("JWTSecretStub: want dev-secret, got %q", cfg.JWTSecretStub)
	}
}

func TestLoad_WorkerAndOutboxDefaults(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	for _, k := range []string{
		"WORKER_CONCURRENCY", "WORKER_POLL_INTERVAL", "WORKER_JOB_TIMEOUT",
		"WORKER_RETRY_BACKOFF_BASE", "WORKER_RETRY_BACKOFF_MAX",
		"OUTBOX_BATCH_SIZE", "OUTBOX_POLL_INTERVAL",
		"IDEMPOTENCY_TTL", "IDEMPOTENCY_KEY_MAX_LENGTH",
	} {
		es.unset(k)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed with defaults: %v", err)
	}

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"WorkerConcurrency", cfg.WorkerConcurrency, 4},
		{"WorkerPollInterval", cfg.WorkerPollInterval, time.Second},
		{"WorkerJobTimeout", cfg.WorkerJobTimeout, 5 * time.Minute},
		{"WorkerRetryBackoffBase", cfg.WorkerRetryBackoffBase, 2 * time.Second},
		{"WorkerRetryBackoffMax", cfg.WorkerRetryBackoffMax, 10 * time.Minute},
		{"OutboxBatchSize", cfg.OutboxBatchSize, 50},
		{"OutboxPollInterval", cfg.OutboxPollInterval, 2 * time.Second},
		{"IdempotencyTTL", cfg.IdempotencyTTL, 24 * time.Hour},
		{"IdempotencyKeyMaxLength", cfg.IdempotencyKeyMaxLength, 255},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default for %s: want %v, got %v", c.name, c.want, c.got)
		}
	}
}

func TestLoad_JWTFieldsWiredFromEnv(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("JWT_SIGNING_SECRET", "dev-secret")
	es.set("ENABLE_DEV_AUTH", "true")
	es.set("JWT_ISSUER", "my-issuer")
	es.set("JWT_AUDIENCE", "my-audience")
	es.set("JWT_DEFAULT_TTL", "2h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed: %v", err)
	}
	if cfg.JWTIssuer != "my-issuer" {
		t.Errorf("JWTIssuer: want my-issuer, got %q", cfg.JWTIssuer)
	}
	if cfg.JWTAudience != "my-audience" {
		t.Errorf("JWTAudience: want my-audience, got %q", cfg.JWTAudience)
	}
	if cfg.JWTDefaultTTL != 2*time.Hour {
		t.Errorf("JWTDefaultTTL: want 2h, got %v", cfg.JWTDefaultTTL)
	}
}

func TestLoad_EmailAndSMTPFieldsWiredFromEnv(t *testing.T) {
	es := newEnvSetter(t)
	es.set("APP_ENV", "development")
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("ENABLE_DEV_AUTH", "false")
	es.set("EMAIL_MODE", "smtp")
	es.set("SMTP_HOST", "smtp.example.com")
	es.set("SMTP_PORT", "465")
	es.set("SMTP_USERNAME", "user@example.com")
	es.set("SMTP_PASSWORD", "hunter2")
	es.set("SMTP_FROM", "noreply@example.com")
	es.set("SMTP_USE_TLS", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed: %v", err)
	}
	if cfg.EmailMode != EmailModeSMTP {
		t.Errorf("EmailMode: want smtp, got %q", cfg.EmailMode)
	}
	if cfg.SMTPHost != "smtp.example.com" {
		t.Errorf("SMTPHost: want smtp.example.com, got %q", cfg.SMTPHost)
	}
	if cfg.SMTPPort != "465" {
		t.Errorf("SMTPPort: want 465, got %q", cfg.SMTPPort)
	}
	if cfg.SMTPPassword != "hunter2" {
		t.Errorf("SMTPPassword should be loaded from env")
	}
	if cfg.SMTPFrom != "noreply@example.com" {
		t.Errorf("SMTPFrom: want noreply@example.com, got %q", cfg.SMTPFrom)
	}
	if !cfg.SMTPUseTLS {
		t.Error("SMTPUseTLS: want true")
	}
}

// ---------------------------------------------------------------------------
// Feature #113 — Config struct field tags (env, required, default)
// ---------------------------------------------------------------------------

// fieldTag is a helper that returns the value of a named tag for a Config field.
// It returns ("", false) when the field or tag is absent.
func fieldTag(fieldName, tagKey string) (string, bool) {
	t, ok := reflect.TypeOf(Config{}).FieldByName(fieldName)
	if !ok {
		return "", false
	}
	val, ok := t.Tag.Lookup(tagKey)
	return val, ok
}

func TestConfigFieldTags_EnvTagPresent(t *testing.T) {
	// Each entry maps a Config field name to the expected env var name.
	cases := []struct {
		field   string
		envName string
	}{
		{"AppEnv", "APP_ENV"},
		{"AppName", "APP_NAME"},
		{"AppVersion", "APP_VERSION"},
		{"AppCommit", "APP_COMMIT"},
		{"AppPublicURL", "APP_PUBLIC_URL"},
		{"HTTPListenAddr", "HTTP_LISTEN_ADDR"},
		{"WorkerMetricsAddr", "WORKER_METRICS_ADDR"},
		{"BodyLimitBytes", "BODY_LIMIT_BYTES"},
		{"RequestTimeout", "REQUEST_TIMEOUT_SECONDS"},
		{"CORSAllowedOrigins", "CORS_ALLOWED_ORIGINS"},
		{"ShutdownTimeout", "SHUTDOWN_TIMEOUT"},
		{"HealthAddr", "HEALTH_ADDR"},
		{"DatabaseURL", "DATABASE_URL"},
		{"DBPoolMinConns", "DB_POOL_MIN_CONNS"},
		{"DBPoolMaxConns", "DB_POOL_MAX_CONNS"},
		{"DBPoolMaxConnLife", "DB_POOL_MAX_CONN_LIFETIME"},
		{"DBPoolMaxConnIdle", "DB_POOL_MAX_CONN_IDLE_TIME"},
		{"DBLogQueries", "DB_LOG_QUERIES"},
		{"RedisURL", "REDIS_URL"},
		{"JWTSecretStub", "JWT_SIGNING_SECRET"},
		{"EnableStubAuth", "ENABLE_DEV_AUTH"},
		{"JWTIssuer", "JWT_ISSUER"},
		{"JWTAudience", "JWT_AUDIENCE"},
		{"JWTDefaultTTL", "JWT_DEFAULT_TTL"},
		{"IdempotencyTTL", "IDEMPOTENCY_TTL"},
		{"IdempotencyKeyMaxLength", "IDEMPOTENCY_KEY_MAX_LENGTH"},
		{"DefaultLocale", "DEFAULT_LOCALE"},
		{"ActiveLocales", "ACTIVE_LOCALES"},
		{"LogLevel", "LOG_LEVEL"},
		{"LogFormat", "LOG_FORMAT"},
		{"OTLPEndpoint", "OTEL_EXPORTER_OTLP_ENDPOINT"},
		{"OTELServiceName", "OTEL_SERVICE_NAME"},
		{"OTELTracesSampler", "OTEL_TRACES_SAMPLER_ARG"},
		{"OTELInsecure", "OTEL_EXPORTER_OTLP_INSECURE"},
		{"WorkerConcurrency", "WORKER_CONCURRENCY"},
		{"WorkerPollInterval", "WORKER_POLL_INTERVAL"},
		{"WorkerJobTimeout", "WORKER_JOB_TIMEOUT"},
		{"WorkerRetryBackoffBase", "WORKER_RETRY_BACKOFF_BASE"},
		{"WorkerRetryBackoffMax", "WORKER_RETRY_BACKOFF_MAX"},
		{"OutboxMode", "OUTBOX_MODE"},
		{"OutboxWebhookURL", "OUTBOX_WEBHOOK_URL"},
		{"OutboxSigningSecret", "OUTBOX_SIGNING_SECRET"},
		{"OutboxPollInterval", "OUTBOX_POLL_INTERVAL"},
		{"OutboxBatchSize", "OUTBOX_BATCH_SIZE"},
		{"EmailMode", "EMAIL_MODE"},
		{"SMTPHost", "SMTP_HOST"},
		{"SMTPPort", "SMTP_PORT"},
		{"SMTPUsername", "SMTP_USERNAME"},
		{"SMTPPassword", "SMTP_PASSWORD"},
		{"SMTPFrom", "SMTP_FROM"},
		{"SMTPUseTLS", "SMTP_USE_TLS"},
		{"MediaBackend", "MEDIA_BACKEND"},
		{"MediaLocalRoot", "MEDIA_LOCAL_ROOT"},
		{"MediaSigningSecret", "MEDIA_SIGNING_SECRET"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			val, ok := fieldTag(tc.field, "env")
			if !ok {
				t.Errorf("Config.%s is missing the env struct tag", tc.field)
				return
			}
			if val != tc.envName {
				t.Errorf("Config.%s env tag: want %q, got %q", tc.field, tc.envName, val)
			}
		})
	}
}

func TestConfigFieldTags_RequiredTagPresent(t *testing.T) {
	// Every field must have a required tag so tooling can enumerate
	// which variables are mandatory without running Load().
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if _, ok := f.Tag.Lookup("required"); !ok {
			t.Errorf("Config.%s is missing the required struct tag", f.Name)
		}
	}
}

func TestConfigFieldTags_RequiredFieldsAreMarkedTrue(t *testing.T) {
	// The fields that Load() / Validate() treat as required must be
	// tagged required:"true" so documentation tooling stays in sync.
	requiredFields := []string{"HTTPListenAddr", "DatabaseURL", "DefaultLocale", "ActiveLocales"}
	for _, name := range requiredFields {
		val, ok := fieldTag(name, "required")
		if !ok {
			t.Errorf("Config.%s is missing the required struct tag", name)
			continue
		}
		if val != "true" {
			t.Errorf("Config.%s: expected required:\"true\", got required:%q", name, val)
		}
	}
}

func TestConfigFieldTags_DefaultTagOnNonRequiredFields(t *testing.T) {
	// Fields that are not required must carry a default tag so operators
	// know what value to expect when the variable is absent.
	optionalWithDefaults := []struct {
		field       string
		wantDefault string
	}{
		{"AppEnv", "development"},
		{"AppName", "arena-api"},
		{"HTTPListenAddr", ":8080"},
		{"BodyLimitBytes", "1048576"},
		{"DBPoolMinConns", "2"},
		{"DBPoolMaxConns", "20"},
		{"LogLevel", "info"},
		{"LogFormat", "json"},
		{"WorkerConcurrency", "4"},
		{"WorkerPollInterval", "1s"},
		{"OutboxBatchSize", "50"},
		{"IdempotencyTTL", "24h"},
		{"IdempotencyKeyMaxLength", "255"},
		{"EmailMode", "log"},
		{"OutboxMode", "noop"},
	}

	for _, tc := range optionalWithDefaults {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			val, ok := fieldTag(tc.field, "default")
			if !ok {
				t.Errorf("Config.%s is missing the default struct tag", tc.field)
				return
			}
			if val != tc.wantDefault {
				t.Errorf("Config.%s default tag: want %q, got %q", tc.field, tc.wantDefault, val)
			}
		})
	}
}

func TestConfigFieldTags_AllEnvTagsNonEmpty(t *testing.T) {
	// A field with env:"" would silently break any documentation or code
	// generation that reads the tags. Every env tag must be non-empty.
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if val, ok := f.Tag.Lookup("env"); ok {
			if strings.TrimSpace(val) == "" {
				t.Errorf("Config.%s has an empty env struct tag", f.Name)
			}
		}
	}
}

func TestConfigFieldTags_RequiredTagValueIsBoolean(t *testing.T) {
	// The required tag must be exactly "true" or "false" — no other values
	// are valid so tooling can parse it safely.
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if val, ok := f.Tag.Lookup("required"); ok {
			if val != "true" && val != "false" {
				t.Errorf("Config.%s has invalid required tag %q (must be \"true\" or \"false\")", f.Name, val)
			}
		}
	}
}

// TestConfig113_BootValidation_MissingRequired verifies the exact scenario
// described in the feature test specification: "missing required var fails boot".
func TestConfig113_BootValidation_MissingRequired(t *testing.T) {
	es := newEnvSetter(t)
	es.unset("DATABASE_URL")
	es.set("ENABLE_DEV_AUTH", "false") // avoid secondary errors

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load() to fail when DATABASE_URL is missing")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error should mention DATABASE_URL: %v", err)
	}
}

// TestConfig113_BootValidation_InvalidType verifies: "invalid type fails".
func TestConfig113_BootValidation_InvalidType(t *testing.T) {
	es := newEnvSetter(t)
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("BODY_LIMIT_BYTES", "not-a-number")
	es.set("ENABLE_DEV_AUTH", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load() to fail for non-integer BODY_LIMIT_BYTES")
	}
	if !strings.Contains(err.Error(), "BODY_LIMIT_BYTES") {
		t.Errorf("error should mention BODY_LIMIT_BYTES: %v", err)
	}
}

// TestConfig113_BootValidation_DefaultsApplyWhenAbsent verifies: "defaults apply when var absent".
func TestConfig113_BootValidation_DefaultsApplyWhenAbsent(t *testing.T) {
	es := newEnvSetter(t)
	// Required vars only — everything else should get defaults.
	es.set("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	es.set("JWT_SIGNING_SECRET", "dev-secret")
	es.set("ENABLE_DEV_AUTH", "true")
	es.set("APP_ENV", "development")

	// Ensure optional vars are absent so defaults are exercised.
	for _, k := range []string{
		"APP_NAME", "APP_VERSION", "APP_COMMIT",
		"HTTP_LISTEN_ADDR", "BODY_LIMIT_BYTES", "REQUEST_TIMEOUT_SECONDS",
		"CORS_ALLOWED_ORIGINS", "SHUTDOWN_TIMEOUT",
		"DB_POOL_MIN_CONNS", "DB_POOL_MAX_CONNS",
		"DB_POOL_MAX_CONN_LIFETIME", "DB_POOL_MAX_CONN_IDLE_TIME",
		"DB_LOG_QUERIES", "REDIS_URL",
		"DEFAULT_LOCALE", "ACTIVE_LOCALES",
		"LOG_LEVEL", "LOG_FORMAT",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME",
		"OTEL_TRACES_SAMPLER_ARG", "OTEL_EXPORTER_OTLP_INSECURE",
	} {
		es.unset(k)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed with defaults: %v", err)
	}

	// Verify defaults for a representative set of fields.
	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"AppName", cfg.AppName, "arena-api"},
		{"AppVersion", cfg.AppVersion, "0.0.0-dev"},
		{"HTTPListenAddr", cfg.HTTPListenAddr, ":8080"},
		{"BodyLimitBytes", cfg.BodyLimitBytes, int64(1 << 20)},
		{"RequestTimeout", cfg.RequestTimeout, 30 * time.Second},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 20 * time.Second},
		{"DBPoolMinConns", cfg.DBPoolMinConns, int32(2)},
		{"DBPoolMaxConns", cfg.DBPoolMaxConns, int32(20)},
		{"DefaultLocale", cfg.DefaultLocale, "en"},
		{"LogLevel", cfg.LogLevel, "info"},
		{"LogFormat", cfg.LogFormat, "json"},
		{"OTELTracesSampler", cfg.OTELTracesSampler, float64(1.0)},
		{"OTELInsecure", cfg.OTELInsecure, true},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default for %s: want %v, got %v", c.name, c.want, c.got)
		}
	}
}

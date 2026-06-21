package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// validBase returns a Config that passes Validate(). Individual tests start
// from this baseline and mutate one or more fields to exercise the failure
// branches.
func validBase() *Config {
	return &Config{
		AppEnv:             EnvDevelopment,
		AppName:            "arena-api",
		AppVersion:         "0.0.0-test",
		AppCommit:          "test",
		HTTPListenAddr:     ":8080",
		BodyLimitBytes:     1 << 20,
		RequestTimeout:     30 * time.Second,
		CORSAllowedOrigins: []string{"*"},
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
		JWTSecretStub:      "dev-secret",
		EnableStubAuth:     true,
	}
}

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
	if !strings.Contains(err.Error(), "ENABLE_STUB_AUTH") {
		t.Errorf("error should mention ENABLE_STUB_AUTH, got: %v", err)
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

// -----------------------------------------------------------------------------
// Load() — environment-driven entry point.
// -----------------------------------------------------------------------------

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
		"JWT_SIGNING_SECRET", "ENABLE_STUB_AUTH",
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
	// Set ENABLE_STUB_AUTH=false so the missing JWT_SIGNING_SECRET doesn't
	// dominate the assertion below.
	es.set("ENABLE_STUB_AUTH", "false")

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
	es.set("ENABLE_STUB_AUTH", "true")

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

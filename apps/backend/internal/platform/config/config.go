// Package config loads, validates, and exposes runtime configuration for all
// arena_new binaries (arena-api, arena-worker, arena-migrate).
//
// Configuration is read from environment variables. Missing or malformed
// required values cause a fail-fast error so the process never starts in an
// inconsistent state.
//
// Validate aggregates every validation issue into a single joined error so the
// operator sees the full picture in one boot attempt instead of fixing one
// variable at a time.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// AppEnv enumerates supported deployment profiles.
type AppEnv string

const (
	EnvDevelopment AppEnv = "development"
	EnvStaging     AppEnv = "staging"
	EnvProduction  AppEnv = "production"
)

// Config is the validated runtime configuration consumed by every binary.
//
// Field tags document the environment variable name, whether the field is
// required, and (where applicable) the default value applied when the
// variable is absent. Tags follow the convention:
//
//	env:"VAR_NAME"         — environment variable that populates this field
//	required:"true"        — Load() returns an error when the variable is absent/empty
//	default:"<value>"      — value used when the environment variable is unset
//
// These tags are also read by the .env.example generator and documentation
// tooling. Required fields with no default must be supplied in every
// deployment environment; see .env.example at the repository root for the
// authoritative list and per-variable comments.
type Config struct {
	// Application
	AppEnv     AppEnv `env:"APP_ENV"     required:"false" default:"development"`
	AppName    string `env:"APP_NAME"    required:"false" default:"arena-api"`
	AppVersion string `env:"APP_VERSION" required:"false" default:"0.0.0-dev"`
	AppCommit  string `env:"APP_COMMIT"  required:"false" default:"local"`

	// HTTP
	HTTPListenAddr     string        `env:"HTTP_LISTEN_ADDR"         required:"true"  default:":8080"`
	WorkerMetricsAddr  string        `env:"WORKER_METRICS_ADDR"      required:"false" default:":9091"`
	BodyLimitBytes     int64         `env:"BODY_LIMIT_BYTES"         required:"false" default:"1048576"`
	RequestTimeout     time.Duration `env:"REQUEST_TIMEOUT_SECONDS"  required:"false" default:"30s"`
	CORSAllowedOrigins []string      `env:"CORS_ALLOWED_ORIGINS"     required:"false" default:"*"`
	ShutdownTimeout    time.Duration `env:"SHUTDOWN_TIMEOUT"         required:"false" default:"20s"`

	// Database
	DatabaseURL       string        `env:"DATABASE_URL"              required:"true"`
	DBPoolMinConns    int32         `env:"DB_POOL_MIN_CONNS"         required:"false" default:"2"`
	DBPoolMaxConns    int32         `env:"DB_POOL_MAX_CONNS"         required:"false" default:"20"`
	DBPoolMaxConnLife time.Duration `env:"DB_POOL_MAX_CONN_LIFETIME" required:"false" default:"1h"`
	DBPoolMaxConnIdle time.Duration `env:"DB_POOL_MAX_CONN_IDLE_TIME" required:"false" default:"30m"`
	DBLogQueries      bool          `env:"DB_LOG_QUERIES"            required:"false" default:"false"`

	// Redis
	RedisURL string `env:"REDIS_URL" required:"false" default:""`

	// Internationalization
	DefaultLocale string   `env:"DEFAULT_LOCALE" required:"true"  default:"en"`
	ActiveLocales []string `env:"ACTIVE_LOCALES" required:"true"  default:"en"`

	// Observability
	LogLevel          string  `env:"LOG_LEVEL"                   required:"false" default:"info"`
	LogFormat         string  `env:"LOG_FORMAT"                  required:"false" default:"json"`
	OTLPEndpoint      string  `env:"OTEL_EXPORTER_OTLP_ENDPOINT" required:"false" default:""`
	OTELServiceName   string  `env:"OTEL_SERVICE_NAME"           required:"false" default:""`
	OTELTracesSampler float64 `env:"OTEL_TRACES_SAMPLER_ARG"     required:"false" default:"1.0"`
	OTELInsecure      bool    `env:"OTEL_EXPORTER_OTLP_INSECURE" required:"false" default:"true"`

	// Auth (dev-only placeholder — replaced by real identity module in a later milestone)
	JWTSecretStub  string `env:"JWT_SIGNING_SECRET" required:"false" default:""`
	EnableStubAuth bool   `env:"ENABLE_DEV_AUTH"    required:"false" default:"false"`

	// Outbox dispatcher (feature #110)
	// OutboxWebhookURL is the HTTP endpoint where outbox_events are POSTed.
	// When empty the dispatcher uses the NoopDispatcher (log-only mode).
	OutboxWebhookURL string `env:"OUTBOX_WEBHOOK_URL" required:"false" default:""`
	// OutboxSigningSecret is the HMAC-SHA256 key used to sign webhook payloads.
	// When empty, no X-Arena-Signature header is added.
	OutboxSigningSecret string `env:"OUTBOX_SIGNING_SECRET" required:"false" default:""`
	// OutboxPollInterval is the wait between empty outbox_events queue polls.
	// Defaults to 1s when 0.
	OutboxPollInterval time.Duration `env:"OUTBOX_POLL_INTERVAL" required:"false" default:"1s"`
}

// DBDSN is an alias for DatabaseURL that matches the terminology used in the
// architecture specification (see app_spec.txt and feature #83).
func (c *Config) DBDSN() string { return c.DatabaseURL }

// Load reads configuration from environment variables, parses typed values,
// runs Validate, and returns either a populated *Config or an aggregated
// validation error.
func Load() (*Config, error) {
	cfg := &Config{
		AppEnv:     AppEnv(getenv("APP_ENV", "development")),
		AppName:    getenv("APP_NAME", "arena-api"),
		AppVersion: getenv("APP_VERSION", "0.0.0-dev"),
		AppCommit:  getenv("APP_COMMIT", "local"),

		HTTPListenAddr:     getenv("HTTP_LISTEN_ADDR", ":8080"),
		WorkerMetricsAddr:  getenv("WORKER_METRICS_ADDR", ":9091"),
		CORSAllowedOrigins: splitCSV(getenv("CORS_ALLOWED_ORIGINS", "*")),

		DatabaseURL: getenv("DATABASE_URL", ""),
		RedisURL:    getenv("REDIS_URL", ""),

		DefaultLocale: getenv("DEFAULT_LOCALE", "en"),
		ActiveLocales: splitCSV(getenv("ACTIVE_LOCALES", "en")),

		LogLevel:        getenv("LOG_LEVEL", "info"),
		LogFormat:       getenv("LOG_FORMAT", "json"),
		OTLPEndpoint:    getenv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTELServiceName: getenv("OTEL_SERVICE_NAME", ""),

		JWTSecretStub: getenv("JWT_SIGNING_SECRET", ""),

		OutboxWebhookURL:    getenv("OUTBOX_WEBHOOK_URL", ""),
		OutboxSigningSecret: getenv("OUTBOX_SIGNING_SECRET", ""),
	}

	// Parse errors are collected together with Validate() errors so a single
	// boot attempt surfaces every problem at once.
	var parseErrs []error

	// OTEL sampling ratio (0.0 - 1.0). Empty falls back to 1.0 (sample
	// everything) so dev environments don't silently drop spans. Out-of-range
	// values are reported via Validate(), not here.
	if sampleStr := getenv("OTEL_TRACES_SAMPLER_ARG", ""); sampleStr != "" {
		ratio, perr := strconv.ParseFloat(strings.TrimSpace(sampleStr), 64)
		if perr != nil {
			parseErrs = append(parseErrs, fmt.Errorf(
				"config: OTEL_TRACES_SAMPLER_ARG must be a float in [0.0, 1.0] (got %q): %w",
				sampleStr, perr,
			))
		} else {
			cfg.OTELTracesSampler = ratio
		}
	} else {
		cfg.OTELTracesSampler = 1.0
	}

	// OTEL gRPC insecure flag — defaults to true for local dev (no TLS).
	insecureBool, err := getenvBool("OTEL_EXPORTER_OTLP_INSECURE", true)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.OTELInsecure = insecureBool

	v, err := getenvInt64("BODY_LIMIT_BYTES", 1048576)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.BodyLimitBytes = v

	d, err := getenvDuration("REQUEST_TIMEOUT_SECONDS", 30*time.Second, true)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.RequestTimeout = d

	d, err = getenvDuration("SHUTDOWN_TIMEOUT", 20*time.Second, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.ShutdownTimeout = d

	i32, err := getenvInt32("DB_POOL_MIN_CONNS", 2)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.DBPoolMinConns = i32

	i32, err = getenvInt32("DB_POOL_MAX_CONNS", 20)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.DBPoolMaxConns = i32

	d, err = getenvDuration("DB_POOL_MAX_CONN_LIFETIME", time.Hour, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.DBPoolMaxConnLife = d

	d, err = getenvDuration("DB_POOL_MAX_CONN_IDLE_TIME", 30*time.Minute, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.DBPoolMaxConnIdle = d

	b, err := getenvBool("DB_LOG_QUERIES", false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.DBLogQueries = b

	// Stub auth defaults to on in development so /v1/echo is testable without
	// wiring a real IdP, and off otherwise so a forgotten variable in
	// staging/production cannot silently accept dev tokens.
	stubDefault := cfg.AppEnv == EnvDevelopment
	b, err = getenvBool("ENABLE_DEV_AUTH", stubDefault)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.EnableStubAuth = b

	// Outbox dispatcher poll interval (feature #110).
	d, err = getenvDuration("OUTBOX_POLL_INTERVAL", time.Second, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.OutboxPollInterval = d

	if validateErr := cfg.Validate(); validateErr != nil {
		parseErrs = append(parseErrs, validateErr)
	}

	if len(parseErrs) > 0 {
		return nil, fmt.Errorf("config: invalid configuration: %w", errors.Join(parseErrs...))
	}
	return cfg, nil
}

// Validate inspects every field on the receiver and returns an aggregated
// error (errors.Join) enumerating every missing or invalid value. Returns nil
// when the configuration is acceptable.
//
// This method is also exercised directly by unit tests; callers in production
// code should normally use Load, which invokes Validate internally.
func (c *Config) Validate() error {
	var errs []error

	switch c.AppEnv {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf(
			"APP_ENV %q is invalid (allowed: development|staging|production)", c.AppEnv,
		))
	}

	if strings.TrimSpace(c.HTTPListenAddr) == "" {
		errs = append(errs, errors.New("HTTP_LISTEN_ADDR is required"))
	}

	if strings.TrimSpace(c.DatabaseURL) == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	} else if !looksLikePostgresDSN(c.DatabaseURL) {
		errs = append(errs, fmt.Errorf(
			"DATABASE_URL must be a postgres:// or postgresql:// DSN (got %q)", redactDSN(c.DatabaseURL),
		))
	}

	if c.DBPoolMinConns < 0 {
		errs = append(errs, fmt.Errorf(
			"DB_POOL_MIN_CONNS must be >= 0 (got %d)", c.DBPoolMinConns,
		))
	}
	if c.DBPoolMaxConns <= 0 {
		errs = append(errs, fmt.Errorf(
			"DB_POOL_MAX_CONNS must be > 0 (got %d)", c.DBPoolMaxConns,
		))
	}
	if c.DBPoolMinConns > c.DBPoolMaxConns {
		errs = append(errs, fmt.Errorf(
			"DB_POOL_MIN_CONNS (%d) must be <= DB_POOL_MAX_CONNS (%d)",
			c.DBPoolMinConns, c.DBPoolMaxConns,
		))
	}
	if c.BodyLimitBytes <= 0 {
		errs = append(errs, fmt.Errorf(
			"BODY_LIMIT_BYTES must be > 0 (got %d)", c.BodyLimitBytes,
		))
	}
	if c.RequestTimeout <= 0 {
		errs = append(errs, errors.New("REQUEST_TIMEOUT_SECONDS must be > 0"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be > 0"))
	}

	if strings.TrimSpace(c.DefaultLocale) == "" {
		errs = append(errs, errors.New("DEFAULT_LOCALE is required"))
	}
	if len(c.ActiveLocales) == 0 {
		errs = append(errs, errors.New("ACTIVE_LOCALES must contain at least one locale"))
	} else if !contains(c.ActiveLocales, c.DefaultLocale) {
		errs = append(errs, fmt.Errorf(
			"DEFAULT_LOCALE %q must appear in ACTIVE_LOCALES %v",
			c.DefaultLocale, c.ActiveLocales,
		))
	}

	switch strings.ToLower(c.LogFormat) {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf(
			"LOG_FORMAT must be 'json' or 'text' (got %q)", c.LogFormat,
		))
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf(
			"LOG_LEVEL must be one of debug|info|warn|error (got %q)", c.LogLevel,
		))
	}

	if c.OTELTracesSampler < 0.0 || c.OTELTracesSampler > 1.0 {
		errs = append(errs, fmt.Errorf(
			"OTEL_TRACES_SAMPLER_ARG must be in [0.0, 1.0] (got %v)", c.OTELTracesSampler,
		))
	}

	// Auth stub: secret required when the stub IdP is enabled. In production
	// the stub MUST be disabled — the boundary is wired to a real IdP later.
	if c.EnableStubAuth {
		if strings.TrimSpace(c.JWTSecretStub) == "" {
			errs = append(errs, errors.New(
				"JWT_SIGNING_SECRET is required when ENABLE_DEV_AUTH=true",
			))
		}
		if c.AppEnv == EnvProduction {
			errs = append(errs, errors.New(
				"ENABLE_DEV_AUTH must be false in production (APP_ENV=production)",
			))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// IsProduction reports whether the runtime is the production profile.
func (c *Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// IsDevelopment reports whether the runtime is the development profile.
func (c *Config) IsDevelopment() bool { return c.AppEnv == EnvDevelopment }

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func getenv(key, def string) string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func getenvInt64(key string, def int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer (got %q): %w", key, v, err)
	}
	return n, nil
}

func getenvInt32(key string, def int32) (int32, error) {
	n, err := getenvInt64(key, int64(def))
	if err != nil {
		return 0, err
	}
	if n > int64(int32(^uint32(0)>>1)) || n < int64(-int32(^uint32(0)>>1)-1) {
		return 0, fmt.Errorf("config: %s out of int32 range", key)
	}
	return int32(n), nil
}

func getenvBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("config: %s must be bool (got %q): %w", key, v, err)
	}
	return b, nil
}

// getenvDuration parses a duration value. If secondsFallback is true, a bare
// numeric string is interpreted as seconds (used by *_SECONDS variables).
func getenvDuration(key string, def time.Duration, secondsFallback bool) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	v = strings.TrimSpace(v)
	if secondsFallback {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second, nil
		}
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration (e.g. 30s, 5m) (got %q): %w", key, v, err)
	}
	return d, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	for _, s := range haystack {
		if strings.ToLower(strings.TrimSpace(s)) == needle {
			return true
		}
	}
	return false
}

func looksLikePostgresDSN(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

// redactDSN strips credentials from a DSN before placing it in error messages.
// Best-effort: if parsing fails we return a generic placeholder rather than
// leaking the raw value.
func redactDSN(dsn string) string {
	idx := strings.Index(dsn, "://")
	if idx < 0 {
		return "<dsn>"
	}
	scheme := dsn[:idx+3]
	rest := dsn[idx+3:]
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		return scheme + "***@" + rest[at+1:]
	}
	return scheme + rest
}

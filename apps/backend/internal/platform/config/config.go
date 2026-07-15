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
//
// Production safety contract (PR-00):
//
//   - APP_ENV=production rejects every unsafe fallback (weak JWT secret, dev
//     auth, wildcard CORS, query logging, unsafe DB TLS, unsigned local media,
//     log-only email, implicit/noop outbox mode, text log format).
//   - Secrets are never written to logs. Use [Config.LogValue] or
//     [Config.LogAttrs] when logging configuration at startup.
//   - Every variable in .env.example has a corresponding typed field here.
package config

import (
	"errors"
	"fmt"
	"log/slog"
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

// OutboxMode enumerates how outbox_events are dispatched.
type OutboxMode string

const (
	// OutboxModeNoop logs events but does not deliver them. Dev/test only;
	// forbidden in production.
	OutboxModeNoop OutboxMode = "noop"
	// OutboxModeWebhook delivers events via HTTP POST to OUTBOX_WEBHOOK_URL.
	// Requires OUTBOX_WEBHOOK_URL and a strong OUTBOX_SIGNING_SECRET.
	OutboxModeWebhook OutboxMode = "webhook"
	// OutboxModeDisabled explicitly disables outbox dispatch. Safe for
	// production when the domain does not produce outbox events.
	OutboxModeDisabled OutboxMode = "disabled"
)

// EmailMode enumerates how transactional emails are delivered.
type EmailMode string

const (
	// EmailModeLog writes email content to the structured logger instead of
	// delivering it. Dev/test only; forbidden in production.
	EmailModeLog EmailMode = "log"
	// EmailModeSMTP delivers emails via SMTP. Required for production.
	EmailModeSMTP EmailMode = "smtp"
)

// minJWTSecretLen is the minimum byte length of JWT_SIGNING_SECRET accepted in
// production. 32 bytes (256 bits) provides adequate HS256 entropy.
const minJWTSecretLen = 32

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
	// -------------------------------------------------------------------------
	// Application
	// -------------------------------------------------------------------------
	AppEnv     AppEnv `env:"APP_ENV"      required:"false" default:"development"`
	AppName    string `env:"APP_NAME"     required:"false" default:"arena-api"`
	AppVersion string `env:"APP_VERSION"  required:"false" default:"0.0.0-dev"`
	AppCommit  string `env:"APP_COMMIT"   required:"false" default:"local"`
	// AppPublicURL is the canonical public base URL of the application
	// (e.g. https://app.example.com). Used to construct absolute links in
	// emails and webhooks so they are never derived from r.Host or forwarded
	// headers. Required for production when EMAIL_MODE=smtp.
	AppPublicURL string `env:"APP_PUBLIC_URL" required:"false" default:""`

	// -------------------------------------------------------------------------
	// HTTP
	// -------------------------------------------------------------------------
	HTTPListenAddr     string        `env:"HTTP_LISTEN_ADDR"         required:"true"  default:":8080"`
	WorkerMetricsAddr  string        `env:"WORKER_METRICS_ADDR"      required:"false" default:":9091"`
	BodyLimitBytes     int64         `env:"BODY_LIMIT_BYTES"         required:"false" default:"1048576"`
	RequestTimeout     time.Duration `env:"REQUEST_TIMEOUT_SECONDS"  required:"false" default:"30s"`
	CORSAllowedOrigins []string      `env:"CORS_ALLOWED_ORIGINS"     required:"false" default:"*"`
	ShutdownTimeout    time.Duration `env:"SHUTDOWN_TIMEOUT"         required:"false" default:"20s"`
	// HealthAddr is the address queried by healthcheck probes and the
	// arena-healthcheck binary. When empty it defaults to the process-specific
	// address (HTTPListenAddr for arena-api, WorkerMetricsAddr for arena-worker).
	HealthAddr string `env:"HEALTH_ADDR" required:"false" default:""`

	// -------------------------------------------------------------------------
	// Database (PostgreSQL 17)
	// -------------------------------------------------------------------------
	DatabaseURL       string        `env:"DATABASE_URL"               required:"true"`
	DBPoolMinConns    int32         `env:"DB_POOL_MIN_CONNS"          required:"false" default:"2"`
	DBPoolMaxConns    int32         `env:"DB_POOL_MAX_CONNS"          required:"false" default:"20"`
	DBPoolMaxConnLife time.Duration `env:"DB_POOL_MAX_CONN_LIFETIME"  required:"false" default:"1h"`
	DBPoolMaxConnIdle time.Duration `env:"DB_POOL_MAX_CONN_IDLE_TIME" required:"false" default:"30m"`
	// DBLogQueries enables per-statement SQL logging. Forbidden in production
	// because it leaks PII and degrades performance.
	DBLogQueries bool `env:"DB_LOG_QUERIES" required:"false" default:"false"`

	// -------------------------------------------------------------------------
	// Redis
	// -------------------------------------------------------------------------
	RedisURL string `env:"REDIS_URL" required:"false" default:""`

	// -------------------------------------------------------------------------
	// Auth — JWT signing
	// -------------------------------------------------------------------------
	// JWTSecretStub is the HMAC-SHA256 key used to issue and verify JWTs by
	// the development stub identity provider, and (for now) by the production
	// JWT verifier wired in PR-01. Production requires >= 32 bytes and must
	// not equal a known dev placeholder.
	//
	// Field name kept as JWTSecretStub for backwards compatibility with callers;
	// the canonical env var name is JWT_SIGNING_SECRET.
	JWTSecretStub string `env:"JWT_SIGNING_SECRET" required:"false" default:""`
	// EnableStubAuth activates the development-only stub identity provider.
	// Must be false in production.
	EnableStubAuth bool          `env:"ENABLE_DEV_AUTH" required:"false" default:"false"`
	JWTIssuer      string        `env:"JWT_ISSUER"      required:"false" default:"arena-dev"`
	JWTAudience    string        `env:"JWT_AUDIENCE"    required:"false" default:"arena-api"`
	JWTDefaultTTL  time.Duration `env:"JWT_DEFAULT_TTL" required:"false" default:"1h"`

	// -------------------------------------------------------------------------
	// Idempotency
	// -------------------------------------------------------------------------
	IdempotencyTTL          time.Duration `env:"IDEMPOTENCY_TTL"            required:"false" default:"24h"`
	IdempotencyKeyMaxLength int           `env:"IDEMPOTENCY_KEY_MAX_LENGTH" required:"false" default:"255"`

	// -------------------------------------------------------------------------
	// Internationalization
	// -------------------------------------------------------------------------
	DefaultLocale string   `env:"DEFAULT_LOCALE" required:"true"  default:"en"`
	ActiveLocales []string `env:"ACTIVE_LOCALES" required:"true"  default:"en"`

	// -------------------------------------------------------------------------
	// Observability
	// -------------------------------------------------------------------------
	LogLevel          string  `env:"LOG_LEVEL"                   required:"false" default:"info"`
	// LogFormat must be "json" in production for log aggregator compatibility.
	LogFormat         string  `env:"LOG_FORMAT"                  required:"false" default:"json"`
	OTLPEndpoint      string  `env:"OTEL_EXPORTER_OTLP_ENDPOINT" required:"false" default:""`
	OTELServiceName   string  `env:"OTEL_SERVICE_NAME"           required:"false" default:""`
	OTELTracesSampler float64 `env:"OTEL_TRACES_SAMPLER_ARG"     required:"false" default:"1.0"`
	OTELInsecure      bool    `env:"OTEL_EXPORTER_OTLP_INSECURE" required:"false" default:"true"`

	// -------------------------------------------------------------------------
	// Worker
	// -------------------------------------------------------------------------
	WorkerConcurrency      int           `env:"WORKER_CONCURRENCY"        required:"false" default:"4"`
	WorkerPollInterval     time.Duration `env:"WORKER_POLL_INTERVAL"      required:"false" default:"1s"`
	WorkerJobTimeout       time.Duration `env:"WORKER_JOB_TIMEOUT"        required:"false" default:"5m"`
	WorkerRetryBackoffBase time.Duration `env:"WORKER_RETRY_BACKOFF_BASE" required:"false" default:"2s"`
	WorkerRetryBackoffMax  time.Duration `env:"WORKER_RETRY_BACKOFF_MAX"  required:"false" default:"10m"`

	// -------------------------------------------------------------------------
	// Outbox dispatcher
	// -------------------------------------------------------------------------
	// OutboxMode selects the dispatch strategy. Production must be "webhook" or
	// "disabled"; "noop" and the implicit empty value are dev/test only.
	OutboxMode         OutboxMode    `env:"OUTBOX_MODE"         required:"false" default:"noop"`
	OutboxWebhookURL   string        `env:"OUTBOX_WEBHOOK_URL"  required:"false" default:""`
	OutboxSigningSecret string       `env:"OUTBOX_SIGNING_SECRET" required:"false" default:""`
	OutboxPollInterval time.Duration `env:"OUTBOX_POLL_INTERVAL" required:"false" default:"2s"`
	OutboxBatchSize    int           `env:"OUTBOX_BATCH_SIZE"   required:"false" default:"50"`

	// -------------------------------------------------------------------------
	// Email delivery
	// -------------------------------------------------------------------------
	// EmailMode selects the email delivery backend. Production must be "smtp".
	// "log" mode writes email content to the structured logger (dev/test only).
	EmailMode   EmailMode `env:"EMAIL_MODE"    required:"false" default:"log"`
	SMTPHost    string    `env:"SMTP_HOST"     required:"false" default:""`
	SMTPPort    string    `env:"SMTP_PORT"     required:"false" default:"587"`
	SMTPUsername string   `env:"SMTP_USERNAME" required:"false" default:""`
	SMTPPassword string   `env:"SMTP_PASSWORD" required:"false" default:""`
	SMTPFrom    string    `env:"SMTP_FROM"     required:"false" default:""`
	SMTPUseTLS  bool      `env:"SMTP_USE_TLS"  required:"false" default:"true"`

	// -------------------------------------------------------------------------
	// Bil24 compatibility gateway (feature #157)
	// -------------------------------------------------------------------------
	// Bil24CompatEnabled mounts the /compat/bil24/* API gateway when true.
	Bil24CompatEnabled bool `env:"BIL24_COMPAT_ENABLED" required:"false" default:"false"`

	// -------------------------------------------------------------------------
	// Media storage backend (feature #285 / G-1, Wave G)
	// -------------------------------------------------------------------------
	// MediaBackend selects which adapter the storage package constructs:
	//   "s3"    — any S3-compatible object store (AWS S3, Cloudflare R2, MinIO).
	//   "local" — filesystem under MediaLocalRoot (development & tests).
	// Empty value disables the media subsystem.
	MediaBackend   string `env:"MEDIA_BACKEND"    required:"false" default:""`
	MediaLocalRoot string `env:"MEDIA_LOCAL_ROOT" required:"false" default:""`
	// MediaSigningSecret is the HMAC key used to sign local-backend download
	// URLs. Required when MEDIA_BACKEND=local in production. In development the
	// JWT signing secret is used as a fallback if this is empty.
	MediaSigningSecret     string `env:"MEDIA_SIGNING_SECRET"       required:"false" default:""`
	MediaS3Endpoint        string `env:"MEDIA_S3_ENDPOINT"          required:"false" default:""`
	MediaS3Region          string `env:"MEDIA_S3_REGION"            required:"false" default:""`
	MediaS3Bucket          string `env:"MEDIA_S3_BUCKET"            required:"false" default:""`
	MediaS3AccessKeyID     string `env:"MEDIA_S3_ACCESS_KEY_ID"     required:"false" default:""`
	MediaS3SecretAccessKey string `env:"MEDIA_S3_SECRET_ACCESS_KEY" required:"false" default:""`
	MediaS3UsePathStyle    bool   `env:"MEDIA_S3_USE_PATH_STYLE"    required:"false" default:"true"`
}

// DBDSN is an alias for DatabaseURL that matches the terminology used in the
// architecture specification (see app_spec.txt and feature #83).
func (c *Config) DBDSN() string { return c.DatabaseURL }

// IsProduction reports whether the runtime is the production profile.
func (c *Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// IsDevelopment reports whether the runtime is the development profile.
func (c *Config) IsDevelopment() bool { return c.AppEnv == EnvDevelopment }

// EffectiveHealthAddr returns the address that healthcheck probes should
// target. When HEALTH_ADDR is explicitly configured it takes precedence; for
// arena-api the HTTP listen address is the natural fallback; for arena-worker
// the worker metrics address is used.
func (c *Config) EffectiveHealthAddr(role string) string {
	if c.HealthAddr != "" {
		return c.HealthAddr
	}
	switch role {
	case "worker":
		return c.WorkerMetricsAddr
	default:
		return c.HTTPListenAddr
	}
}

// MediaSigningKey returns the HMAC key to use for signing media download URLs.
// In production, MediaSigningSecret must be set explicitly. In development, the
// JWT signing secret is used as a fallback so developers do not need a second
// secret for local testing.
func (c *Config) MediaSigningKey() []byte {
	if c.MediaSigningSecret != "" {
		return []byte(c.MediaSigningSecret)
	}
	return []byte(c.JWTSecretStub)
}

// LogValue implements slog.LogValuer. Returns a Group of configuration
// attributes with all secret values replaced by "[REDACTED]". Use this when
// logging cfg at startup:
//
//	logger.Info("configuration loaded", "config", cfg)
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(c.LogAttrs()...)
}

// LogAttrs returns the full slice of structured log attributes for this
// configuration. Sensitive fields (JWT secret, SMTP password, signing secrets,
// S3 credentials) are replaced with the literal string "[REDACTED]".
func (c *Config) LogAttrs() []slog.Attr {
	redact := func(s string) string {
		if s == "" {
			return ""
		}
		return "[REDACTED]"
	}
	return []slog.Attr{
		slog.String("app_env", string(c.AppEnv)),
		slog.String("app_name", c.AppName),
		slog.String("app_version", c.AppVersion),
		slog.String("app_commit", c.AppCommit),
		slog.String("app_public_url", c.AppPublicURL),
		slog.String("http_listen_addr", c.HTTPListenAddr),
		slog.String("worker_metrics_addr", c.WorkerMetricsAddr),
		slog.String("database_url", redactDSN(c.DatabaseURL)),
		slog.String("redis_url", redactURL(c.RedisURL)),
		slog.String("log_level", c.LogLevel),
		slog.String("log_format", c.LogFormat),
		slog.String("jwt_signing_secret", redact(c.JWTSecretStub)),
		slog.Bool("enable_dev_auth", c.EnableStubAuth),
		slog.String("jwt_issuer", c.JWTIssuer),
		slog.String("jwt_audience", c.JWTAudience),
		slog.Duration("jwt_default_ttl", c.JWTDefaultTTL),
		slog.String("outbox_mode", string(c.OutboxMode)),
		slog.String("outbox_webhook_url", c.OutboxWebhookURL),
		slog.String("outbox_signing_secret", redact(c.OutboxSigningSecret)),
		slog.String("email_mode", string(c.EmailMode)),
		slog.String("smtp_host", c.SMTPHost),
		slog.String("smtp_port", c.SMTPPort),
		slog.String("smtp_from", c.SMTPFrom),
		slog.String("smtp_password", redact(c.SMTPPassword)),
		slog.String("media_backend", c.MediaBackend),
		slog.String("media_signing_secret", redact(c.MediaSigningSecret)),
		slog.String("media_s3_access_key_id", redact(c.MediaS3AccessKeyID)),
		slog.String("media_s3_secret_access_key", redact(c.MediaS3SecretAccessKey)),
	}
}

// Load reads configuration from environment variables, parses typed values,
// runs Validate, and returns either a populated *Config or an aggregated
// validation error.
func Load() (*Config, error) {
	cfg := &Config{
		AppEnv:     AppEnv(getenv("APP_ENV", "development")),
		AppName:    getenv("APP_NAME", "arena-api"),
		AppVersion: getenv("APP_VERSION", "0.0.0-dev"),
		AppCommit:  getenv("APP_COMMIT", "local"),
		AppPublicURL: getenv("APP_PUBLIC_URL", ""),

		HTTPListenAddr:     getenv("HTTP_LISTEN_ADDR", ":8080"),
		WorkerMetricsAddr:  getenv("WORKER_METRICS_ADDR", ":9091"),
		CORSAllowedOrigins: splitCSV(getenv("CORS_ALLOWED_ORIGINS", "*")),
		HealthAddr:         getenv("HEALTH_ADDR", ""),

		DatabaseURL: getenv("DATABASE_URL", ""),
		RedisURL:    getenv("REDIS_URL", ""),

		JWTSecretStub: getenv("JWT_SIGNING_SECRET", ""),
		JWTIssuer:        getenv("JWT_ISSUER", "arena-dev"),
		JWTAudience:      getenv("JWT_AUDIENCE", "arena-api"),

		DefaultLocale: getenv("DEFAULT_LOCALE", "en"),
		ActiveLocales: splitCSV(getenv("ACTIVE_LOCALES", "en")),

		LogLevel:        getenv("LOG_LEVEL", "info"),
		LogFormat:       getenv("LOG_FORMAT", "json"),
		OTLPEndpoint:    getenv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTELServiceName: getenv("OTEL_SERVICE_NAME", ""),

		OutboxWebhookURL:    getenv("OUTBOX_WEBHOOK_URL", ""),
		OutboxSigningSecret: getenv("OUTBOX_SIGNING_SECRET", ""),
		OutboxMode:          OutboxMode(getenv("OUTBOX_MODE", "")),

		EmailMode:    EmailMode(getenv("EMAIL_MODE", "")),
		SMTPHost:     getenv("SMTP_HOST", ""),
		SMTPPort:     getenv("SMTP_PORT", "587"),
		SMTPUsername: getenv("SMTP_USERNAME", ""),
		SMTPPassword: getenv("SMTP_PASSWORD", ""),
		SMTPFrom:     getenv("SMTP_FROM", ""),

		MediaBackend:           getenv("MEDIA_BACKEND", ""),
		MediaLocalRoot:         getenv("MEDIA_LOCAL_ROOT", ""),
		MediaSigningSecret:     getenv("MEDIA_SIGNING_SECRET", ""),
		MediaS3Endpoint:        getenv("MEDIA_S3_ENDPOINT", ""),
		MediaS3Region:          getenv("MEDIA_S3_REGION", ""),
		MediaS3Bucket:          getenv("MEDIA_S3_BUCKET", ""),
		MediaS3AccessKeyID:     getenv("MEDIA_S3_ACCESS_KEY_ID", ""),
		MediaS3SecretAccessKey: getenv("MEDIA_S3_SECRET_ACCESS_KEY", ""),
	}

	// Parse errors are collected together with Validate() errors so a single
	// boot attempt surfaces every problem at once.
	var parseErrs []error

	// OTEL sampling ratio (0.0 - 1.0).
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

	// OTEL gRPC insecure flag.
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

	// JWT default TTL.
	d, err = getenvDuration("JWT_DEFAULT_TTL", time.Hour, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.JWTDefaultTTL = d

	// Idempotency settings.
	d, err = getenvDuration("IDEMPOTENCY_TTL", 24*time.Hour, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.IdempotencyTTL = d

	iKeyMax, err := getenvInt("IDEMPOTENCY_KEY_MAX_LENGTH", 255)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.IdempotencyKeyMaxLength = iKeyMax

	// Worker settings.
	iConc, err := getenvInt("WORKER_CONCURRENCY", 4)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.WorkerConcurrency = iConc

	d, err = getenvDuration("WORKER_POLL_INTERVAL", time.Second, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.WorkerPollInterval = d

	d, err = getenvDuration("WORKER_JOB_TIMEOUT", 5*time.Minute, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.WorkerJobTimeout = d

	d, err = getenvDuration("WORKER_RETRY_BACKOFF_BASE", 2*time.Second, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.WorkerRetryBackoffBase = d

	d, err = getenvDuration("WORKER_RETRY_BACKOFF_MAX", 10*time.Minute, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.WorkerRetryBackoffMax = d

	// Outbox settings.
	d, err = getenvDuration("OUTBOX_POLL_INTERVAL", 2*time.Second, false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.OutboxPollInterval = d

	iBatch, err := getenvInt("OUTBOX_BATCH_SIZE", 50)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.OutboxBatchSize = iBatch

	// Bil24 compatibility gateway.
	b, err = getenvBool("BIL24_COMPAT_ENABLED", false)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.Bil24CompatEnabled = b

	// Media S3 path-style flag.
	b, err = getenvBool("MEDIA_S3_USE_PATH_STYLE", true)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.MediaS3UsePathStyle = b

	// SMTP TLS flag.
	b, err = getenvBool("SMTP_USE_TLS", true)
	if err != nil {
		parseErrs = append(parseErrs, err)
	}
	cfg.SMTPUseTLS = b

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
// Production-only checks enforce the PR-00 safety contract: no dev auth,
// no weak secrets, no wildcard CORS, no query logging, safe DB TLS mode,
// signed media, SMTP email delivery, explicit non-noop outbox mode, and JSON
// log format.
func (c *Config) Validate() error {
	var errs []error

	// --- AppEnv ---------------------------------------------------------------
	switch c.AppEnv {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf(
			"APP_ENV %q is invalid (allowed: development|staging|production)", c.AppEnv,
		))
	}

	// --- HTTP -----------------------------------------------------------------
	if strings.TrimSpace(c.HTTPListenAddr) == "" {
		errs = append(errs, errors.New("HTTP_LISTEN_ADDR is required"))
	}

	// --- Database -------------------------------------------------------------
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

	// --- Locale ---------------------------------------------------------------
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

	// --- Log format / level ---------------------------------------------------
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

	// --- OTEL -----------------------------------------------------------------
	if c.OTELTracesSampler < 0.0 || c.OTELTracesSampler > 1.0 {
		errs = append(errs, fmt.Errorf(
			"OTEL_TRACES_SAMPLER_ARG must be in [0.0, 1.0] (got %v)", c.OTELTracesSampler,
		))
	}

	// --- Auth stub ------------------------------------------------------------
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

	// --- Outbox dispatcher ----------------------------------------------------
	switch c.OutboxMode {
	case OutboxModeNoop, OutboxModeWebhook, OutboxModeDisabled, "":
		// basic values accepted; further production checks below
	default:
		errs = append(errs, fmt.Errorf(
			"OUTBOX_MODE %q is invalid (allowed: noop|webhook|disabled or empty for dev default)",
			c.OutboxMode,
		))
	}
	if c.OutboxMode == OutboxModeWebhook {
		if strings.TrimSpace(c.OutboxWebhookURL) == "" {
			errs = append(errs, errors.New(
				"OUTBOX_WEBHOOK_URL is required when OUTBOX_MODE=webhook",
			))
		}
	}

	// --- Email delivery -------------------------------------------------------
	switch c.EmailMode {
	case EmailModeLog, EmailModeSMTP, "":
		// basic values accepted; further production checks below
	default:
		errs = append(errs, fmt.Errorf(
			"EMAIL_MODE %q is invalid (allowed: log|smtp or empty for dev default)",
			c.EmailMode,
		))
	}
	if c.EmailMode == EmailModeSMTP {
		if strings.TrimSpace(c.SMTPHost) == "" {
			errs = append(errs, errors.New(
				"SMTP_HOST is required when EMAIL_MODE=smtp",
			))
		}
		if strings.TrimSpace(c.SMTPFrom) == "" {
			errs = append(errs, errors.New(
				"SMTP_FROM is required when EMAIL_MODE=smtp",
			))
		}
	}

	// --- Media storage backend ------------------------------------------------
	switch strings.ToLower(strings.TrimSpace(c.MediaBackend)) {
	case "":
		// inactive — fine
	case "local":
		if strings.TrimSpace(c.MediaLocalRoot) == "" {
			errs = append(errs, errors.New(
				"MEDIA_LOCAL_ROOT is required when MEDIA_BACKEND=local",
			))
		}
	case "s3":
		var missing []string
		if strings.TrimSpace(c.MediaS3Endpoint) == "" {
			missing = append(missing, "MEDIA_S3_ENDPOINT")
		}
		if strings.TrimSpace(c.MediaS3Region) == "" {
			missing = append(missing, "MEDIA_S3_REGION")
		}
		if strings.TrimSpace(c.MediaS3Bucket) == "" {
			missing = append(missing, "MEDIA_S3_BUCKET")
		}
		if strings.TrimSpace(c.MediaS3AccessKeyID) == "" {
			missing = append(missing, "MEDIA_S3_ACCESS_KEY_ID")
		}
		if strings.TrimSpace(c.MediaS3SecretAccessKey) == "" {
			missing = append(missing, "MEDIA_S3_SECRET_ACCESS_KEY")
		}
		if len(missing) > 0 {
			errs = append(errs, fmt.Errorf(
				"MEDIA_BACKEND=s3 requires: %s", strings.Join(missing, ", "),
			))
		}
	default:
		errs = append(errs, fmt.Errorf(
			"MEDIA_BACKEND %q is invalid (allowed: s3|local or empty for inactive)",
			c.MediaBackend,
		))
	}

	// =========================================================================
	// PRODUCTION-ONLY safety checks (PR-00)
	// =========================================================================
	if c.AppEnv == EnvProduction {
		errs = append(errs, c.validateProduction()...)
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// validateProduction returns validation errors specific to APP_ENV=production.
// It is called from Validate() when AppEnv == EnvProduction.
//
// Production rejects every unsafe silent fallback:
//   - weak/missing JWT secret (< 32 bytes or known dev placeholder)
//   - dev auth enabled
//   - wildcard CORS
//   - query logging enabled
//   - unsafe DB TLS mode (sslmode=disable or sslmode=allow)
//   - unsigned local media (MEDIA_BACKEND=local without MEDIA_SIGNING_SECRET)
//   - log-only email (EMAIL_MODE=log or not configured)
//   - implicit/noop outbox mode (OUTBOX_MODE empty or noop)
//   - text log format
func (c *Config) validateProduction() []error {
	var errs []error

	// 1. JWT secret must be present and strong.
	secret := strings.TrimSpace(c.JWTSecretStub)
	if secret == "" {
		errs = append(errs, errors.New(
			"JWT_SIGNING_SECRET is required in production (APP_ENV=production)",
		))
	} else if len(secret) < minJWTSecretLen {
		errs = append(errs, fmt.Errorf(
			"JWT_SIGNING_SECRET is too short for production: must be at least %d bytes, got %d",
			minJWTSecretLen, len(secret),
		))
	} else if isKnownDevSecret(secret) {
		errs = append(errs, errors.New(
			"JWT_SIGNING_SECRET is a known development placeholder and must not be used in production",
		))
	}

	// 2. Dev auth must be disabled (also enforced by EnableStubAuth block, but
	// repeat here so the error appears in production context too).
	// (Already covered by validateProduction guard, but explicit is better.)

	// 3. Wildcard CORS is forbidden.
	for _, origin := range c.CORSAllowedOrigins {
		if strings.TrimSpace(origin) == "*" {
			errs = append(errs, errors.New(
				"CORS_ALLOWED_ORIGINS must not contain wildcard '*' in production",
			))
			break
		}
	}

	// 4. Query logging exposes PII and degrades performance.
	if c.DBLogQueries {
		errs = append(errs, errors.New(
			"DB_LOG_QUERIES must be false in production",
		))
	}

	// 5. Unsafe DB TLS mode.
	if unsafeDBTLS(c.DatabaseURL) {
		errs = append(errs, fmt.Errorf(
			"DATABASE_URL uses an unsafe TLS mode in production; use sslmode=require or sslmode=verify-full",
		))
	}

	// 6. Local media backend requires an explicit signing secret in production.
	// (Using the JWT secret as a fallback is only acceptable in development.)
	if strings.EqualFold(strings.TrimSpace(c.MediaBackend), "local") {
		if strings.TrimSpace(c.MediaSigningSecret) == "" {
			errs = append(errs, errors.New(
				"MEDIA_SIGNING_SECRET is required when MEDIA_BACKEND=local in production",
			))
		} else if len(strings.TrimSpace(c.MediaSigningSecret)) < minJWTSecretLen {
			errs = append(errs, fmt.Errorf(
				"MEDIA_SIGNING_SECRET must be at least %d bytes in production, got %d",
				minJWTSecretLen, len(strings.TrimSpace(c.MediaSigningSecret)),
			))
		}
	}

	// 7. Log-only email is forbidden. Must be smtp.
	if c.EmailMode == EmailModeLog || c.EmailMode == "" {
		errs = append(errs, errors.New(
			"EMAIL_MODE must be 'smtp' in production; log-only delivery is forbidden",
		))
	}

	// 8. Implicit or noop outbox mode is forbidden. Must be webhook or disabled.
	if c.OutboxMode == OutboxModeNoop || c.OutboxMode == "" {
		errs = append(errs, errors.New(
			"OUTBOX_MODE must be 'webhook' or 'disabled' in production; noop/implicit mode is forbidden",
		))
	}

	// 9. Log format must be JSON in production for log aggregator compatibility.
	if !strings.EqualFold(c.LogFormat, "json") {
		errs = append(errs, errors.New(
			"LOG_FORMAT must be 'json' in production",
		))
	}

	return errs
}

// isKnownDevSecret returns true when s matches a well-known development
// placeholder that must never be used in production.
func isKnownDevSecret(s string) bool {
	known := []string{
		"dev-only-do-not-use-in-prod",
		"dev-secret",
		"secret",
		"changeme",
		"password",
		"insecure",
	}
	lower := strings.ToLower(strings.TrimSpace(s))
	for _, k := range known {
		if lower == k {
			return true
		}
	}
	return false
}

// unsafeDBTLS returns true when the DATABASE_URL contains an unsafe sslmode
// (disable or allow). Both modes transmit credentials without server
// verification and are forbidden in production.
func unsafeDBTLS(dsn string) bool {
	lower := strings.ToLower(dsn)
	return strings.Contains(lower, "sslmode=disable") ||
		strings.Contains(lower, "sslmode=allow")
}

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

func getenvInt(key string, def int) (int, error) {
	n, err := getenvInt64(key, int64(def))
	if err != nil {
		return 0, err
	}
	return int(n), nil
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

// redactDSN strips credentials from a DSN before placing it in error messages
// or log output. Best-effort: if parsing fails we return a generic placeholder.
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

// redactURL strips credentials from a generic URL (e.g. Redis DSN).
func redactURL(u string) string {
	if u == "" {
		return ""
	}
	return redactDSN(u)
}

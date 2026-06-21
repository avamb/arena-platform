// Package logging configures the slog-based structured logger used across
// arena_new binaries.
//
// The package provides:
//
//   - New / NewWithOptions — construct a *slog.Logger writing JSON (default)
//     or text records, with optional default attributes for app/env/version.
//   - WithLogger / FromContext — attach and retrieve a logger from a
//     context.Context. FromContext automatically enriches the returned logger
//     with any request_id, correlation_id, and trace_id values stored on the
//     context, so call sites only need `logging.FromContext(ctx).Info(...)`
//     to get fully-tagged records.
//   - WithRequestID / WithCorrelationID / WithTraceID — attach correlation
//     identifiers to a context. The matching RequestID / CorrelationID /
//     TraceID accessors return the stored value (or "" if absent).
//   - Defaults — collect default attributes (app, env, version) from explicit
//     overrides, runtime/debug.ReadBuildInfo(), and the APP_ENV environment
//     variable. Useful for building loggers outside of NewWithOptions.
//
// All public APIs are safe for concurrent use after construction.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Field keys used for default and context-derived attributes.
// Exported so tests and middleware can reference them without typos.
const (
	FieldApp           = "app"
	FieldEnv           = "env"
	FieldVersion       = "version"
	FieldRequestID     = "request_id"
	FieldCorrelationID = "correlation_id"
	FieldTraceID       = "trace_id"
)

// ctxKey is the unexported context key type used to attach a logger or
// correlation identifiers to a Context.
type ctxKey int

const (
	loggerKey ctxKey = iota
	requestIDKey
	correlationIDKey
	traceIDKey
)

// Options configures NewWithOptions. Zero values are sensible defaults:
//
//   - Writer: os.Stdout
//   - Format: "json"
//   - Level:  "info"
//   - App / Env / Version: resolved via Defaults() — see that function for
//     fallback rules.
type Options struct {
	Writer  io.Writer
	Format  string // "json" (default) or "text"
	Level   string // "debug" | "info" | "warn" | "error"; defaults to "info"
	App     string // overrides build-info module / argv[0] basename
	Env     string // overrides APP_ENV environment variable
	Version string // overrides build-info main.Version
}

// New constructs a slog.Logger configured for the given format ("json" or
// "text") and level ("debug" | "info" | "warn" | "error"). Output goes to w
// (typically os.Stdout). Unknown level strings fall back to info.
//
// New is a thin compatibility wrapper around NewWithOptions that does NOT
// attach default attributes; callers that want app/env/version baked in
// should use NewWithOptions or compose with Defaults() / logger.With(...).
func New(w io.Writer, format, level string) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}

	var h slog.Handler
	switch strings.ToLower(format) {
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// NewWithOptions builds a slog.Logger that has default attributes (app, env,
// version) attached to every record. Use this constructor in production
// binaries (arena-api, arena-worker, arena-migrate) so that all log lines
// are uniformly tagged regardless of where they originate.
func NewWithOptions(opts Options) *slog.Logger {
	logger := New(opts.Writer, opts.Format, opts.Level)
	attrs := Defaults(opts.App, opts.Env, opts.Version)
	if len(attrs) == 0 {
		return logger
	}
	return logger.With(attrs...)
}

// Defaults returns slog default attributes (app, env, version) suitable for
// attaching to a logger via logger.With(Defaults(...)...). Resolution order
// per field:
//
//	app:     explicit override → debug.ReadBuildInfo().Main.Path basename →
//	         argv[0] basename (without .exe)
//	env:     explicit override → APP_ENV environment variable
//	version: explicit override → debug.ReadBuildInfo().Main.Version
//
// Empty/unknown values are omitted from the returned slice; the function
// never panics and is safe to call on every process startup.
func Defaults(app, env, version string) []any {
	app = strings.TrimSpace(app)
	env = strings.TrimSpace(env)
	version = strings.TrimSpace(version)

	info, infoOK := debug.ReadBuildInfo()

	if app == "" {
		if infoOK && info.Main.Path != "" {
			parts := strings.Split(info.Main.Path, "/")
			app = parts[len(parts)-1]
		}
		if app == "" && len(os.Args) > 0 {
			app = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		}
	}

	if env == "" {
		env = strings.TrimSpace(os.Getenv("APP_ENV"))
	}

	if version == "" && infoOK {
		// Main.Version is "(devel)" for `go run` builds and the module
		// version for installed binaries; either is more useful than "".
		version = info.Main.Version
	}

	var attrs []any
	if app != "" {
		attrs = append(attrs, slog.String(FieldApp, app))
	}
	if env != "" {
		attrs = append(attrs, slog.String(FieldEnv, env))
	}
	if version != "" {
		attrs = append(attrs, slog.String(FieldVersion, version))
	}
	return attrs
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger returns a context carrying the provided logger. Passing a nil
// logger returns ctx unchanged so callers can chain unconditionally.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext returns the logger attached to ctx (or slog.Default() if none),
// automatically enriched with any request_id / correlation_id / trace_id
// values previously stored on the context via WithRequestID, WithCorrelationID,
// or WithTraceID. Call sites should therefore do:
//
//	logging.FromContext(ctx).Info("event", "key", value)
//
// to get fully-tagged records without manually re-passing correlation IDs.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	logger, _ := ctx.Value(loggerKey).(*slog.Logger)
	if logger == nil {
		logger = slog.Default()
	}

	var attrs []any
	if id := RequestID(ctx); id != "" {
		attrs = append(attrs, slog.String(FieldRequestID, id))
	}
	if id := CorrelationID(ctx); id != "" {
		attrs = append(attrs, slog.String(FieldCorrelationID, id))
	}
	if id := TraceID(ctx); id != "" {
		attrs = append(attrs, slog.String(FieldTraceID, id))
	}
	if len(attrs) > 0 {
		logger = logger.With(attrs...)
	}
	return logger
}

// WithRequestID stores a request identifier on ctx. Subsequent calls to
// FromContext(ctx) return a logger whose records include a "request_id"
// attribute. Empty ids are ignored to keep call sites declarative.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the request identifier previously stored via
// WithRequestID, or "" if none was set.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// WithCorrelationID stores a correlation identifier on ctx. Subsequent calls
// to FromContext(ctx) return a logger whose records include a
// "correlation_id" attribute. Empty ids are ignored.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationID extracts the correlation identifier previously stored via
// WithCorrelationID, or "" if none was set.
func CorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(correlationIDKey).(string); ok {
		return v
	}
	return ""
}

// WithTraceID stores an OpenTelemetry-style trace identifier on ctx.
// Subsequent calls to FromContext(ctx) return a logger whose records include
// a "trace_id" attribute. Empty ids are ignored. When the OTEL SDK is wired
// in (Wave 7), middleware can derive trace ids from the active SpanContext
// and forward them here.
func WithTraceID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceID extracts the trace identifier previously stored via WithTraceID,
// or "" if none was set.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

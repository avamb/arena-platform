// Package http (imported as httpadapter to avoid colliding with the
// standard-library net/http identifier) owns the chi router construction
// and the standard cross-cutting middleware chain consumed by every
// arena_new HTTP listener.
//
// Feature #89 ("chi router + middleware chain") tasks this package with:
//
//   - Exposing a single NewRouter(deps) chi.Router factory that returns a
//     chi.Router with the canonical middleware chain pre-applied. Callers
//     (arena-api, future arena-admin, integration tests) mount their own
//     routes on top of the returned router via the standard chi API
//     (Get / Post / Route / Group / Method / Mount).
//
//   - Owning the cross-cutting middlewares the chain depends on:
//
//       1. Recoverer          — chi.Recoverer wraps every downstream
//                                handler so a panic anywhere in the chain
//                                returns 500 (with a structured log record)
//                                instead of crashing the process.
//       2. RealIP             — chi.RealIP rewrites r.RemoteAddr from the
//                                forwarded-for / forwarded-host headers so
//                                downstream code sees the actual client IP
//                                when arena-api is behind Dokploy's
//                                reverse proxy.
//       3. RequestID          — chi.RequestID generates a per-request
//                                identifier and stores it on r.Context().
//       4. requestContext     — surfaces the chi RequestID via the
//                                X-Request-Id response header and copies
//                                it into the slog context via
//                                logging.WithRequestID, so every
//                                logging.FromContext(ctx) call automatically
//                                emits a "request_id" attribute.
//       5. loggerMiddleware   — attaches the base *slog.Logger to ctx via
//                                logging.WithLogger so any downstream
//                                logging.FromContext(ctx) returns a logger
//                                derived from the configured handler (JSON
//                                or text, info or debug level, …).
//       6. prometheusMiddleware (optional, only when Deps.Metrics != nil) —
//                                records arena_http_request_duration_seconds
//                                + arena_http_requests_total with low
//                                cardinality (method, normalised
//                                chi RoutePattern, status string) labels.
//       7. tracerMiddleware   — extracts the incoming W3C TraceContext via
//                                the global OTel propagator, opens a
//                                SpanKindServer span via the supplied
//                                tracer, surfaces the W3C trace_id via the
//                                X-Trace-Id response header, and copies it
//                                into the slog context via
//                                logging.WithTraceID. When the OTel SDK is
//                                in disabled mode (no exporter; see
//                                observability.InitTracer with an empty
//                                Endpoint) the span is a no-op but the
//                                middleware still produces a random trace
//                                id so the X-Trace-Id contract holds.
//       8. Timeout            — chi.Timeout caps r.Context() with a
//                                deadline derived from Deps.RequestTimeout
//                                so a slow handler cannot stall the listener.
//       9. jsonBodyLimit      — http.MaxBytesReader cap on POST/PUT/PATCH
//                                bodies so an oversized payload cannot
//                                exhaust process memory before Timeout
//                                fires.
//
// The chain is ordered so the outermost middleware (Recoverer) covers a
// panic anywhere downstream — including the OTel span finaliser — and the
// identifier-injecting middlewares (RequestID, requestContext) run before
// loggerMiddleware so every log record produced by handlers automatically
// carries the request_id / trace_id pair.
//
// NewRouter is intentionally NOT responsible for mounting routes. The
// caller (httpserver.Server) owns the operational (/healthz, /readyz,
// /metrics) and /v1 routes because those are tightly coupled to handler
// methods that live next to the lifecycle code. Splitting the chain
// construction here keeps the Server focused on the http.Server lifecycle
// (listen / graceful shutdown / signal binding) while this package owns
// the cross-cutting "what runs on every request" contract.
package http

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTP response headers surfaced by the standard middleware chain. Exported
// so handler code and integration tests can refer to them without typos.
const (
	HeaderRequestID = "X-Request-Id"
	HeaderTraceID   = "X-Trace-Id"
)

// TracerName is the OTel instrumentation library identifier used when the
// caller does not supply its own trace.Tracer. Mirrors the import path so
// span dumps point back to the source of the instrumentation.
const TracerName = "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"

// Deps bundles the cross-cutting dependencies that the middleware chain
// requires. Every field is optional; sensible defaults are applied in
// NewRouter so a caller can construct the simplest possible router with
// httpadapter.NewRouter(httpadapter.Deps{}) — useful in tests.
type Deps struct {
	// Logger is the base *slog.Logger attached to every request context
	// via logging.WithLogger. Downstream handlers call
	// logging.FromContext(r.Context()) to retrieve a logger automatically
	// enriched with request_id and trace_id. Defaults to slog.Default().
	Logger *slog.Logger

	// RequestTimeout caps the per-request context deadline via chi.Timeout.
	// Zero disables the middleware (useful for streaming endpoints or unit
	// tests that need indefinite deadlines).
	RequestTimeout time.Duration

	// BodyLimitBytes caps the size of POST/PUT/PATCH request bodies via
	// http.MaxBytesReader. Zero disables the limit.
	BodyLimitBytes int64

	// Metrics is the shared *observability.Metrics that backs the
	// prometheusMiddleware HTTP histogram + counter. Nil disables the
	// middleware (useful in tests that don't want metric noise).
	Metrics *observability.Metrics

	// Propagator overrides the W3C trace context propagator. Defaults to
	// otel.GetTextMapPropagator() so the package picks up whatever the
	// observability.InitTracer call configured globally.
	Propagator propagation.TextMapPropagator

	// Tracer overrides the OTel tracer used to open the per-request span.
	// Defaults to otel.Tracer(TracerName) so the global TracerProvider
	// installed by observability.InitTracer drives sampling and export.
	Tracer trace.Tracer
}

// NewRouter constructs a chi.Router with the canonical arena_new
// middleware chain pre-applied. Callers attach routes on the returned
// router via the standard chi API; the cross-cutting chain runs before
// every handler regardless of route.
//
// The returned chi.Router is a *chi.Mux internally — callers that need the
// concrete type can type-assert, but the interface is sufficient for
// route mounting (Get / Post / Route / Group / Method / Mount / Handle).
func NewRouter(deps Deps) chi.Router {
	deps = deps.withDefaults()

	r := chi.NewRouter()

	// 1. Recoverer — outermost so a panic anywhere downstream becomes a
	//    500 with a structured log line rather than a process crash.
	r.Use(chimw.Recoverer)
	// 2. RealIP — must run before any middleware that reads r.RemoteAddr.
	r.Use(chimw.RealIP)
	// 3. RequestID — populates chimw.GetReqID(ctx).
	r.Use(chimw.RequestID)
	// 4. requestContext — copies the chi RequestID into the X-Request-Id
	//    response header and into the slog ctx.
	r.Use(requestContext)
	// 5. loggerMiddleware — attaches the configured base logger to ctx.
	r.Use(loggerMiddleware(deps.Logger))
	// 6. prometheusMiddleware — only when Metrics was supplied.
	if deps.Metrics != nil {
		r.Use(prometheusMiddleware(deps.Metrics))
	}
	// 7. tracerMiddleware — opens an OTel span, sets X-Trace-Id, copies
	//    the trace_id into the slog ctx.
	r.Use(tracerMiddleware(deps.Propagator, deps.Tracer))
	// 7b. requestLogMiddleware — emits "http request start/end" records with
	//     sensitive headers (Authorization, Cookie) masked so that raw bearer
	//     tokens and session cookies never appear in log output.
	r.Use(requestLogMiddleware(deps.Logger))
	// 8. Timeout — only when RequestTimeout > 0.
	if deps.RequestTimeout > 0 {
		r.Use(chimw.Timeout(deps.RequestTimeout))
	}
	// 9. jsonBodyLimit — only when BodyLimitBytes > 0.
	if deps.BodyLimitBytes > 0 {
		r.Use(JSONBodyLimit(deps.BodyLimitBytes))
	}

	return r
}

// withDefaults returns a copy of deps with zero-value fields filled in by
// the package-level fallbacks documented on Deps.
func (d Deps) withDefaults() Deps {
	out := d
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	if out.Propagator == nil {
		out.Propagator = otel.GetTextMapPropagator()
	}
	if out.Tracer == nil {
		out.Tracer = otel.Tracer(TracerName)
	}
	return out
}

// -----------------------------------------------------------------------------
// Middleware implementations
// -----------------------------------------------------------------------------

// requestContext copies the chi RequestID (set upstream by chimw.RequestID)
// into:
//
//   - the X-Request-Id response header — the public contract that every
//     response carries an identifier the client can quote in support
//     requests.
//   - the slog context via logging.WithRequestID — so any subsequent
//     logging.FromContext(ctx) returns a logger with a "request_id"
//     attribute attached without per-call boilerplate.
//
// If chimw.RequestID was not installed upstream (defensive — tests that
// build a partial chain), this middleware mints its own 16-byte random hex
// identifier so the X-Request-Id contract still holds.
func requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := chimw.GetReqID(r.Context())
		if reqID == "" {
			reqID = newRandomHex(16)
		}
		w.Header().Set(HeaderRequestID, reqID)
		ctx := logging.WithRequestID(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggerMiddleware attaches base to the request context via
// logging.WithLogger. Downstream code that calls logging.FromContext(ctx)
// then receives a logger derived from base (preserving its handler, level,
// and default attributes) automatically enriched with the request_id /
// correlation_id / trace_id attributes pulled from ctx.
//
// If base is nil the middleware is a no-op pass-through; logging.FromContext
// will fall through to slog.Default().
func loggerMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if base == nil {
				next.ServeHTTP(w, r)
				return
			}
			ctx := logging.WithLogger(r.Context(), base)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// prometheusMiddleware records the request latency and total count on the
// supplied *observability.Metrics. Three labels are emitted:
//
//   - method : HTTP method as-is (GET, POST, …).
//   - route  : the chi route pattern resolved AFTER the handler ran (e.g.
//              "/v1/orders/{id}" rather than "/v1/orders/abc-123") so
//              cardinality is bounded by the route table, not by URL
//              parameter values. A request that produced a 404 (no match)
//              is labelled "unmatched" to preserve the same bound.
//   - status : decimal HTTP status string. If the handler returned without
//              calling WriteHeader, chi's WrapResponseWriter reports 0 —
//              we coerce that to "200" because Go's net/http documents an
//              implicit 200 in that case.
//
// The middleware uses chi.middleware.WrapResponseWriter so it can read the
// final status code AFTER the handler ran without depending on the
// handler's cooperation.
func prometheusMiddleware(m *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			status := ww.Status()
			if status == 0 {
				// net/http: an absent WriteHeader call is treated as 200.
				status = http.StatusOK
			}
			statusStr := strconv.Itoa(status)
			elapsed := time.Since(start).Seconds()

			m.HTTPRequestDuration.WithLabelValues(r.Method, route, statusStr).Observe(elapsed)
			m.HTTPRequestsTotal.WithLabelValues(r.Method, route, statusStr).Inc()
		})
	}
}

// tracerMiddleware opens an OTel server span around every request:
//
//  1. Extracts the incoming W3C TraceContext via the supplied propagator
//     so a request that hops from a sister service carries the same
//     trace_id end-to-end.
//
//  2. Calls tracer.Start with SpanKindServer and a small set of standard
//     http.* attributes (method, target, scheme). Per the OTel HTTP
//     semantic conventions the status_code attribute is set AFTER the
//     handler runs, once chimw.WrapResponseWriter knows what was written.
//
//  3. Surfaces the resolved trace_id via the X-Trace-Id response header
//     and via logging.WithTraceID(ctx, ...). When the global tracer is in
//     disabled mode (observability.InitTracer with an empty Endpoint) the
//     span has a zero TraceID — we mint a random hex string in that case
//     so the response-header contract still holds and integration tests
//     have something to assert on.
//
// Calling tracer.Start always succeeds; in disabled mode it returns a
// no-op span whose End() is a cheap function call, so the per-request
// overhead is bounded.
func tracerMiddleware(propagator propagation.TextMapPropagator, tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Extract incoming trace context from request headers.
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			// 2. Open a server span. Name follows the OTel HTTP convention
			//    "<METHOD> <route>" — but the route is unknown until chi
			//    matches, so we fall back to the raw URL path.
			spanName := r.Method + " " + r.URL.Path
			ctx, span := tracer.Start(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.target", r.URL.Path),
					attribute.String("http.scheme", schemeOf(r)),
				),
			)
			defer span.End()

			// 3. Resolve trace_id for the X-Trace-Id header. Prefer the
			//    real OTel SpanContext when sampling is on; otherwise
			//    honour an inbound X-Trace-Id (lets the caller pin one);
			//    otherwise generate a random 128-bit hex string.
			traceID := ""
			if sc := span.SpanContext(); sc.TraceID().IsValid() {
				traceID = sc.TraceID().String()
			}
			if traceID == "" {
				traceID = strings.TrimSpace(r.Header.Get(HeaderTraceID))
			}
			if traceID == "" {
				traceID = newRandomHex(16)
			}
			w.Header().Set(HeaderTraceID, traceID)
			ctx = logging.WithTraceID(ctx, traceID)

			// 4. Hand off to the next handler and record the final status
			//    on the span so traces show 4xx/5xx without parsing the
			//    response body.
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(ctx))
			span.SetAttributes(attribute.Int("http.status_code", ww.Status()))
		})
	}
}

// JSONBodyLimit returns a middleware that wraps r.Body with
// http.MaxBytesReader for POST/PUT/PATCH requests so an oversized payload
// cannot exhaust process memory before chi.Timeout fires. Safe methods are
// passed through untouched.
//
// Exported (capitalised) so callers that build a router outside NewRouter
// — for example tests or future arena-admin binaries — can reuse the same
// body-limit semantics without duplicating the cap logic.
func JSONBodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				if maxBytes > 0 {
					r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// -----------------------------------------------------------------------------
// Request logging with sensitive-header masking
// -----------------------------------------------------------------------------

// MaskSensitiveHeader returns a safe-to-log representation of an HTTP header
// value. The header name comparison is case-insensitive. The following headers
// are masked before the value reaches any slog output:
//
//   - authorization / proxy-authorization: scheme is preserved, credential
//     replaced — "Bearer eyJ…" becomes "Bearer ***", "Basic dXNl…" becomes
//     "Basic ***".
//   - cookie / set-cookie: entire value replaced with "<redacted>" because
//     cookie strings can contain session identifiers and CSRF tokens.
//
// All other header names are returned unchanged so request tracing retains
// useful diagnostic information (Content-Type, Accept-Language, etc.) without
// leaking credentials.
//
// Exported so callers outside the package (e.g. integration-test helpers) can
// apply the same masking rule without duplicating the logic.
func MaskSensitiveHeader(name, value string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization":
		// Preserve the scheme name so operators know WHICH kind of credential
		// was presented (Bearer vs Basic vs Digest) without seeing the secret.
		// "Bearer eyJhbGci…" → "Bearer ***"
		// "Basic dXNlcjpwYXNz" → "Basic ***"
		if idx := strings.IndexByte(value, ' '); idx > 0 {
			return value[:idx] + " ***"
		}
		// No space — value has no recognisable scheme; redact in full.
		return "<redacted>"
	case "cookie", "set-cookie":
		// Cookie values often embed session IDs, CSRF tokens, and auth material.
		// Redact the entire value rather than attempting per-cookie parsing.
		return "<redacted>"
	}
	return value
}

// requestLogMiddleware emits structured "http request start" and "http request
// end" slog records for every request. Sensitive headers are masked via
// MaskSensitiveHeader before reaching the log output so raw bearer tokens and
// session cookies never appear in log files or log-aggregation systems.
//
// The middleware is positioned AFTER tracerMiddleware in the canonical chain so
// that logging.FromContext(r.Context()) returns a logger already enriched with
// both request_id (set by requestContext) and trace_id (set by tracerMiddleware)
// — every log record produced here is therefore automatically correlated to the
// right distributed trace.
//
// The Authorization and Cookie headers are always included in "request start"
// when they are present, masked per MaskSensitiveHeader. This gives operators
// visibility into which requests carried credentials (and which scheme) without
// exposing the credential itself.
func requestLogMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// logging.FromContext inherits request_id and trace_id from ctx so
			// both identifiers appear on every record without per-site boilerplate.
			logger := logging.FromContext(r.Context())
			if logger == nil {
				logger = base
			}
			if logger == nil {
				logger = slog.Default()
			}

			start := time.Now()

			// Build "request start" log args. Always include method, path, and
			// the real client address. Sensitive headers are appended only when
			// they are present in the request.
			args := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			}
			if auth := r.Header.Get("Authorization"); auth != "" {
				args = append(args, "authorization", MaskSensitiveHeader("authorization", auth))
			}
			if cookie := r.Header.Get("Cookie"); cookie != "" {
				args = append(args, "cookie", MaskSensitiveHeader("cookie", cookie))
			}
			logger.Info("http request start", args...)

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(r.Context()))

			logger.Info("http request end",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes_out", ww.BytesWritten(),
				"elapsed_ms", float64(time.Since(start).Microseconds())/1000.0,
			)
		})
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// schemeOf returns the wire scheme used to reach the server: "https" when
// the request rode TLS, the value of the X-Forwarded-Proto header when a
// trusted reverse proxy set it, otherwise "http".
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); scheme != "" {
		return scheme
	}
	return "http"
}

// newRandomHex returns a hex-encoded random byte string of the requested
// byte length. Used as a fallback when chi's RequestID middleware was not
// installed and when the OTel SDK is disabled (no real trace IDs). On
// crypto/rand failure we fall back to a wall-clock-derived id; we never
// panic in the hot path.
func newRandomHex(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 16
	}
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		t := uint64(time.Now().UnixNano())
		for i := 0; i < len(b) && i < 8; i++ {
			b[i] = byte(t >> (i * 8))
		}
	}
	return hex.EncodeToString(b)
}

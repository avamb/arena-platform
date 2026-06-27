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
//     1. Recoverer          — chi.Recoverer wraps every downstream
//     handler so a panic anywhere in the chain
//     returns 500 (with a structured log record)
//     instead of crashing the process.
//     2. RealIP             — chi.RealIP rewrites r.RemoteAddr from the
//     forwarded-for / forwarded-host headers so
//     downstream code sees the actual client IP
//     when arena-api is behind Dokploy's
//     reverse proxy.
//     3. RequestID          — chi.RequestID generates a per-request
//     identifier and stores it on r.Context().
//     4. requestContext     — surfaces the chi RequestID via the
//     X-Request-Id response header and copies
//     it into the slog context via
//     logging.WithRequestID, so every
//     logging.FromContext(ctx) call automatically
//     emits a "request_id" attribute.
//     5. loggerMiddleware   — attaches the base *slog.Logger to ctx via
//     logging.WithLogger so any downstream
//     logging.FromContext(ctx) returns a logger
//     derived from the configured handler (JSON
//     or text, info or debug level, …).
//     6. prometheusMiddleware (optional, only when Deps.Metrics != nil) —
//     records arena_http_request_duration_seconds
//
//   - arena_http_requests_total with low
//     cardinality (method, normalised
//     chi RoutePattern, status string) labels.
//     7. tracerMiddleware   — extracts the incoming W3C TraceContext via
//     the global OTel propagator, opens a
//     SpanKindServer span via the supplied
//     tracer, surfaces the W3C trace_id via the
//     X-Trace-Id response header, and copies it
//     into the slog context via
//     logging.WithTraceID. When the OTel SDK is
//     in disabled mode (no exporter; see
//     observability.InitTracer with an empty
//     Endpoint) the span is a no-op but the
//     middleware still produces a random trace
//     id so the X-Trace-Id contract holds.
//     8. Timeout            — chi.Timeout caps r.Context() with a
//     deadline derived from Deps.RequestTimeout
//     so a slow handler cannot stall the listener.
//     9. jsonBodyLimit      — http.MaxBytesReader cap on POST/PUT/PATCH
//     bodies so an oversized payload cannot
//     exhaust process memory before Timeout
//     fires.
//     10. RequireJSONContentType — enforces Content-Type: application/json
//     on POST/PUT/PATCH requests. Returns 415
//     Unsupported Media Type with the project-
//     standard JSON error envelope when the
//     header is absent or uses an unsupported
//     media type. Adds Accept-Post: application/json
//     to every 415 response.
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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
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
	// prometheusMiddleware HTTP histogram + counter and the panic counter.
	// Nil disables both the prometheusMiddleware and the panic counter
	// (useful in tests that don't want metric noise).
	Metrics *observability.Metrics

	// AppEnv is the deployment profile string (e.g. "development",
	// "production"). The panic recoverer uses this to decide whether to
	// include the goroutine stack trace in the error response body:
	//   - "development"         → stack included (developer convenience)
	//   - "production"|"staging" → stack excluded (no information leak)
	// Defaults to "development" when empty so tests are not accidentally
	// treated as production.
	AppEnv string

	// CORSAllowedOrigins controls which browser origins may call the API.
	// "*" allows any origin and is the development default. Empty disables
	// CORS headers entirely for callers that terminate CORS at a proxy.
	CORSAllowedOrigins []string

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
	//    We use our own panicRecoverer instead of chimw.Recoverer so we can
	//    log the panic at ERROR level via slog, increment the Prometheus
	//    http_panics_total counter, and return the project-standard JSON
	//    error envelope.  In development mode the stack trace is also
	//    included in the response body for developer convenience; in
	//    production / staging it is omitted to prevent information leaks.
	r.Use(panicRecoverer(deps.Logger, deps.Metrics, deps.AppEnv))
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
	// 7c. CORS — must run before timeout/body/content-type checks so browser
	//     preflight OPTIONS requests do not fall through to chi's 405 handler.
	if len(deps.CORSAllowedOrigins) > 0 {
		r.Use(corsMiddleware(deps.CORSAllowedOrigins))
	}
	// 8. Timeout — only when RequestTimeout > 0.
	if deps.RequestTimeout > 0 {
		r.Use(chimw.Timeout(deps.RequestTimeout))
	}
	// 9. jsonBodyLimit — only when BodyLimitBytes > 0.
	if deps.BodyLimitBytes > 0 {
		r.Use(JSONBodyLimit(deps.BodyLimitBytes))
	}
	// 10. RequireJSONContentType — enforces Content-Type: application/json on
	//     POST/PUT/PATCH so mutating endpoints never receive non-JSON bodies
	//     without an explicit client mistake being surfaced as a 415 early in
	//     the chain, before any handler logic runs.
	//
	//     The route-aware wrapper is used here so that requests with the wrong
	//     HTTP method on an EXISTING path (405 case) receive 405 Method Not
	//     Allowed from chi rather than 415 Unsupported Media Type.  For
	//     completely unknown paths (404 case) the middleware still returns 415
	//     before chi's 404 handler runs, preserving the existing behaviour.
	r.Use(routeAwareContentTypeMiddleware(r))

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
	if out.AppEnv == "" {
		// Default to development so tests get stack traces in panic responses
		// and so we never accidentally treat an unset env as production-safe.
		out.AppEnv = "development"
	}
	return out
}

func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin == "*" {
			allowAll = true
			continue
		}
		allowed[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowedOrigin := ""
			if allowAll {
				allowedOrigin = "*"
			} else if _, ok := allowed[origin]; ok {
				allowedOrigin = origin
				w.Header().Add("Vary", "Origin")
			}
			if allowedOrigin == "" {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Set("Access-Control-Allow-Origin", allowedOrigin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Expose-Headers", "X-Request-Id, X-Trace-Id")
			h.Set("Access-Control-Max-Age", "600")

			requestHeaders := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
			if requestHeaders == "" {
				requestHeaders = "Accept, Authorization, Content-Type, X-Admin-Reason, X-Request-Id, X-Trace-Id"
			}
			h.Set("Access-Control-Allow-Headers", requestHeaders)
			if requestHeaders != "" {
				h.Add("Vary", "Access-Control-Request-Headers")
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// -----------------------------------------------------------------------------
// Middleware implementations
// -----------------------------------------------------------------------------

// requestContext resolves the per-request identifier and surfaces it via:
//
//   - the X-Request-Id response header — the public contract that every
//     response carries a correlatable identifier the client can quote in
//     support requests (feature #61).
//   - the slog context via logging.WithRequestID — so any subsequent
//     logging.FromContext(ctx) returns a logger with a "request_id"
//     attribute attached automatically without per-call boilerplate.
//
// Resolution order (feature #61 steps 5–6):
//
//  1. If the incoming request carries an X-Request-Id header whose value is a
//     valid UUID (any version, RFC 4122 format), the client-supplied value is
//     preserved verbatim in the response header and in the slog context.
//  2. Otherwise (header absent, empty, or not a valid UUID) a fresh UUIDv7 is
//     minted so the response always carries a well-formed, sortable UUID.
//
// The reference to chimw.RequestID is intentionally dropped: chi's built-in
// RequestID generates non-UUID identifiers (e.g. "host/000001") that fail the
// "valid UUID" contract in step 2 of feature #61.
//
// Additionally, if the request carries an X-Correlation-Id header, its value
// is stored on the context via logging.WithCorrelationID so that every
// logging.FromContext(ctx) call automatically emits a "correlation_id"
// attribute — enabling end-to-end correlation across service boundaries.
func requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := resolveRequestID(r)
		w.Header().Set(HeaderRequestID, reqID)
		ctx := logging.WithRequestID(r.Context(), reqID)

		// Propagate correlation_id from the X-Correlation-Id request header into
		// the slog context. Any logging.FromContext(ctx) call downstream will then
		// automatically include "correlation_id" in every log record without
		// per-call boilerplate — supporting distributed trace correlation across
		// service-to-service HTTP calls (feature #63).
		if corrID := strings.TrimSpace(r.Header.Get("X-Correlation-Id")); corrID != "" {
			ctx = logging.WithCorrelationID(ctx, corrID)
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveRequestID returns the request identifier to use for this request.
// If the client sent a well-formed UUID via X-Request-Id it is preserved
// verbatim; otherwise a fresh UUIDv7 is minted.
func resolveRequestID(r *http.Request) string {
	if inbound := strings.TrimSpace(r.Header.Get(HeaderRequestID)); inbound != "" {
		if _, err := uuid.Parse(inbound); err == nil {
			return inbound
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		// Entropy exhaustion is effectively impossible; fall back to a hex
		// string so the non-empty X-Request-Id contract still holds.
		return newRandomHex(16)
	}
	return id.String()
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
//     "/v1/orders/{id}" rather than "/v1/orders/abc-123") so
//     cardinality is bounded by the route table, not by URL
//     parameter values. A request that produced a 404 (no match)
//     is labelled "unmatched" to preserve the same bound.
//   - status : decimal HTTP status string. If the handler returned without
//     calling WriteHeader, chi's WrapResponseWriter reports 0 —
//     we coerce that to "200" because Go's net/http documents an
//     implicit 200 in that case.
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
//     and via logging.WithTraceID(ctx, ...). The trace_id is resolved
//     with the following priority:
//     a) Live OTel SpanContext trace_id (when the sampler decides to sample).
//     b) trace-id field from the incoming W3C Traceparent header (honours
//     distributed traces even when the local SDK runs in disabled /
//     NeverSample mode — the span is a no-op but the parent's trace_id
//     still propagates through so end-to-end log correlation works).
//     c) Inbound X-Trace-Id header (lets callers pin a custom value).
//     d) Cryptographic random 128-bit hex string (guarantees non-empty
//     contract on every response regardless of SDK mode).
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

			// 3. Resolve trace_id with the priority order documented above.
			traceID := ""
			if sc := span.SpanContext(); sc.TraceID().IsValid() {
				// (a) Sampled local span — use the real OTel trace_id.
				traceID = sc.TraceID().String()
			}
			if traceID == "" {
				// (b) Disabled SDK / NeverSample: the local span is a no-op so
				// sc.TraceID() is zeroed, but the incoming W3C traceparent header
				// still carries the distributed trace_id. Parse it directly so
				// the trace propagates end-to-end even without local sampling.
				// W3C traceparent format: {version(2)}-{trace-id(32)}-{parent-id(16)}-{flags(2)}
				if tp := strings.TrimSpace(r.Header.Get("Traceparent")); tp != "" {
					if parts := strings.SplitN(tp, "-", 4); len(parts) == 4 && len(parts[1]) == 32 {
						traceID = parts[1]
					}
				}
			}
			if traceID == "" {
				// (c) Caller-pinned value via X-Trace-Id request header.
				traceID = strings.TrimSpace(r.Header.Get(HeaderTraceID))
			}
			if traceID == "" {
				// (d) Random fallback — guarantees non-empty contract.
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

// JSONBodyLimit returns a middleware that enforces a maximum request body
// size for POST/PUT/PATCH requests so an oversized payload cannot exhaust
// process memory before chi.Timeout fires. Safe methods are passed through
// untouched.
//
// Two complementary checks are applied:
//
//  1. Fast path — if the client sends a Content-Length header whose value
//     already exceeds maxBytes the request is rejected immediately with
//     HTTP 413 and the standard JSON error envelope (code
//     'http.payload_too_large') before any body bytes are read. This makes
//     rejection nearly instantaneous regardless of the body size.
//
//  2. Slow path — when Content-Length is absent or -1 (chunked / unknown
//     body size) r.Body is wrapped with http.MaxBytesReader. A handler
//     that reads past maxBytes receives a *http.MaxBytesError; callers are
//     expected to map this to a 413 response.
//
// A slog WARN record is emitted for every rejected request, carrying
// "content_length" and "limit" fields for dashboards and alerting rules.
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
					// Fast path: reject immediately when Content-Length already
					// exceeds the limit. net/http sets r.ContentLength from the
					// Content-Length header; a value of -1 means the header was
					// absent or the transfer is chunked.
					if r.ContentLength > maxBytes {
						logger := logging.FromContext(r.Context())
						logger.Warn("request body exceeds limit",
							"content_length", r.ContentLength,
							"limit", maxBytes,
						)
						requestID := logging.RequestID(r.Context())
						traceID := logging.TraceID(r.Context())
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
						w.WriteHeader(http.StatusRequestEntityTooLarge)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"error": map[string]any{
								"code":       "http.payload_too_large",
								"message":    fmt.Sprintf("request body exceeds the %d-byte limit", maxBytes),
								"request_id": requestID,
								"trace_id":   traceID,
							},
						})
						return
					}
					// Slow path: wrap body so any attempt to read past maxBytes
					// returns a *http.MaxBytesError rather than blocking forever.
					r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// -----------------------------------------------------------------------------
// Panic recovery middleware
// -----------------------------------------------------------------------------

// panicRecoverer returns a middleware that catches any panic from a downstream
// handler and converts it into an HTTP 500 response with the project-standard
// JSON error envelope.
//
// Behaviour:
//   - Captures the full goroutine stack via runtime/debug.Stack().
//   - Logs an ERROR record via slog carrying the panic value ("panic" field)
//     and the raw stack string ("stack" field) plus request_id and trace_id
//     read from the response headers set by the upstream requestContext and
//     tracerMiddleware.
//   - Increments the arena_http_panics_total Prometheus counter when m is
//     non-nil.
//   - Returns HTTP 500 with {"error":{"code":"internal.unexpected",...}}.
//   - In "development" mode the response also contains a "stack" field so
//     engineers can see the backtrace directly in the HTTP client without
//     tailing server logs.  In "production" / "staging" the stack is omitted
//     to prevent leaking internal source paths and variable values to clients.
//
// panicRecoverer must be the OUTERMOST middleware (position 1) so it covers
// panics in every downstream middleware and handler.  It supersedes
// chimw.Recoverer.
func panicRecoverer(base *slog.Logger, m *observability.Metrics, appEnv string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rvr := recover()
				if rvr == nil {
					return // no panic; nothing to do
				}

				// Capture the stack trace immediately — runtime.Stack fills
				// a buffer with the goroutine backtraces; debug.Stack is a
				// convenience wrapper that allocates one for us.
				stack := string(debug.Stack())

				// Read request identifiers from response headers. The inner
				// requestContext middleware (position 4) and tracerMiddleware
				// (position 7) both set these on the original http.ResponseWriter
				// before calling next, so they are visible to the outer defer
				// even though the enriched request values passed downstream are
				// local to each middleware closure.
				requestID := w.Header().Get(HeaderRequestID)
				traceID := w.Header().Get(HeaderTraceID)

				// Log the panic at ERROR level so alerting rules can page on
				// rising error_count where msg="http panic recovered".
				logger := base
				if logger == nil {
					logger = slog.Default()
				}
				logger.Error("http panic recovered",
					"panic", fmt.Sprintf("%v", rvr),
					"stack", stack,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", requestID,
					"trace_id", traceID,
				)

				// Increment the Prometheus panic counter so dashboards and
				// alerts can fire on any non-zero panic rate.
				if m != nil {
					m.HTTPPanicsTotal.Inc()
				}

				// Build the error response body.
				errFields := map[string]any{
					"code":       "internal.unexpected",
					"message":    "an unexpected error occurred",
					"request_id": requestID,
					"trace_id":   traceID,
				}
				// In development mode include the stack trace in the response
				// so engineers can debug without tailing server logs. In all
				// other environments (staging, production) omit the stack to
				// avoid leaking internal paths and values to clients.
				if appEnv == "development" {
					errFields["stack"] = stack
				}

				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": errFields})
			}()

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

// requestLogMiddleware emits structured slog records for every request:
//
//   - "http.request.started"   — emitted before the handler runs; includes
//     method, path, remote_addr, and masked credentials.
//   - "http.request.completed" — emitted after the handler returns; includes
//     all fields required by feature #63: request_id, correlation_id, route,
//     method, status, latency_ms, bytes_in, bytes_out, user_agent.
//
// Sensitive headers are masked via MaskSensitiveHeader before reaching the log
// output so raw bearer tokens and session cookies never appear in log files.
//
// The middleware is positioned AFTER tracerMiddleware in the canonical chain so
// that logging.FromContext(r.Context()) returns a logger already enriched with
// request_id (set by requestContext), correlation_id (set by requestContext from
// X-Correlation-Id header), and trace_id (set by tracerMiddleware) — every log
// record produced here is therefore automatically correlated to the right
// distributed trace without per-call boilerplate.
//
// The Authorization and Cookie headers are always included in "request start"
// when they are present, masked per MaskSensitiveHeader. This gives operators
// visibility into which requests carried credentials (and which scheme) without
// exposing the credential itself.
func requestLogMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// logging.FromContext inherits request_id, correlation_id, and trace_id
			// from ctx so all identifiers appear on every record automatically.
			logger := logging.FromContext(r.Context())
			if logger == nil {
				logger = base
			}
			if logger == nil {
				logger = slog.Default()
			}

			start := time.Now()

			// Capture bytes_in from the declared Content-Length header.
			// A value of -1 means the header was absent or the transfer is
			// chunked; we report 0 in that case to keep the field numeric.
			bytesIn := r.ContentLength
			if bytesIn < 0 {
				bytesIn = 0
			}

			// Build "request started" log args. Always include method, path, and
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

			// Resolve the matched chi route pattern after the handler ran.
			// chi.RouteContext(r.Context()).RoutePattern() returns the low-
			// cardinality pattern (e.g. "/v1/orders/{id}") rather than the
			// concrete URL path so the log field is safe for aggregation/alerting.
			// Returns "" for unmatched requests (404s); we label those "unmatched".
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			// Emit the single completion log record (feature #63). Fields are
			// ordered from most-used (method, route, status) to least-used (sizes,
			// user agent) so log tail output is readable without wide terminals.
			// request_id, correlation_id, and trace_id are prepended automatically
			// by logging.FromContext via the enriched logger returned above.
			logger.Info("http.request.completed",
				"method", r.Method,
				"route", route,
				"status", status,
				"latency_ms", float64(time.Since(start).Microseconds())/1000.0,
				"bytes_in", bytesIn,
				"bytes_out", ww.BytesWritten(),
				"user_agent", r.Header.Get("User-Agent"),
			)
		})
	}
}

// -----------------------------------------------------------------------------
// Content-Type enforcement
// -----------------------------------------------------------------------------

// RequireJSONContentType is a middleware that enforces Content-Type:
// application/json on mutating HTTP methods (POST, PUT, PATCH). Requests
// using any other content type — or with a missing Content-Type header —
// receive a 415 Unsupported Media Type response with the project-standard
// JSON error envelope and an Accept-Post: application/json header that
// tells the client which media type to use.
//
// The check is case-insensitive and ignores media-type parameters such as
// charset, so both "application/json" and "application/json; charset=utf-8"
// and "application/JSON" all satisfy the constraint.
//
// GET, HEAD, DELETE, and other safe or idempotent methods are passed through
// without any Content-Type inspection because those methods conventionally
// carry no body.
//
// Exported (capital R) so callers that build partial routers outside NewRouter
// — integration tests, future arena-admin binaries — can apply the same
// enforcement semantics without duplicating the logic.
func RequireJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			// Extract the media type from the Content-Type header, ignoring
			// any parameters (e.g. "; charset=utf-8"). The comparison is
			// case-insensitive per RFC 7231 §3.1.1.1.
			ct := r.Header.Get("Content-Type")
			mediaType := strings.ToLower(strings.TrimSpace(ct))
			if idx := strings.IndexByte(mediaType, ';'); idx >= 0 {
				mediaType = strings.TrimSpace(mediaType[:idx])
			}
			if mediaType != "application/json" && mediaType != "multipart/form-data" {
				// The Accept-Post response header (RFC 7240 / draft-wilde-accept-post)
				// tells the client which media types are accepted for POST.
				w.Header().Set("Accept-Post", "application/json")
				// Use the resolved request_id from context (set by requestContext
				// middleware upstream). Fall back to the inbound header in the
				// unlikely case that requestContext wasn't in the chain.
				requestID := logging.RequestID(r.Context())
				if requestID == "" {
					requestID = strings.TrimSpace(r.Header.Get(HeaderRequestID))
				}
				traceID := strings.TrimSpace(r.Header.Get(HeaderTraceID))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnsupportedMediaType)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":       "http.unsupported_media_type",
						"message":    "Content-Type must be application/json",
						"request_id": requestID,
						"trace_id":   traceID,
					},
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// routeAwareContentTypeMiddleware wraps RequireJSONContentType with route
// awareness so that the 405 Method Not Allowed case is handled correctly.
//
// When a PUT/PATCH/POST request arrives without the correct Content-Type:
//   - If the URL path is registered in the router but the HTTP method is not
//     (i.e. chi would respond with 405), the middleware passes the request
//     through so chi's MethodNotAllowed handler can run and return 405.
//   - If the URL path is not registered at all (i.e. chi would respond with
//     404), the middleware still returns 415 before routing — preserving the
//     existing "415 before 404" behaviour tested by content_type_test.go.
//   - If the method IS registered (a handler would run), the middleware
//     returns 415 as before — the handler must not receive a non-JSON body.
//
// The router argument is captured by closure; since routes are mounted after
// NewRouter returns but before any request is served, Match() sees the full
// route tree at request time.
func routeAwareContentTypeMiddleware(router chi.Router) func(http.Handler) http.Handler {
	// methodsToCheck is the ordered set of HTTP methods used to detect whether
	// a path is registered under *any* method (to distinguish 404 from 405).
	methodsToCheck := []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				// Skip content-type enforcement for bodyless requests (e.g. action
				// endpoints like /issue, /pay, /void that transition state without
				// a request body). ContentLength == 0 means the client sent no body.
				if r.ContentLength == 0 {
					break
				}
				ct := r.Header.Get("Content-Type")
				mediaType := strings.ToLower(strings.TrimSpace(ct))
				if idx := strings.IndexByte(mediaType, ';'); idx >= 0 {
					mediaType = strings.TrimSpace(mediaType[:idx])
				}
				if mediaType != "application/json" && mediaType != "multipart/form-data" {
					// Check if this exact method+path is registered.
					rctx := chi.NewRouteContext()
					if !router.Match(rctx, r.Method, r.URL.Path) {
						// Method not registered for this path. Check whether the path
						// itself exists under any other method (405 vs 404 detection).
						for _, m := range methodsToCheck {
							if m == r.Method {
								continue
							}
							rctx2 := chi.NewRouteContext()
							if router.Match(rctx2, m, r.URL.Path) {
								// Path is known; wrong method → let chi return 405.
								next.ServeHTTP(w, r)
								return
							}
						}
						// Path is not registered at all → fall through to 415.
					}
					// Either the method IS registered (handler would run) or the path
					// doesn't exist entirely (404 case): enforce Content-Type → 415.
					w.Header().Set("Accept-Post", "application/json")
					requestID := logging.RequestID(r.Context())
					if requestID == "" {
						requestID = strings.TrimSpace(r.Header.Get(HeaderRequestID))
					}
					traceID := strings.TrimSpace(r.Header.Get(HeaderTraceID))
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusUnsupportedMediaType)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":       "http.unsupported_media_type",
							"message":    "Content-Type must be application/json",
							"request_id": requestID,
							"trace_id":   traceID,
						},
					})
					return
				}
			}
			next.ServeHTTP(w, r)
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
			b[i] = byte(t >> (i * 8)) //nolint:gosec // intentional low-byte truncation
		}
	}
	return hex.EncodeToString(b)
}

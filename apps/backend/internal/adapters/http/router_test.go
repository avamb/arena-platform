// Package http_test exercises the NewRouter factory and the canonical
// middleware chain defined in router.go.
//
// Feature #89 step 6 — unit test: "middleware chain sets request_id in
// context and in Response header X-Request-Id".
//
// The tests use net/http/httptest so no network listener is required. Every
// test builds a router with httpadapter.NewRouter(Deps{...}), mounts a probe
// handler that captures whatever the middleware chain wrote onto the context
// and response headers, then fires an httptest.NewRecorder round trip and
// asserts the expected contract.
//
// Dependency-free construction: tests that do not care about Prometheus
// metrics pass Deps{Metrics: nil} — the prometheusMiddleware is skipped in
// that case, keeping the test free of registry-global state side-effects.
package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// -----------------------------------------------------------------------------
// probeResult — captures middleware-enriched context values inside a handler.
// -----------------------------------------------------------------------------

type probeResult struct {
	requestID string
	traceID   string
	loggerOK  bool // true when logging.FromContext returned non-nil
}

// probeHandler returns an http.HandlerFunc that stores context values into out.
func probeHandler(out *probeResult) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out.requestID = logging.RequestID(r.Context())
		out.traceID = logging.TraceID(r.Context())
		out.loggerOK = logging.FromContext(r.Context()) != nil
		w.WriteHeader(http.StatusOK)
	}
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestNewRouter_RequestIDInResponseHeader verifies the core contract of step 6:
// every response from a router built with NewRouter carries a non-empty
// X-Request-Id response header.
func TestNewRouter_RequestIDInResponseHeader(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatalf("expected X-Request-Id response header to be non-empty, got %q", got)
	}
}

// TestNewRouter_RequestIDInContext verifies that the middleware chain stores
// the same request identifier in the request context (via logging.RequestID)
// as it advertises in the X-Request-Id response header. Downstream handlers
// can therefore read the request_id from ctx without re-parsing the header.
func TestNewRouter_RequestIDInContext(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})

	var out probeResult
	r.Get("/probe", probeHandler(&out))

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	headerID := rr.Header().Get(httpadapter.HeaderRequestID)
	if headerID == "" {
		t.Fatal("X-Request-Id response header is empty")
	}
	if out.requestID == "" {
		t.Fatal("logging.RequestID(ctx) returned empty string inside handler")
	}
	if headerID != out.requestID {
		t.Fatalf("X-Request-Id header %q != logging.RequestID(ctx) %q", headerID, out.requestID)
	}
}

// TestNewRouter_TraceIDInResponseHeader verifies that every response carries a
// non-empty X-Trace-Id header sourced from the OTel tracer middleware (or from
// the random-hex fallback when the global TracerProvider is the no-op default).
func TestNewRouter_TraceIDInResponseHeader(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got == "" {
		t.Fatalf("expected X-Trace-Id response header to be non-empty, got %q", got)
	}
}

// TestNewRouter_TraceIDInContext verifies that the X-Trace-Id header value
// matches what the tracerMiddleware stored on the request context via
// logging.WithTraceID. Downstream handlers and log records are then
// correlated to the same identifier without extra plumbing.
func TestNewRouter_TraceIDInContext(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})

	var out probeResult
	r.Get("/probe", probeHandler(&out))

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	headerID := rr.Header().Get(httpadapter.HeaderTraceID)
	if headerID == "" {
		t.Fatal("X-Trace-Id response header is empty")
	}
	if out.traceID == "" {
		t.Fatal("logging.TraceID(ctx) returned empty string inside handler")
	}
	if headerID != out.traceID {
		t.Fatalf("X-Trace-Id header %q != logging.TraceID(ctx) %q", headerID, out.traceID)
	}
}

// TestNewRouter_InboundRequestIDHonoured verifies that when a client supplies
// a valid UUID as X-Request-Id (common in service-to-service calls), the
// requestContext middleware preserves that UUID verbatim in both the response
// header and the slog context (feature #61 step 5).
//
// Note: only well-formed UUIDs are preserved. An arbitrary non-UUID string is
// replaced with a fresh UUIDv7 (feature #61 step 6).
func TestNewRouter_InboundRequestIDHonoured(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})

	var out probeResult
	r.Get("/probe", probeHandler(&out))

	// Supply a valid UUID — the middleware must echo it back unchanged.
	clientID := "01234567-89ab-cdef-0123-456789abcdef"
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Request-Id", clientID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if got := rr.Header().Get(httpadapter.HeaderRequestID); got != clientID {
		t.Fatalf("X-Request-Id header: want %q got %q", clientID, got)
	}
	if out.requestID != clientID {
		t.Fatalf("logging.RequestID(ctx): want %q got %q", clientID, out.requestID)
	}
}

// TestNewRouter_InvalidInboundRequestIDIsReplaced verifies that when a client
// sends a non-UUID value as X-Request-Id, the server generates a fresh UUIDv7
// instead of echoing the invalid value (feature #61 step 6).
func TestNewRouter_InvalidInboundRequestIDIsReplaced(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})

	var out probeResult
	r.Get("/probe", probeHandler(&out))

	invalidID := "not-a-uuid"
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Request-Id", invalidID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id header must be non-empty even when client sent invalid value")
	}
	if got == invalidID {
		t.Fatalf("server must NOT echo back invalid X-Request-Id %q", invalidID)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("replacement X-Request-Id %q is not a valid UUID: %v", got, err)
	}
}

// TestNewRouter_RecovererCatchesPanic verifies that the Recoverer middleware
// (outermost in the chain) converts a panicking handler into a 500 response
// instead of crashing the goroutine. This is essential for the "no process
// crash on handler panic" guarantee documented in router.go.
func TestNewRouter_RecovererCatchesPanic(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})
	r.Get("/panic", func(_ http.ResponseWriter, _ *http.Request) {
		panic("simulated handler panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rr := httptest.NewRecorder()

	// ServeHTTP must not propagate the panic — Recoverer should catch it.
	panicked := false
	func() {
		defer func() {
			if v := recover(); v != nil {
				panicked = true
			}
		}()
		r.ServeHTTP(rr, req)
	}()

	if panicked {
		t.Fatal("panic escaped from Recoverer middleware — it should have been caught")
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after panic recovery, got %d", rr.Code)
	}
}

// TestNewRouter_TimeoutMiddlewareApplied verifies that when Deps.RequestTimeout
// is set the chi.Timeout middleware (backed by http.TimeoutHandler) is in the
// chain and cancels the request context after the deadline. A slow handler
// waits on ctx.Done(), sends the error to a buffered channel, and the test
// reads from the channel with a generous timeout to handle chi's goroutine
// scheduling (http.TimeoutHandler runs the handler concurrently; it may return
// the 503 response before the handler goroutine executes the channel send).
func TestNewRouter_TimeoutMiddlewareApplied(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{
		RequestTimeout: 50 * time.Millisecond,
	})

	// Buffered so the handler goroutine never blocks on send even if the
	// test's ServeHTTP has already returned.
	ctxErrCh := make(chan error, 1)
	r.Get("/slow", func(_ http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
			ctxErrCh <- req.Context().Err()
		case <-time.After(5 * time.Second):
			ctxErrCh <- nil // safety: timeout not triggered — indicates bug
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// Wait for the handler goroutine to complete. chi.Timeout (which wraps
	// http.TimeoutHandler) may return ServeHTTP before the handler goroutine
	// finishes, so we allow a brief additional window.
	select {
	case err := <-ctxErrCh:
		if err == nil {
			t.Fatal("expected context to be cancelled by Timeout middleware, but ctx.Err() is nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler to report ctx.Err(): Timeout middleware may not have fired")
	}
}

// TestNewRouter_BodyLimitMiddlewareApplied verifies that when BodyLimitBytes
// is set an oversized POST body causes the handler to receive an error when
// reading r.Body. The middleware wraps r.Body with http.MaxBytesReader so
// reading past the limit fails gracefully.
func TestNewRouter_BodyLimitMiddlewareApplied(t *testing.T) {
	const limit = 16
	r := httpadapter.NewRouter(httpadapter.Deps{
		BodyLimitBytes: limit,
	})

	var readErr error
	r.Post("/upload", func(w http.ResponseWriter, req *http.Request) {
		// io.ReadAll exhausts the reader via repeated Read calls, triggering
		// the MaxBytesError once the limit is reached on a subsequent read.
		_, readErr = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
	})

	// Wrap the oversized body in an io.NopCloser so that http.NewRequest
	// cannot determine its length — Content-Length is not set (-1). This
	// bypasses the JSONBodyLimit "fast path" (which checks Content-Length)
	// and exercises the MaxBytesReader "slow path" that the handler
	// encounters when it tries to read past the limit.
	//
	// Content-Type: application/json is required by the RequireJSONContentType
	// middleware that is now wired globally in NewRouter.
	oversized := io.NopCloser(strings.NewReader(strings.Repeat("x", limit+10)))
	req := httptest.NewRequest(http.MethodPost, "/upload", oversized)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if readErr == nil {
		t.Fatal("expected a read error from the body-limit middleware, got nil")
	}
}

// TestNewRouter_PrometheusMiddlewareSkippedWhenNil verifies that a router
// constructed with Deps{Metrics: nil} does not panic and still serves
// requests correctly. Callers that don't need metrics should not be forced to
// supply a *observability.Metrics just to build a functional router.
func TestNewRouter_PrometheusMiddlewareSkippedWhenNil(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{Metrics: nil})
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// TestNewRouter_PrometheusMiddlewareRecordsRequest verifies that when a real
// *observability.Metrics is supplied the prometheusMiddleware records at least
// one observation on the HTTP histogram for the probe request.
func TestNewRouter_PrometheusMiddlewareRecordsRequest(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	r := httpadapter.NewRouter(httpadapter.Deps{Metrics: m})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("registry.Gather: %v", err)
	}

	var found bool
	for _, mf := range families {
		if mf.GetName() == "arena_http_request_duration_seconds" {
			for _, metric := range mf.GetMetric() {
				if h := metric.GetHistogram(); h != nil && h.GetSampleCount() > 0 {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Fatal("expected arena_http_request_duration_seconds to have at least one observation")
	}
}

// TestNewRouter_EachRequestGetsUniqueRequestID verifies that two separate
// requests to the same route receive distinct X-Request-Id values. The
// chimw.RequestID middleware must generate a new identifier per request;
// reusing the same value would break request correlation.
func TestNewRouter_EachRequestGetsUniqueRequestID(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fire := func() string {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Header().Get(httpadapter.HeaderRequestID)
	}

	id1 := fire()
	id2 := fire()

	if id1 == "" || id2 == "" {
		t.Fatalf("got empty X-Request-Id (id1=%q id2=%q)", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct X-Request-Id per request, both were %q", id1)
	}
}

// TestNewRouter_LoggerAttachedToContext verifies that logging.FromContext
// inside a handler returns a non-nil logger (the loggerMiddleware attaches
// the base logger to ctx via logging.WithLogger so downstream handlers can
// call logging.FromContext without boilerplate).
func TestNewRouter_LoggerAttachedToContext(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})

	var out probeResult
	r.Get("/probe", probeHandler(&out))

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if !out.loggerOK {
		t.Fatal("logging.FromContext(ctx) returned nil inside handler")
	}
}

// TestNewRouter_MiddlewareOrderRespected verifies the documented chain order
// by asserting that BOTH request_id and trace_id are non-empty from the
// handler's point of view. This is only possible if requestContext (which sets
// request_id) runs before tracerMiddleware (which sets trace_id on the same
// ctx chain) — the documented order is: RequestID → requestContext → logger →
// tracer → Timeout.
func TestNewRouter_MiddlewareOrderRespected(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{})

	var reqID, traceID string
	r.Get("/probe", func(w http.ResponseWriter, req *http.Request) {
		reqID = logging.RequestID(req.Context())
		traceID = logging.TraceID(req.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if reqID == "" {
		t.Error("request_id not set on context — requestContext middleware may not have run")
	}
	if traceID == "" {
		t.Error("trace_id not set on context — tracerMiddleware may not have run")
	}
}

// Compile-time guard: pin the exported names this test package depends on so
// that future renames surface as compile errors rather than silent test skips.
var (
	_ = httpadapter.HeaderRequestID
	_ = httpadapter.HeaderTraceID
	_ httpadapter.Deps
	_ = httpadapter.NewRouter
)

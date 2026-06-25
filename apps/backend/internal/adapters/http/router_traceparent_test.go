// router_traceparent_test.go verifies feature #62, step 6:
// "Send incoming traceparent header (W3C) — server continues the trace
// (same trace_id) not start fresh."
//
// The tests exercise the tracerMiddleware directly via httpadapter.NewRouter
// with an explicit W3C TraceContext propagator, avoiding any dependency on
// the global OTel state that process-wide InitTracer would set.
//
// Two scenarios are covered:
//
//	A. AlwaysSample SDK — the local span is live, sc.TraceID() is valid,
//	   and the trace_id must equal the one carried by the incoming traceparent.
//
//	B. NeverSample SDK — the local span is a no-op, sc.TraceID() is zeroed,
//	   but the remote SpanContext extracted by the propagator still holds the
//	   parent trace_id. The middleware must surface THAT trace_id (priority (b)
//	   in tracerMiddleware) rather than minting a fresh random one.
//
// Both scenarios confirm that the client's distributed trace context propagates
// through the server boundary transparently.
package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// knownTraceparent is a syntactically valid W3C traceparent header whose
// trace-id, parent-id, and sampled flag are all known in advance. The
// trace-id portion ("4bf92f3577b34da6a3ce929d0e0e4736") is used to verify
// that the server continues the distributed trace rather than generating a
// fresh identifier.
//
// Format: "00-{trace-id(32hex)}-{parent-id(16hex)}-{flags(2hex)}"
//   - version=00 (W3C TraceContext v1)
//   - trace-id=4bf92f3577b34da6a3ce929d0e0e4736
//   - parent-id=00f067aa0ba902b7 (the client's span)
//   - flags=01 (sampled)
const (
	knownTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	knownTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"

	// unsampled version of the same trace — sampled flag=00
	unsampledTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
)

// w3cPropagator returns a pure W3C TraceContext propagator that extracts and
// injects traceparent / tracestate headers. Used in all tests in this file so
// they don't depend on the global OTel propagator state.
func w3cPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
	)
}

// =============================================================================
// Scenario A — AlwaysSample SDK: live OTel span, trace_id from SpanContext
// =============================================================================

// TestTraceparent_AlwaysSample_TraceIDMatchesParent verifies that when the
// server is running with AlwaysSample and receives a W3C traceparent header,
// the X-Trace-Id response header carries the SAME 32-hex trace_id as the
// incoming traceparent (not a randomly generated one).
func TestTraceparent_AlwaysSample_TraceIDMatchesParent(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Traceparent", knownTraceparent)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got != knownTraceID {
		t.Errorf("X-Trace-Id: want %q (from traceparent), got %q", knownTraceID, got)
	}
}

// TestTraceparent_AlwaysSample_ContextCarriesParentTraceID verifies that the
// trace_id stored on the request context (via logging.WithTraceID) also equals
// the incoming traceparent's trace_id — so every slog record emitted by a
// handler is correlated to the distributed trace.
func TestTraceparent_AlwaysSample_ContextCarriesParentTraceID(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})

	var ctxTraceID string
	r.Get("/probe", func(w http.ResponseWriter, req *http.Request) {
		ctxTraceID = logging.TraceID(req.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Traceparent", knownTraceparent)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if ctxTraceID != knownTraceID {
		t.Errorf("logging.TraceID(ctx): want %q, got %q", knownTraceID, ctxTraceID)
	}
}

// TestTraceparent_AlwaysSample_NoTraceparent_GeneratesNew verifies that when
// no traceparent header is supplied, the server generates its own fresh
// trace_id (does NOT produce an empty string or the zero-value).
func TestTraceparent_AlwaysSample_NoTraceparent_GeneratesNew(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	// No Traceparent header.
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got == "" {
		t.Fatal("X-Trace-Id must be non-empty when no traceparent is supplied")
	}
	if got == knownTraceID {
		t.Errorf("X-Trace-Id must be a fresh value, not the known test trace_id %q", knownTraceID)
	}
}

// =============================================================================
// Scenario B — NeverSample SDK: remote SpanContext fallback (code-change path)
// =============================================================================

// TestTraceparent_NeverSample_TraceIDHonoured verifies the key behaviour
// introduced by the tracerMiddleware code change (priority (b)):
//
// Even when the local OTel SDK uses NeverSample (no-op spans, disabled mode),
// an incoming W3C traceparent with sampled=01 must cause the server to surface
// the PARENT trace_id, not a fresh random one.
//
// Before the fix, the middleware skipped past the zeroed SpanContext and fell
// through to the X-Trace-Id header / random fallback. Now it reads the remote
// SpanContext from the propagated context and uses its TraceID.
func TestTraceparent_NeverSample_TraceIDHonoured(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.NeverSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Traceparent", knownTraceparent) // sampled=01
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got != knownTraceID {
		t.Errorf("X-Trace-Id: want %q (from traceparent, NeverSample mode), got %q",
			knownTraceID, got)
	}
}

// TestTraceparent_NeverSample_UnsampledHonoured verifies that even when the
// incoming traceparent uses sampled=00 (the remote service chose not to sample
// the span), the server still surfaces the trace_id rather than a new random
// one. This is important for trace correlation in high-volume environments where
// head-based sampling drops most traces but operators still want to correlate
// logs by trace_id.
func TestTraceparent_NeverSample_UnsampledHonoured(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.NeverSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Traceparent", unsampledTraceparent) // sampled=00
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got != knownTraceID {
		t.Errorf("X-Trace-Id: want %q (from unsampled traceparent), got %q",
			knownTraceID, got)
	}
}

// TestTraceparent_NeverSample_ContextAlsoCarriesParentTraceID verifies that the
// trace_id in the request context (via logging.TraceID) also equals the
// incoming traceparent's trace_id when in NeverSample mode, so handler log
// records are correlated to the right distributed trace.
func TestTraceparent_NeverSample_ContextAlsoCarriesParentTraceID(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.NeverSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})

	var ctxTraceID string
	r.Get("/probe", func(w http.ResponseWriter, req *http.Request) {
		ctxTraceID = logging.TraceID(req.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Traceparent", knownTraceparent)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if ctxTraceID != knownTraceID {
		t.Errorf("logging.TraceID(ctx) in NeverSample mode: want %q, got %q",
			knownTraceID, ctxTraceID)
	}
}

// TestTraceparent_NeverSample_NoTraceparent_GeneratesRandom verifies that when
// no traceparent is supplied AND sampling is off, the server still produces a
// non-empty trace_id (the random fallback path — priority (d)).
func TestTraceparent_NeverSample_NoTraceparent_GeneratesRandom(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.NeverSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got == "" {
		t.Fatal("X-Trace-Id must be non-empty even in NeverSample mode with no traceparent")
	}
}

// TestTraceparent_InvalidTraceparent_GeneratesNew verifies that a malformed
// traceparent header (wrong version, wrong length, etc.) is silently ignored
// and the server generates a fresh trace_id rather than propagating garbage.
func TestTraceparent_InvalidTraceparent_GeneratesNew(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Traceparent", "not-a-valid-traceparent")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderTraceID)
	if got == "" {
		t.Fatal("X-Trace-Id must be non-empty even when traceparent is malformed")
	}
	// Must not equal the known test value since the malformed header was ignored.
	if got == knownTraceID {
		t.Errorf("invalid traceparent should have been ignored; got known test trace_id %q", got)
	}
}

// TestTraceparent_DifferentRequests_DifferentTraceIDs verifies that two
// requests without a traceparent header receive distinct trace_ids. The server
// must not reuse trace_ids across requests — each fresh request starts a new
// trace.
func TestTraceparent_DifferentRequests_DifferentTraceIDs(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	r := httpadapter.NewRouter(httpadapter.Deps{
		Propagator: w3cPropagator(),
		Tracer:     tp.Tracer(httpadapter.TracerName),
	})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fire := func() string {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Header().Get(httpadapter.HeaderTraceID)
	}

	id1 := fire()
	id2 := fire()

	if id1 == "" || id2 == "" {
		t.Fatalf("both requests must produce non-empty trace_ids (id1=%q id2=%q)", id1, id2)
	}
	if id1 == id2 {
		t.Errorf("two fresh requests must get distinct trace_ids; both got %q", id1)
	}
}

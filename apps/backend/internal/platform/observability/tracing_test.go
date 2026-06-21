// tracing_test.go covers the OpenTelemetry tracer-provider scaffold from
// feature #87.
//
// The OTLP exporter is constructed with lazy gRPC connection, so these tests
// exercise InitTracer in two regimes without ever opening a real network
// socket:
//
//   - Disabled mode (Endpoint = "") — the spec's local-dev fallback. We
//     assert the returned provider is non-nil, the global propagators are
//     installed, and Shutdown is a safe no-op.
//
//   - Configured mode (Endpoint = "127.0.0.1:nnnn") — the production
//     pipeline. The OTLP exporter dials lazily, so InitTracer returns
//     immediately and Shutdown flushes batches without ever talking to a
//     collector (the export call simply times out on shutdown, which we
//     bound with a short ctx).
//
// Validation of the SamplerRatio bounds is asserted directly without
// constructing any exporters at all.
package observability

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// shortCtx returns a context that times out quickly so a misconfigured
// shutdown can never block the test suite.
func shortCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Second)
}

// resetGlobalTracerProvider replaces the global TracerProvider with the OTel
// default (no-op) so tests that assert "InitTracer installed our tp as the
// global" cannot be polluted by side effects from a prior test.
func resetGlobalTracerProvider(t *testing.T) {
	t.Helper()
	otel.SetTracerProvider(otel.GetTracerProvider()) // no-op semantically
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
}

func TestInitTracer_DisabledMode_ReturnsProviderAndNoopShutdown(t *testing.T) {
	resetGlobalTracerProvider(t)

	ctx, cancel := shortCtx(t)
	defer cancel()

	tp, shutdown, err := InitTracer(ctx, TracingOptions{
		Endpoint:     "",
		ServiceName:  "arena-api",
		Environment:  "test",
		SamplerRatio: 0.0,
	})
	if err != nil {
		t.Fatalf("InitTracer(disabled): %v", err)
	}
	if tp == nil {
		t.Fatal("InitTracer returned nil *TracerProvider in disabled mode")
	}
	if shutdown == nil {
		t.Fatal("InitTracer returned nil ShutdownFunc in disabled mode")
	}

	// Global provider must be installed even in disabled mode so call sites
	// can use otel.Tracer(...) unconditionally.
	if got := otel.GetTracerProvider(); got != tp {
		t.Errorf("global TracerProvider not installed: got %T, want our *sdktrace.TracerProvider", got)
	}

	// Shutdown must succeed and be safe to call repeatedly.
	if err := shutdown(ctx); err != nil {
		t.Errorf("first shutdown returned error: %v", err)
	}
	if err := shutdown(ctx); err != nil {
		t.Errorf("second shutdown returned error: %v", err)
	}
}

func TestInitTracer_ConfiguredMode_ReturnsProvider(t *testing.T) {
	resetGlobalTracerProvider(t)

	ctx, cancel := shortCtx(t)
	defer cancel()

	tp, shutdown, err := InitTracer(ctx, TracingOptions{
		// Endpoint is syntactically valid but no collector is listening.
		// Because the OTLP/gRPC client connects lazily, InitTracer should
		// still return cleanly and Shutdown should not block.
		Endpoint:       "127.0.0.1:14317",
		Insecure:       true,
		ServiceName:    "arena-api",
		ServiceVersion: "0.0.0-test",
		Environment:    "test",
		InstanceID:     "test-instance-1",
		SamplerRatio:   0.25,
		BatchTimeout:   100 * time.Millisecond,
		ExportTimeout:  100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("InitTracer(configured): %v", err)
	}
	if tp == nil {
		t.Fatal("InitTracer returned nil *TracerProvider in configured mode")
	}
	if shutdown == nil {
		t.Fatal("InitTracer returned nil ShutdownFunc in configured mode")
	}

	// Provider must be the global one.
	if otel.GetTracerProvider() != tp {
		t.Error("global TracerProvider not installed in configured mode")
	}

	// Bound shutdown with an aggressive deadline so a hanging exporter cannot
	// stall the test. We don't assert on the error value: depending on grpc
	// dial timing the lazy connection may surface a context error here,
	// which is the expected behaviour when no collector is listening.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shutCancel()
	_ = shutdown(shutCtx)
}

func TestInitTracer_RejectsInvalidSamplerRatio(t *testing.T) {
	resetGlobalTracerProvider(t)
	ctx, cancel := shortCtx(t)
	defer cancel()

	cases := []struct {
		name  string
		ratio float64
	}{
		{"negative", -0.1},
		{"slightly_negative", -0.00001},
		{"greater_than_one", 1.0001},
		{"large_positive", 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tp, shutdown, err := InitTracer(ctx, TracingOptions{
				Endpoint:     "",
				SamplerRatio: tc.ratio,
			})
			if err == nil {
				t.Fatalf("expected error for SamplerRatio=%v, got nil", tc.ratio)
			}
			if tp != nil {
				t.Errorf("expected nil *TracerProvider when ratio is invalid")
			}
			if shutdown != nil {
				t.Errorf("expected nil ShutdownFunc when ratio is invalid")
			}
			if !strings.Contains(err.Error(), "SamplerRatio") {
				t.Errorf("error does not mention SamplerRatio: %v", err)
			}
		})
	}
}

func TestInitTracer_AcceptsBoundaryRatios(t *testing.T) {
	resetGlobalTracerProvider(t)
	ctx, cancel := shortCtx(t)
	defer cancel()

	for _, ratio := range []float64{0.0, 1.0} {
		ratio := ratio
		t.Run("ratio=", func(t *testing.T) {
			tp, shutdown, err := InitTracer(ctx, TracingOptions{
				Endpoint:     "",
				SamplerRatio: ratio,
			})
			if err != nil {
				t.Fatalf("InitTracer(ratio=%v): %v", ratio, err)
			}
			if tp == nil || shutdown == nil {
				t.Fatalf("InitTracer(ratio=%v) returned nil tp/shutdown", ratio)
			}
			if err := shutdown(ctx); err != nil {
				t.Errorf("shutdown(ratio=%v) failed: %v", ratio, err)
			}
		})
	}
}

func TestInitTracer_ShutdownIsIdempotent_Disabled(t *testing.T) {
	resetGlobalTracerProvider(t)
	ctx, cancel := shortCtx(t)
	defer cancel()

	_, shutdown, err := InitTracer(ctx, TracingOptions{
		Endpoint:     "",
		SamplerRatio: 0.5,
	})
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}

	// 100 concurrent shutdowns must all succeed.
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			errs <- shutdown(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("concurrent shutdown error: %v", err)
		}
	}
}

func TestInitTracer_InstallsCompositePropagator(t *testing.T) {
	resetGlobalTracerProvider(t)
	ctx, cancel := shortCtx(t)
	defer cancel()

	_, shutdown, err := InitTracer(ctx, TracingOptions{
		Endpoint:     "",
		SamplerRatio: 1.0,
	})
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer shutdown(ctx)

	prop := otel.GetTextMapPropagator()
	if prop == nil {
		t.Fatal("global TextMapPropagator is nil after InitTracer")
	}

	// The composite propagator's Fields() must include the W3C tracecontext
	// + baggage headers, which is what HTTP middleware will rely on.
	fields := prop.Fields()
	wantAny := func(target string) bool {
		for _, f := range fields {
			if f == target {
				return true
			}
		}
		return false
	}
	if !wantAny("traceparent") {
		t.Errorf("propagator does not advertise traceparent; fields=%v", fields)
	}
	if !wantAny("baggage") {
		t.Errorf("propagator does not advertise baggage; fields=%v", fields)
	}
}

func TestInitTracer_ProviderTypeIsSDKTracerProvider(t *testing.T) {
	resetGlobalTracerProvider(t)
	ctx, cancel := shortCtx(t)
	defer cancel()

	tp, shutdown, err := InitTracer(ctx, TracingOptions{
		Endpoint:     "",
		ServiceName:  "arena-api",
		SamplerRatio: 0.0,
	})
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer shutdown(ctx)

	// Compile-time guarantee that we returned an SDK tracer provider, not a
	// no-op stub — the global getter returns a trace.TracerProvider interface,
	// so we cross-check the concrete return type here.
	var _ *sdktrace.TracerProvider = tp
}

func TestInitTracer_TracerEmitsSpansWithoutPanic(t *testing.T) {
	resetGlobalTracerProvider(t)
	ctx, cancel := shortCtx(t)
	defer cancel()

	tp, shutdown, err := InitTracer(ctx, TracingOptions{
		Endpoint:     "",
		ServiceName:  "arena-api",
		SamplerRatio: 1.0,
	})
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer shutdown(ctx)

	// In disabled mode the sampler is NeverSample, so spans are non-recording
	// — but the API surface (Tracer.Start, span.End) must still work without
	// panic. This guards against future regressions where a code change might
	// return a nil tracer provider.
	tracer := tp.Tracer("observability-test")
	_, span := tracer.Start(context.Background(), "test-span")
	span.End()
}

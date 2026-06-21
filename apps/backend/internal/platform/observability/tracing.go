// tracing.go owns the OpenTelemetry tracing scaffold for arena_new.
//
// Responsibilities
//
//   - Construct a *sdktrace.TracerProvider configured with the OTLP/gRPC
//     exporter, a parent-based ratio sampler, and a resource that identifies
//     the running service (service.name, service.version, service.instance.id,
//     deployment.environment).
//
//   - Install the provider as the global tracer provider and register the
//     W3C TraceContext + Baggage propagators so HTTP middleware can extract
//     and inject trace headers transparently.
//
//   - Return a Shutdown function that flushes pending spans and closes the
//     exporter on graceful shutdown. The returned Shutdown is idempotent and
//     safe to call from multiple goroutines.
//
// Design notes
//
//   - If TracingOptions.Endpoint is empty, InitTracer treats tracing as
//     DISABLED and returns a *sdktrace.TracerProvider with a NeverSample
//     sampler and no exporter. The global provider and propagators are still
//     installed so call-site code (e.g. otel.Tracer(name).Start(...)) works
//     without nil checks. Shutdown is a no-op flush in that mode.
//
//   - The OTLP/gRPC client is constructed with lazy connection
//     (otlptracegrpc.WithDialOption(grpc.WithBlock()) is NOT used) so that
//     InitTracer never blocks on collector availability. A misconfigured or
//     temporarily unreachable collector therefore degrades to dropped spans
//     instead of preventing the API server from starting.
//
//   - The sampling ratio is clamped at the call site by config.Validate
//     (feature #83), but InitTracer also rejects out-of-range values
//     defensively so the package can be reused outside the standard config
//     pipeline (e.g. tests, future migration tooling).
package observability

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TracingOptions configures InitTracer. Field semantics:
//
//	Endpoint        OTLP/gRPC collector host:port (e.g. "otel-collector:4317").
//	                Empty disables tracing — InitTracer still returns a working
//	                TracerProvider, but it samples nothing and exports nothing.
//	Insecure        When true, the gRPC client dials in plaintext (no TLS).
//	                Use TLS in production; default to true for local dev.
//	ServiceName     Value of the service.name resource attribute. Falls back
//	                to "arena-service" if empty.
//	ServiceVersion  service.version resource attribute. Optional.
//	Environment     deployment.environment.name resource attribute. Optional.
//	InstanceID      service.instance.id resource attribute. Falls back to the
//	                process hostname (os.Hostname). Used by collectors to
//	                deduplicate replicas.
//	SamplerRatio    Probability in [0.0, 1.0] passed to ParentBased(
//	                TraceIDRatioBased(ratio)). 1.0 samples every root span;
//	                0.0 samples none. Values outside [0,1] return an error.
//	ExportTimeout   Per-batch export timeout. Defaults to 10s when zero.
//	BatchTimeout    Maximum delay before a non-full batch is exported.
//	                Defaults to 5s when zero.
type TracingOptions struct {
	Endpoint       string
	Insecure       bool
	ServiceName    string
	ServiceVersion string
	Environment    string
	InstanceID     string
	SamplerRatio   float64
	ExportTimeout  time.Duration
	BatchTimeout   time.Duration
}

// ShutdownFunc flushes pending spans and releases tracing resources. It is
// safe to call multiple times and from multiple goroutines: subsequent calls
// after the first return nil without re-running the shutdown sequence.
type ShutdownFunc func(ctx context.Context) error

// InitTracer constructs an OTel TracerProvider, installs it as the global
// provider together with the W3C TraceContext + Baggage propagators, and
// returns the provider plus a Shutdown function.
//
// If opts.Endpoint is empty, tracing is treated as disabled: a provider with
// NeverSample sampling (and no exporter) is returned so call sites that
// already invoke otel.Tracer(...).Start(...) keep compiling and running, but
// no spans are produced. Shutdown still flushes the provider so any future
// addition of a non-OTLP processor is closed cleanly.
//
// Returns an error if:
//   - opts.SamplerRatio is outside [0.0, 1.0]
//   - the OTLP exporter cannot be constructed (network setup is lazy, so this
//     normally only fires on programmer errors)
//   - the resource cannot be built
func InitTracer(ctx context.Context, opts TracingOptions) (*sdktrace.TracerProvider, ShutdownFunc, error) {
	if opts.SamplerRatio < 0.0 || opts.SamplerRatio > 1.0 {
		return nil, nil, fmt.Errorf(
			"observability: SamplerRatio must be in [0.0, 1.0] (got %v)", opts.SamplerRatio,
		)
	}

	res, err := buildResource(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("observability: build resource: %w", err)
	}

	// Disabled mode: empty endpoint → no exporter, NeverSample sampler. We
	// still register a TracerProvider + propagators so caller code stays
	// uniform.
	if opts.Endpoint == "" {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.NeverSample()),
		)
		installGlobals(tp)
		return tp, idempotentShutdown(tp), nil
	}

	exporter, err := newOTLPExporter(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("observability: build OTLP exporter: %w", err)
	}

	batchTimeout := opts.BatchTimeout
	if batchTimeout <= 0 {
		batchTimeout = 5 * time.Second
	}
	exportTimeout := opts.ExportTimeout
	if exportTimeout <= 0 {
		exportTimeout = 10 * time.Second
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SamplerRatio))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(batchTimeout),
			sdktrace.WithExportTimeout(exportTimeout),
		),
	)
	installGlobals(tp)

	return tp, idempotentShutdown(tp), nil
}

// newOTLPExporter constructs the gRPC trace exporter without blocking on a
// connection. The exporter dials lazily on the first export call.
func newOTLPExporter(ctx context.Context, opts TracingOptions) (*otlptrace.Exporter, error) {
	clientOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(opts.Endpoint),
	}
	if opts.Insecure {
		clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
	}
	client := otlptracegrpc.NewClient(clientOpts...)
	return otlptrace.New(ctx, client)
}

// buildResource composes the OTel Resource that identifies this process.
// Empty fields are dropped so the exporter doesn't emit blank attributes.
func buildResource(ctx context.Context, opts TracingOptions) (*resource.Resource, error) {
	service := opts.ServiceName
	if service == "" {
		service = "arena-service"
	}

	instance := opts.InstanceID
	if instance == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			instance = h
		}
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(service),
	}
	if opts.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(opts.ServiceVersion))
	}
	if opts.Environment != "" {
		// deployment.environment.name is the canonical key from semconv v1.26+.
		// We emit it via attribute.String rather than semconv.Deployment*Name
		// so the build is stable across minor semconv shuffles (v1.26 → v1.27
		// renamed the helper). The underlying key on the wire is identical.
		attrs = append(attrs, attribute.String("deployment.environment.name", opts.Environment))
	}
	if instance != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(instance))
	}

	return resource.New(ctx, resource.WithAttributes(attrs...))
}

// installGlobals registers tp as the global TracerProvider and installs the
// W3C TraceContext + Baggage propagators. Splitting this out makes the
// disabled-mode branch (no exporter) identical to the configured branch from
// the caller's perspective.
func installGlobals(tp *sdktrace.TracerProvider) {
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// idempotentShutdown wraps tp.Shutdown so it can be called from multiple
// goroutines and only runs the underlying shutdown sequence once. Subsequent
// calls return the result of the first invocation.
//
// tp.Shutdown itself flushes any registered span processors (including the
// OTLP batcher) and shuts down the exporter, so we don't need to track the
// exporter separately.
func idempotentShutdown(tp *sdktrace.TracerProvider) ShutdownFunc {
	var (
		once sync.Once
		err  error
	)
	return func(ctx context.Context) error {
		once.Do(func() {
			err = tp.Shutdown(ctx)
		})
		return err
	}
}

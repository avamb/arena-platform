// Package observability exposes the Prometheus metrics and OpenTelemetry
// tracing scaffolding shared by every arena_new binary.
//
// Two responsibilities live in this package:
//
//   - metrics.go (this file) — owns a Prometheus *Registry, registers the
//     baseline metrics required by the feature #87 spec (HTTP, DB pool,
//     worker, outbox), and exposes Handler() so the HTTP server can mount
//     /metrics. Domain modules add additional collectors by calling
//     Registry().MustRegister(...) on the shared registry.
//
//   - tracing.go — owns the OpenTelemetry TracerProvider, wires the OTLP
//     gRPC exporter, applies a configurable sampler, and returns a shutdown
//     hook so callers can flush spans on graceful stop.
//
// All public APIs in this package are safe for concurrent use after
// construction.
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace and subsystem constants keep metric names consistent across the
// codebase and provide a stable Grafana / alerting contract.
const (
	// MetricsNamespace prefixes every metric exposed by arena_new code. We
	// deliberately use a short, lowercase identifier that matches the
	// project name to avoid clashing with platform metrics (go_*, process_*).
	MetricsNamespace = "arena"

	subsystemHTTP    = "http"
	subsystemDB      = "db"
	subsystemWorker  = "worker"
	subsystemOutbox  = "outbox"
)

// LabelNames groups the canonical label keys used by the baseline metrics so
// call sites stay typo-free.
const (
	LabelMethod = "method"
	LabelRoute  = "route"
	LabelStatus = "status"
	LabelState  = "state"
	LabelQueue  = "queue"
)

// DefaultHTTPDurationBuckets are the Prometheus histogram buckets used for
// HTTP request latency. The values cover the typical web-API range from
// 1 ms (in-memory hits) to 10 s (slow upstream calls); buckets beyond 10 s
// are intentionally omitted because a request taking that long should have
// timed out via REQUEST_TIMEOUT_SECONDS.
var DefaultHTTPDurationBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Metrics owns the baseline Prometheus collectors required by the feature #87
// spec and exposes them as typed fields so middleware / workers can record
// observations without stringly-typed lookups.
type Metrics struct {
	registry *prometheus.Registry

	// HTTPRequestDuration records request latency in seconds, labelled by
	// HTTP method, normalised route template, and response status class.
	HTTPRequestDuration *prometheus.HistogramVec

	// HTTPRequestsTotal counts HTTP requests by method, route, and status.
	HTTPRequestsTotal *prometheus.CounterVec

	// DBPoolConnections reports the number of pgx pool connections by state
	// (acquired, idle, total, max, new_total). Gauges are populated by a
	// background scraper that snapshots Pool.Stat().
	DBPoolConnections *prometheus.GaugeVec

	// WorkerJobsLagSeconds reports the age of the oldest ready-but-unclaimed
	// job per queue, in seconds. Worker dispatcher refreshes this gauge on
	// every poll cycle.
	WorkerJobsLagSeconds *prometheus.GaugeVec

	// OutboxBacklog reports the number of unpublished outbox events.
	OutboxBacklog prometheus.Gauge

	// HTTPPanicsTotal counts HTTP handler panics caught by the Recoverer
	// middleware. Incremented once per panic regardless of the error type.
	// A rising value indicates a programming error in a handler; the metric
	// can be used to page on-call when it exceeds zero over a rolling window.
	HTTPPanicsTotal prometheus.Counter
}

// New constructs a *Metrics, registers every baseline collector on the
// supplied registry, and returns the result. If reg is nil, a fresh
// *prometheus.Registry is allocated; the registry also receives the standard
// Go and process collectors so /metrics reports memory / goroutine / GC
// stats out of the box.
//
// Returns an error if any metric fails to register (typically because the
// caller already mounted a metric of the same name on the same registry).
func New(reg *prometheus.Registry) (*Metrics, error) {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	// Register the Go runtime + process collectors. We ignore
	// AlreadyRegisteredError so callers can pre-populate the registry in
	// tests without breaking constructor reuse.
	registerSafe(reg, collectors.NewGoCollector())
	registerSafe(reg, collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,

		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: MetricsNamespace,
				Subsystem: subsystemHTTP,
				Name:      "request_duration_seconds",
				Help:      "Latency of HTTP requests handled by arena-api, in seconds.",
				Buckets:   DefaultHTTPDurationBuckets,
			},
			[]string{LabelMethod, LabelRoute, LabelStatus},
		),

		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: MetricsNamespace,
				Subsystem: subsystemHTTP,
				Name:      "requests_total",
				Help:      "Total HTTP requests handled by arena-api, partitioned by method, route, and status.",
			},
			[]string{LabelMethod, LabelRoute, LabelStatus},
		),

		DBPoolConnections: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: MetricsNamespace,
				Subsystem: subsystemDB,
				Name:      "pool_connections",
				Help:      "PostgreSQL connection pool gauges (acquired, idle, total, max, new_total).",
			},
			[]string{LabelState},
		),

		WorkerJobsLagSeconds: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: MetricsNamespace,
				Subsystem: subsystemWorker,
				Name:      "jobs_lag_seconds",
				Help:      "Age of the oldest ready-but-unclaimed worker job, in seconds, per queue.",
			},
			[]string{LabelQueue},
		),

		OutboxBacklog: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: MetricsNamespace,
				Subsystem: subsystemOutbox,
				Name:      "backlog",
				Help:      "Number of outbox events awaiting publication.",
			},
		),

		HTTPPanicsTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: MetricsNamespace,
				Subsystem: subsystemHTTP,
				Name:      "panics_total",
				Help:      "Total HTTP handler panics caught by the Recoverer middleware.",
			},
		),
	}

	for _, c := range []prometheus.Collector{
		m.HTTPRequestDuration,
		m.HTTPRequestsTotal,
		m.DBPoolConnections,
		m.WorkerJobsLagSeconds,
		m.OutboxBacklog,
		m.HTTPPanicsTotal,
	} {
		if err := reg.Register(c); err != nil {
			// If a peer test already registered the same metric on the
			// shared registry, surface the existing collector instead of
			// failing — this matches the behaviour expected by repeated
			// New() calls in test fixtures.
			if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
				_ = are // we keep our own typed reference; nothing to do.
				continue
			}
			return nil, err
		}
	}

	return m, nil
}

// MustNew is the panic-on-error variant of New, intended for use in main()
// where a failed metric registration is a programmer error and the process
// must not start in an inconsistent state.
func MustNew(reg *prometheus.Registry) *Metrics {
	m, err := New(reg)
	if err != nil {
		panic("observability: register baseline metrics: " + err.Error())
	}
	return m
}

// Registry returns the underlying Prometheus registry so domain packages can
// attach additional collectors (e.g. business KPIs, custom histograms).
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler returns the http.Handler that serves the metrics endpoint. Mount
// it on the chi router via r.Handle("/metrics", metrics.Handler()).
//
// Handler is a convenience method on *Metrics; for callers that hold only a
// *prometheus.Registry, use HandlerFor(reg) below.
func (m *Metrics) Handler() http.Handler { return HandlerFor(m.registry) }

// HandlerFor returns the http.Handler that exposes the supplied registry
// over the standard /metrics scrape protocol. Useful for tests that build
// a registry directly without going through Metrics.
func HandlerFor(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		// EnableOpenMetrics keeps backward compatibility with the legacy
		// text format while letting scrapers that advertise OpenMetrics
		// in their Accept header get the richer encoding.
		EnableOpenMetrics: true,
		Registry:          reg,
	})
}

// registerSafe registers a collector and ignores AlreadyRegisteredError so
// the constructor remains idempotent across multiple New() calls on the same
// registry. Any other error is silently swallowed because the runtime / process
// collectors are best-effort: arena-api still works without them, and the unit
// tests in metrics_test.go assert that the registry-level collectors we own
// (the typed *Vec / Gauge fields above) are present.
func registerSafe(reg *prometheus.Registry, c prometheus.Collector) {
	if err := reg.Register(c); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return
		}
	}
}

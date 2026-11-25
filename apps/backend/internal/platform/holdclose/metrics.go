package holdclose

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the job's Prometheus collectors (spec 67 section 5.2).
type Metrics struct {
	// Lag is arena_hold_close_lag_seconds: from the hold's expires_at to the
	// release of its seats.
	Lag prometheus.Histogram
	// Expire is arena_hold_stripe_expire_total{outcome}: what each read or
	// close of a provider page came to.
	Expire *prometheus.CounterVec
}

// NewMetrics builds unregistered collectors; register Collectors() once.
func NewMetrics() *Metrics {
	return &Metrics{
		Lag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "arena_hold_close_lag_seconds",
			Help:    "Seconds from a seat hold's expires_at to the release of its seats after its payment page was closed (spec 67 section 5.2).",
			Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1200, 1800},
		}),
		Expire: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "arena_hold_stripe_expire_total",
			Help: "Provider payment page decisions of hold.close_payment by outcome (closed, already_expired, complete, in_progress, open, error).",
		}, []string{"outcome"}),
	}
}

// Collectors lists the collectors for registration.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Lag, m.Expire}
}

// ObserveLag records one release.
func (m *Metrics) ObserveLag(d time.Duration) {
	if d < 0 {
		d = 0
	}
	m.Lag.Observe(d.Seconds())
}

// CountExpire counts one outcome.
func (m *Metrics) CountExpire(outcome string) {
	m.Expire.WithLabelValues(outcome).Inc()
}

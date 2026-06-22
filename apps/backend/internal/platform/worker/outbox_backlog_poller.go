// Package worker — outbox backlog poller (feature #102).
//
// OutboxBacklogPoller runs a background ticker that periodically queries
// the number of undelivered outbox rows (dispatched_at IS NULL) and
// records the result in the arena_outbox_backlog Prometheus gauge.
//
// This is a pure monitoring side-car: it never modifies the outbox table.
// If the query fails the previous gauge value is left unchanged and the
// error is logged at WARN level so alerting pipelines can detect
// persistent DB connectivity problems independently of the job-queue loop.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DefaultOutboxBacklogPollInterval is the polling cadence used when no
// explicit interval is supplied to NewOutboxBacklogPoller.
const DefaultOutboxBacklogPollInterval = 5 * time.Second

// OutboxBacklogQuerier is the minimal read-only interface the poller needs.
// PGOutboxBacklogQuerier provides the production implementation; tests
// supply a lightweight fake.
type OutboxBacklogQuerier interface {
	// CountUndispatched returns the number of outbox rows whose
	// dispatched_at column is NULL (i.e. not yet delivered).
	CountUndispatched(ctx context.Context) (int64, error)
}

// OutboxBacklogPollerOptions carries the dependencies for the poller.
// Zero-value PollInterval is replaced by DefaultOutboxBacklogPollInterval.
type OutboxBacklogPollerOptions struct {
	Querier      OutboxBacklogQuerier
	Gauge        prometheus.Gauge
	Logger       *slog.Logger
	PollInterval time.Duration
}

// OutboxBacklogPoller refreshes the outbox backlog gauge on a timer.
type OutboxBacklogPoller struct {
	opts OutboxBacklogPollerOptions
}

// NewOutboxBacklogPoller constructs an OutboxBacklogPoller from opts.
// Panics if Querier or Gauge is nil (programmer error — both must be wired
// before the process starts polling).
func NewOutboxBacklogPoller(opts OutboxBacklogPollerOptions) *OutboxBacklogPoller {
	if opts.Querier == nil {
		panic("worker: OutboxBacklogPoller requires a non-nil Querier")
	}
	if opts.Gauge == nil {
		panic("worker: OutboxBacklogPoller requires a non-nil Gauge")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultOutboxBacklogPollInterval
	}
	return &OutboxBacklogPoller{opts: opts}
}

// Run blocks until ctx is cancelled, polling the outbox on each tick.
// The first poll fires immediately so the gauge has a real value before
// the first interval elapses. Run returns nil; it never returns an error
// (query failures are logged but do not abort the loop).
func (p *OutboxBacklogPoller) Run(ctx context.Context) error {
	p.poll(ctx)

	ticker := time.NewTicker(p.opts.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.opts.Logger.Info("outbox backlog poller stopped")
			return nil
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// poll executes the COUNT query and updates the gauge. Errors are logged
// but swallowed so a temporary DB hiccup does not crash the worker.
func (p *OutboxBacklogPoller) poll(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	n, err := p.opts.Querier.CountUndispatched(queryCtx)
	if err != nil {
		p.opts.Logger.Warn("outbox backlog poll failed",
			"error", err.Error(),
		)
		return
	}

	p.opts.Gauge.Set(float64(n))
	p.opts.Logger.Debug("outbox backlog updated", "backlog", n)
}

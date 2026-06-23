// lag_probe.go — outbox-lag readiness probe (feature #112).
//
// OutboxLagProbe implements the ReadinessProbe contract (from httpserver)
// structurally (ProbeName() string + Ping(context.Context) error), without
// importing the httpserver package — Go's structural typing handles the rest.
//
// The probe fails when the number of undelivered outbox rows (dispatched_at
// IS NULL) reaches or exceeds a configurable threshold. This makes it
// possible to surface stalled outbox dispatchers in the /readyz endpoint
// before they cause visible SLO violations in the event-delivery pipeline.
package outbox

import (
	"context"
	"fmt"
	"time"
)

// DefaultOutboxLagThreshold is the default backlog count at which the
// OutboxLagProbe begins failing. Operators can override this value by
// passing an explicit threshold to NewOutboxLagProbe.
const DefaultOutboxLagThreshold = int64(100)

// LagCounter is the minimal interface required to count undelivered outbox
// rows. It is satisfied by worker.PGOutboxBacklogQuerier (and any other
// implementation that executes SELECT count(*) FROM outbox WHERE
// dispatched_at IS NULL). Defining it here avoids importing the worker
// package and keeps the dependency graph clean.
type LagCounter interface {
	// CountUndispatched returns the number of outbox rows whose
	// dispatched_at column is NULL (i.e. not yet delivered).
	CountUndispatched(ctx context.Context) (int64, error)
}

// OutboxLagProbe implements the ReadinessProbe interface by checking that the
// outbox backlog does not exceed a configurable threshold.
//
// If the count of undelivered events is >= Threshold, the probe returns a
// descriptive error so /readyz returns 503. This makes the probe suitable as
// an early-warning mechanism for stuck outbox dispatchers.
type OutboxLagProbe struct {
	counter   LagCounter
	threshold int64
	probeName string
}

// NewOutboxLagProbe constructs an OutboxLagProbe.
//
//   - counter   is the LagCounter used to query the outbox table.
//   - threshold is the inclusive backlog count at which the probe starts
//     failing (fails when backlog >= threshold). A threshold ≤ 0 is
//     replaced by DefaultOutboxLagThreshold (100).
//   - probeName is the key published in the /readyz checks map. Defaults
//     to "outbox" when empty.
func NewOutboxLagProbe(counter LagCounter, threshold int64, probeName string) *OutboxLagProbe {
	if threshold <= 0 {
		threshold = DefaultOutboxLagThreshold
	}
	if probeName == "" {
		probeName = "outbox"
	}
	return &OutboxLagProbe{
		counter:   counter,
		threshold: threshold,
		probeName: probeName,
	}
}

// ProbeName returns the stable identifier used in the /readyz checks map.
func (p *OutboxLagProbe) ProbeName() string { return p.probeName }

// Ping queries the outbox backlog and returns nil when the count is below
// the configured threshold, or an error when it is at or above the threshold.
//
// A 3-second query timeout is applied independently of any deadline already
// on ctx so the probe always completes quickly even when the database is
// under heavy load.
func (p *OutboxLagProbe) Ping(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	n, err := p.counter.CountUndispatched(queryCtx)
	if err != nil {
		return fmt.Errorf("outbox lag: count query failed: %w", err)
	}
	if n >= p.threshold {
		return fmt.Errorf("outbox lag: backlog %d exceeds threshold %d", n, p.threshold)
	}
	return nil
}

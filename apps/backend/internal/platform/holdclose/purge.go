package holdclose

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// PurgeJobType is the worker_jobs.job_type of the visitor hash purge (spec 67
// section 6.1).
const PurgeJobType = "hold.visitor_hash_purge"

// DefaultPurgeInterval is the gap between purge runs.
const DefaultPurgeInterval = time.Hour

// HashRetention is how long the visitor hashes of a closed hold are kept.
const HashRetention = 24 * time.Hour

// purgeBatch and purgeMaxBatches bound one purge run.
const (
	purgeBatch      = 1000
	purgeMaxBatches = 20
)

// Purger erases the visitor hashes of holds closed before closedBefore.
// *gen.Queries satisfies it.
type Purger interface {
	PurgeVisitorHashes(ctx context.Context, closedBefore time.Time, limit int32) (int64, error)
}

// PurgeOptions configures NewPurgeHandler.
type PurgeOptions struct {
	Purger    Purger
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
	Now       func() time.Time
}

// RunPurge erases, in bounded batches, the visitor hashes of every hold
// closed more than HashRetention before now. It returns the rows changed.
func RunPurge(ctx context.Context, p Purger, now time.Time) (int64, error) {
	var total int64
	for i := 0; i < purgeMaxBatches; i++ {
		n, err := p.PurgeVisitorHashes(ctx, now.Add(-HashRetention), purgeBatch)
		if err != nil {
			return total, fmt.Errorf("holdclose: purge visitor hashes: %w", err)
		}
		total += n
		if n < purgeBatch {
			break
		}
	}
	return total, nil
}

// NewPurgeHandler returns the handler of PurgeJobType (a worker.HandlerFunc).
func NewPurgeHandler(opts PurgeOptions) func(ctx context.Context, payload []byte) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultPurgeInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return func(ctx context.Context, _ []byte) error {
		n, err := RunPurge(ctx, opts.Purger, opts.Now())
		if err != nil {
			return err
		}
		if opts.Scheduler != nil {
			if serr := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); serr != nil {
				return fmt.Errorf("holdclose: schedule next purge: %w", serr)
			}
		}
		opts.Logger.Info("hold visitor hash purge complete", "purged", n)
		return nil
	}
}

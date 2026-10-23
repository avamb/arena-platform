package collect

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// JobType is the worker_jobs.job_type of the collect job.
const JobType = "billing.collect"

// DefaultInterval is the gap between passes.
const DefaultInterval = 60 * time.Second

// Scheduler enqueues the next pass. Returning an error fails the handler.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext inserts a pending job due at `at` unless one is already
// pending, so a restart or a retry never forks a second chain.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("billing collect: schedule next pass: %w", err)
	}
	return nil
}

// ScheduleInitialJob seeds the first pass at worker start unless a job is
// already pending or running. Every later pass is enqueued by the handler.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now()
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`
	if _, err := pool.Exec(ctx, q, JobType); err != nil {
		return fmt.Errorf("billing collect: enqueue initial job: %w", err)
	}
	return nil
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	Collector *Collector
	Scheduler Scheduler
	// Interval is the gap between passes; defaults to DefaultInterval.
	Interval time.Duration
	Logger   *slog.Logger
}

// NewHandler returns the handler of job_type JobType; its signature matches
// worker.HandlerFunc. With no date X it does nothing and does not schedule
// itself again.
//
// The next pass is scheduled BEFORE the work, so a pass that fails every
// retry does not end the chain; two passes that overlap are harmless, because
// events are written idempotently and a cursor only ever records rows its own
// pass has read.
func NewHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		if opts.Collector == nil || !opts.Collector.Enabled() {
			return nil
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return err
			}
		}
		results, err := opts.Collector.Run(ctx)
		inserted := 0
		for _, r := range results {
			inserted += r.Inserted
		}
		if err != nil {
			opts.Logger.Error("billing collect pass failed", "error", err.Error(), "inserted", inserted)
			return err
		}
		if inserted > 0 {
			opts.Logger.Info("billing collect pass", "inserted", inserted, "sources", len(results))
		}
		return nil
	}
}

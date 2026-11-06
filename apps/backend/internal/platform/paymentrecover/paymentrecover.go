// Package paymentrecover is the self-scheduling checkout.payment_recover job
// (П-19, spec 62 §5.4 п. 2.4): it settles hosted payment attempts that were
// recorded before the provider call but never got their payment page — the
// provider's answer was lost or the process died mid-call. The work itself is
// hfeed.PaymentRecoverer; this package only runs it on a schedule, the same
// way reservationexpiry runs the hold sweep.
package paymentrecover

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// JobType is the worker_jobs.job_type of the recovery.
const JobType = "checkout.payment_recover"

// DefaultInterval is the gap between two runs.
const DefaultInterval = time.Minute

// DefaultBatchSize caps the attempts looked at per provider and run; each is
// one or two provider calls.
const DefaultBatchSize = 50

// Stats is what one run reports.
type Stats struct {
	Examined, Applied, Relinked, Pending, TooLate, Failed int
	// Reconciled counts the handed-out pages asked about by the
	// reconciliation pass (П-15), ReconcileSettled the ones it settled.
	Reconciled, ReconcileSettled int
}

// Processor does one pass (hfeed.PaymentRecoverer through an adapter).
type Processor interface {
	RecoverStalledPayments(ctx context.Context, limit int32) (Stats, error)
}

// Scheduler enqueues the next run.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler enqueues the next run in worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler builds the production scheduler.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext implements Scheduler.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("paymentrecover: schedule next run: %w", err)
	}
	return nil
}

// Options configures NewHandler.
type Options struct {
	Processor Processor
	Logger    *slog.Logger
	Interval  time.Duration
	BatchSize int32
	Scheduler Scheduler
}

// NewHandler returns the worker handler. The next run is scheduled even when
// this one failed: a provider outage must not stop the recovery for good.
func NewHandler(opts Options) func(ctx context.Context, payload []byte) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	return func(ctx context.Context, _ []byte) error {
		var runErr error
		var stats Stats
		if opts.Processor != nil {
			stats, runErr = opts.Processor.RecoverStalledPayments(ctx, opts.BatchSize)
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return fmt.Errorf("paymentrecover: schedule next run: %w", err)
			}
		}
		if runErr != nil {
			// Logged, not returned: the next run is already queued, and a
			// retried job would queue a second one beside it.
			opts.Logger.Error("checkout payment recovery failed", "error", runErr.Error())
			return nil
		}
		if stats.Examined > 0 || stats.Reconciled > 0 {
			opts.Logger.Info("checkout payment recovery complete",
				"examined", stats.Examined,
				"applied", stats.Applied,
				"relinked", stats.Relinked,
				"pending", stats.Pending,
				"too_late", stats.TooLate,
				"failed", stats.Failed,
				"reconciled", stats.Reconciled,
				"reconcile_settled", stats.ReconcileSettled,
			)
		}
		return nil
	}
}

// ScheduleInitialJob seeds the first run when none is queued.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const checkSQL = `
		SELECT count(*)
		  FROM worker_jobs
		 WHERE job_type = $1
		   AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("paymentrecover: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, JobType); err != nil {
		return fmt.Errorf("paymentrecover: enqueue initial job: %w", err)
	}
	return nil
}

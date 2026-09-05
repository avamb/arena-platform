package ordering

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────────────────────────────────────────────────────────
// order.expire_sweep — the reaper for unpaid orders
// ─────────────────────────────────────────────────────────────────────────────
//
// Spec §14.1: once a minute, every pending_payment order whose expires_at has
// passed and whose checkout session never produced a succeeded payment intent
// becomes 'expired'. Inventory itself is released by the existing reservation
// TTL worker — this job only closes the order aggregate so the admin list and
// GET_ORDER_INFO stop showing a purchase that will never complete, and so the
// "one open order per customer+session" index frees up.
//
// The job is self-scheduling in the same cron-like way as idempotency.cleanup:
// each successful run enqueues the next one. That keeps the worker's job
// vocabulary uniform (no separate ticker goroutine to supervise) and means a
// worker restart resumes the cadence from ScheduleInitialExpireSweepJob.

// ExpireSweepJobType is the worker_jobs.job_type this package handles.
const ExpireSweepJobType = "order.expire_sweep"

// DefaultSweepInterval is the gap between successive sweeps (spec §14.1:
// "once a minute").
const DefaultSweepInterval = time.Minute

// DefaultSweepBatchSize caps how many orders a single run expires. A cap keeps
// one run bounded in time and in lock footprint; anything left over is picked
// up by the next tick a minute later, which is well inside the tolerance for
// "this order is dead".
const DefaultSweepBatchSize = 500

// SweepScheduler enqueues the next sweep run. Returning an error fails the
// current handler invocation, which the worker then retries normally.
type SweepScheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGSweepScheduler is the production SweepScheduler backed by worker_jobs.
type PGSweepScheduler struct {
	pool *pgxpool.Pool
}

// NewPGSweepScheduler wraps a pgx pool into a PGSweepScheduler.
func NewPGSweepScheduler(pool *pgxpool.Pool) *PGSweepScheduler {
	return &PGSweepScheduler{pool: pool}
}

// ScheduleNext implements SweepScheduler by inserting a pending worker_jobs
// row due at `at`.
func (s *PGSweepScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, ExpireSweepJobType, at); err != nil {
		return fmt.Errorf("ordering: schedule next expire sweep: %w", err)
	}
	return nil
}

// ExpireSweepOptions configures the handler returned by NewExpireSweepHandler.
type ExpireSweepOptions struct {
	// Store is the query surface (a *gen.Queries). Required.
	Store SweepStore

	// Logger receives one summary line per run. nil uses slog.Default().
	Logger *slog.Logger

	// Interval is the gap between runs; defaults to DefaultSweepInterval.
	Interval time.Duration

	// BatchSize caps orders expired per run; defaults to
	// DefaultSweepBatchSize.
	BatchSize int32

	// Scheduler enqueues the next run. nil disables self-scheduling, which is
	// what one-shot tests want.
	Scheduler SweepScheduler

	// Now is injectable for deterministic tests; defaults to time.Now().UTC.
	Now func() time.Time
}

// NewExpireSweepHandler returns the handler for job_type=ExpireSweepJobType.
//
// Its signature matches worker.HandlerFunc so it can be passed straight to
// (*worker.Registry).Register without ordering having to import worker.
func NewExpireSweepHandler(opts ExpireSweepOptions) func(ctx context.Context, payload []byte) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultSweepInterval
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultSweepBatchSize
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}

	return func(ctx context.Context, _ []byte) error {
		expired, err := RunExpireSweep(ctx, opts.Store, opts.Now(), opts.BatchSize, opts.Logger)
		if err != nil {
			return err
		}

		if opts.Scheduler != nil {
			if schedErr := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); schedErr != nil {
				return fmt.Errorf("ordering expire sweep: schedule next run: %w", schedErr)
			}
		}

		opts.Logger.Info("order expire sweep complete", "expired", expired)
		return nil
	}
}

// RunExpireSweep performs one sweep pass and reports how many orders it
// expired. Exported so the integration test — and any future admin "run it
// now" action — can drive a single deterministic pass without the scheduling
// machinery.
//
// Each order is flipped with the status-guarded ExpireOrderIfStillPending
// rather than a read-then-write: if a payment webhook wins the race between
// the candidate query and the update, the guard matches zero rows and the
// order is skipped instead of being expired out from under a paying customer.
func RunExpireSweep(ctx context.Context, q SweepStore, now time.Time, batchSize int32, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}

	candidates, err := q.ListExpirableOrders(ctx, now, batchSize)
	if err != nil {
		return 0, fmt.Errorf("ordering: list expirable orders: %w", err)
	}

	expired := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return expired, err
		}

		row, err := q.ExpireOrderIfStillPending(ctx, c.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Lost the race with a webhook (or another worker): the
				// order already moved on. Not an error.
				logger.Debug("order expire sweep: order no longer pending", "order_id", c.ID)
				continue
			}
			return expired, fmt.Errorf("ordering: expire order %s: %w", c.ID, err)
		}

		if _, err := q.InsertOrderEvent(ctx, row.ID, EventHoldExpired, ActorSystem, marshalPayload(map[string]any{
			"job":        ExpireSweepJobType,
			"expires_at": row.ExpiresAt,
		})); err != nil {
			return expired, fmt.Errorf("ordering: insert hold_expired event for %s: %w", row.ID, err)
		}
		expired++
	}
	return expired, nil
}

// ScheduleInitialExpireSweepJob enqueues the first sweep at arena-worker
// startup unless one is already pending or claimed. Every later run is
// self-scheduled by the handler.
func ScheduleInitialExpireSweepJob(ctx context.Context, pool *pgxpool.Pool) error {
	const checkSQL = `
		SELECT count(*)
		  FROM worker_jobs
		 WHERE job_type = $1
		   AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, ExpireSweepJobType).Scan(&cnt); err != nil {
		return fmt.Errorf("ordering: check initial expire sweep job: %w", err)
	}
	if cnt > 0 {
		return nil
	}

	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, ExpireSweepJobType); err != nil {
		return fmt.Errorf("ordering: enqueue initial expire sweep job: %w", err)
	}
	return nil
}

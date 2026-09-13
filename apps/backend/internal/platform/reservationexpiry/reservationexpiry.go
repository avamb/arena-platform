package reservationexpiry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────────────────────────────────────────────────────────
// reservation.expire_sweep — the reaper for TTL-expired reservation holds
// ─────────────────────────────────────────────────────────────────────────────
//
// hcheckout.ReservationProcessor.ProcessExpiredReservations has existed since
// feature #131 but was, until this package, only ever invoked from
// integration tests: no process in arena-api or arena-worker actually
// scheduled it. In production that meant a reservation's hold (the
// reservations row, its held session_seats/ga_unit rows, and the
// inventory_ledger.capacity_held counter) never expired on its own — a buyer
// who reserved and walked away left the seats/units stuck 'held' forever,
// showing the session as sold out even though nothing sold.
//
// This job closes that gap the same way order.expire_sweep
// (internal/platform/ordering/expire_sweep.go) closes the equivalent gap for
// pending_payment orders: it is self-scheduling — each successful run
// enqueues the next one via worker_jobs — so a worker restart resumes the
// cadence from ScheduleInitialJob and there is no separate ticker goroutine
// to supervise.
//
// order.expire_sweep only closes the order aggregate; THIS job is what
// actually releases the held inventory (seats, GA units, and the
// inventory_ledger rollup) that order.expire_sweep's own comment used to
// (incorrectly) claim was already handled by "the existing reservation TTL
// worker" — there was no such worker running until this package existed.

// JobType is the worker_jobs.job_type this package handles.
const JobType = "reservation.expire_sweep"

// DefaultInterval is the gap between successive sweeps. Reservation holds
// are short-lived (checkout TTLs measured in minutes), so this runs far more
// often than the once-a-minute order sweep.
const DefaultInterval = 30 * time.Second

// DefaultBatchSize caps how many reservations a single ProcessExpiredReservations
// pass expires. A cap keeps one pass bounded in time and in lock footprint.
const DefaultBatchSize = 500

// maxBatchesPerRun bounds how many ProcessExpiredReservations passes one
// handler invocation may make. When a pass expires a full batch there is
// likely more work waiting (e.g. after a worker outage); looping immediately
// catches up faster than waiting a full Interval between each batch. The cap
// keeps one job invocation from running unbounded — any backlog left after
// maxBatchesPerRun passes is picked up by the very next scheduled run.
const maxBatchesPerRun = 10

// Processor is the narrow interface the handler needs to process one batch
// of expired reservations. *hcheckout.ReservationProcessor satisfies it.
type Processor interface {
	ProcessExpiredReservations(ctx context.Context, limit int32) (int, error)
}

// Scheduler enqueues the next sweep run. Returning an error fails the
// current handler invocation, which the worker then retries normally.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler struct {
	pool *pgxpool.Pool
}

// NewPGScheduler wraps a pgx pool into a PGScheduler.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler {
	return &PGScheduler{pool: pool}
}

// ScheduleNext implements Scheduler by inserting a pending worker_jobs row
// due at `at`.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("reservationexpiry: schedule next sweep: %w", err)
	}
	return nil
}

// Options configures the handler returned by NewHandler.
type Options struct {
	// Processor runs one bounded pass over expired reservations. Required.
	Processor Processor

	// Logger receives one summary line per run. nil uses slog.Default().
	Logger *slog.Logger

	// Interval is the gap between runs; defaults to DefaultInterval.
	Interval time.Duration

	// BatchSize caps reservations expired per ProcessExpiredReservations
	// call; defaults to DefaultBatchSize.
	BatchSize int32

	// Scheduler enqueues the next run. nil disables self-scheduling, which
	// is what one-shot tests want.
	Scheduler Scheduler
}

// NewHandler returns the handler for job_type=JobType.
//
// Its signature matches worker.HandlerFunc so it can be passed straight to
// (*worker.Registry).Register without this package having to import worker.
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
		total := 0
		ran := 0
		for i := 0; i < maxBatchesPerRun; i++ {
			ran++
			processed, err := opts.Processor.ProcessExpiredReservations(ctx, opts.BatchSize)
			if err != nil {
				return fmt.Errorf("reservationexpiry: process batch: %w", err)
			}
			total += processed
			if processed < int(opts.BatchSize) {
				// Short batch — the backlog is drained (for now).
				break
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return fmt.Errorf("reservationexpiry: schedule next run: %w", err)
			}
		}

		opts.Logger.Info("reservation expire sweep complete",
			"expired", total,
			"batches", ran,
		)
		return nil
	}
}

// ScheduleInitialJob enqueues the first sweep at arena-worker startup unless
// one is already pending or claimed. Every later run is self-scheduled by
// the handler.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const checkSQL = `
		SELECT count(*)
		  FROM worker_jobs
		 WHERE job_type = $1
		   AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("reservationexpiry: check initial sweep job: %w", err)
	}
	if cnt > 0 {
		return nil
	}

	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, JobType); err != nil {
		return fmt.Errorf("reservationexpiry: enqueue initial sweep job: %w", err)
	}
	return nil
}

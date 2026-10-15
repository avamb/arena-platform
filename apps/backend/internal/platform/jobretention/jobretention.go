// Package jobretention deletes finished worker_jobs rows after a retention
// period.
//
// Nothing ever removed a row from worker_jobs: every self-scheduling sweep
// (reservation.expire_sweep, tier.chain_sweep, events.change_watch every
// 30 s, ops.watchdog, order.expire_sweep, refund.sweep every minute) leaves
// one 'done' row per run, about 12 000 rows a day, and the table became half
// of the database and of every dump. worker_jobs.cleanup is the
// self-scheduling job that keeps it bounded:
//
//   - 'done' rows are deleted DoneRetention (30 days) after scheduled_at,
//   - 'failed' rows FailedRetention (90 days) after scheduled_at — longer,
//     because a failed job is forensic evidence and refunds.requeueBuyerNotices
//     counts failed letter jobs; worker_dead_letter keeps its own copy and is
//     not touched,
//   - 'pending' and 'claimed' rows are never deleted.
//
// The age is measured on scheduled_at because worker_jobs has no completion
// timestamp: a job runs at or after its scheduled_at (a retry moves it
// forward), so a row whose scheduled_at is older than the cutoff finished at
// least that long ago in every case but a queue stalled for the whole
// retention period. scheduled_at is also what worker_jobs_status_scheduled_idx
// (status, scheduled_at) orders by, so each batch is an index range scan.
//
// Deletes run in batches of BatchSize ids picked by a LIMIT subquery, so one
// statement never holds many row locks; one run makes at most
// maxBatchesPerRun batches per status and leaves any backlog to the next run.
package jobretention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// JobType is the worker_jobs.job_type this package handles.
const JobType = "worker_jobs.cleanup"

const (
	// DefaultInterval is the gap between runs.
	DefaultInterval = time.Hour
	// DoneRetention is how long a 'done' row is kept.
	DoneRetention = 30 * 24 * time.Hour
	// FailedRetention is how long a 'failed' row is kept.
	FailedRetention = 90 * 24 * time.Hour
	// DefaultBatchSize caps the rows one DELETE statement removes.
	DefaultBatchSize = 5000
	// maxBatchesPerRun caps the DELETE statements per status in one run.
	maxBatchesPerRun = 20
)

// Statuses this package may delete. Anything else is refused by DeleteBatch.
const (
	StatusDone   = "done"
	StatusFailed = "failed"
)

// Store deletes up to limit rows of one terminal status whose scheduled_at
// is before cutoff and reports how many it deleted.
type Store interface {
	DeleteBatch(ctx context.Context, status string, cutoff time.Time, limit int) (int64, error)
}

// Scheduler enqueues the next run.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGStore is the production Store.
type PGStore struct{ pool *pgxpool.Pool }

// NewPGStore wraps a pgx pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// DeleteBatch implements Store. The status is checked twice — in Go and in
// the statement — so a caller can never reach a pending or claimed row.
func (s *PGStore) DeleteBatch(ctx context.Context, status string, cutoff time.Time, limit int) (int64, error) {
	if status != StatusDone && status != StatusFailed {
		return 0, fmt.Errorf("jobretention: refusing to delete status %q", status)
	}
	if limit <= 0 {
		return 0, nil
	}
	// id = ANY(ARRAY(...)) rather than id IN (...): the planner then looks
	// the picked ids up through the primary key instead of hash-joining them
	// against a sequential scan of the whole table.
	const q = `
		DELETE FROM worker_jobs
		 WHERE id = ANY(ARRAY(
		       SELECT id
		         FROM worker_jobs
		        WHERE status = $1
		          AND scheduled_at < $2
		        ORDER BY scheduled_at
		        LIMIT $3))
		   AND status IN ('done', 'failed')
	`
	tag, err := s.pool.Exec(ctx, q, status, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("jobretention: delete %s rows: %w", status, err)
	}
	return tag.RowsAffected(), nil
}

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pgx pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext implements Scheduler.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("jobretention: schedule next run: %w", err)
	}
	return nil
}

// Options configures the handler returned by NewHandler.
type Options struct {
	Store     Store
	Scheduler Scheduler // nil disables self-scheduling (one-shot tests)
	Logger    *slog.Logger
	Interval  time.Duration // defaults to DefaultInterval
	BatchSize int           // defaults to DefaultBatchSize
	// DoneRetention / FailedRetention default to the package constants.
	DoneRetention   time.Duration
	FailedRetention time.Duration
	Now             func() time.Time // defaults to time.Now
}

// NewHandler returns the handler for job_type=JobType. Its signature matches
// worker.HandlerFunc.
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
	if opts.DoneRetention <= 0 {
		opts.DoneRetention = DoneRetention
	}
	if opts.FailedRetention <= 0 {
		opts.FailedRetention = FailedRetention
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return func(ctx context.Context, _ []byte) error {
		now := opts.Now()
		done, err := deleteOlder(ctx, opts.Store, StatusDone, now.Add(-opts.DoneRetention), opts.BatchSize)
		if err != nil {
			return err
		}
		failed, err := deleteOlder(ctx, opts.Store, StatusFailed, now.Add(-opts.FailedRetention), opts.BatchSize)
		if err != nil {
			return err
		}

		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return err
			}
		}
		opts.Logger.Info("worker jobs cleanup complete", "deleted_done", done, "deleted_failed", failed)
		return nil
	}
}

// deleteOlder runs batches until one comes back short or maxBatchesPerRun
// is reached.
func deleteOlder(ctx context.Context, store Store, status string, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for i := 0; i < maxBatchesPerRun; i++ {
		n, err := store.DeleteBatch(ctx, status, cutoff, batch)
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			break
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
	return total, nil
}

// ScheduleInitialJob enqueues the first run at arena-worker startup unless
// one is already pending or claimed.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now()
		 WHERE NOT EXISTS (
		       SELECT 1 FROM worker_jobs
		        WHERE job_type = $1 AND status IN ('pending', 'claimed'))
	`
	if _, err := pool.Exec(ctx, q, JobType); err != nil {
		return fmt.Errorf("jobretention: enqueue initial run: %w", err)
	}
	return nil
}

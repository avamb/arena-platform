// Package idempotency — cleanup.go
//
// CleanupHandler implements the scheduled maintenance job for feature #48:
// "Expired idempotency keys cleaned by maintenance job".
//
// The job_type is "idempotency.cleanup". The handler:
//
//  1. Calls Cleaner.DeleteExpired with a cutoff = now() - RetentionBuffer.
//  2. Increments the IdempotencyCleanupDeletedTotal Prometheus counter by
//     the number of rows deleted.
//  3. Schedules the next run via CleanupScheduler.ScheduleNext (cron-like
//     self-scheduling).
//
// At arena-worker startup, ScheduleInitialCleanupJob ensures at least one
// pending job exists so the first cleanup run happens soon after launch.
package idempotency

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// CleanupJobType is the worker_jobs.job_type for idempotency key cleanup.
	CleanupJobType = "idempotency.cleanup"

	// DefaultCleanupInterval is the delay between successive cleanup runs
	// when using the self-scheduling cron mechanism. Each completion enqueues
	// the next run at now()+DefaultCleanupInterval.
	DefaultCleanupInterval = time.Hour
)

// -----------------------------------------------------------------------------
// Cleaner — persistence contract for the cleanup operation
// -----------------------------------------------------------------------------

// Cleaner deletes expired idempotency_keys rows whose expires_at is strictly
// before cutoff. It returns the number of rows deleted.
//
// PGCleaner provides the production implementation; tests supply a lightweight
// in-memory alternative.
type Cleaner interface {
	DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}

// PGCleaner is the PostgreSQL-backed Cleaner.
type PGCleaner struct {
	pool *pgxpool.Pool
}

// NewPGCleaner wraps a pgx pool into a PGCleaner.
func NewPGCleaner(pool *pgxpool.Pool) *PGCleaner { return &PGCleaner{pool: pool} }

// DeleteExpired implements Cleaner by running a single DELETE statement.
// cutoff is passed as a bind parameter so the planner can use the
// idempotency_keys_expires_at_idx partial index if present.
func (c *PGCleaner) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	const q = `DELETE FROM idempotency_keys WHERE expires_at < $1`
	tag, err := c.pool.Exec(ctx, q, cutoff)
	if err != nil {
		return 0, fmt.Errorf("idempotency: delete expired: %w", err)
	}
	return tag.RowsAffected(), nil
}

// -----------------------------------------------------------------------------
// CleanupScheduler — schedules the next cleanup run
// -----------------------------------------------------------------------------

// CleanupScheduler enqueues the next cleanup job at the given scheduled time.
// Returning a non-nil error causes the current handler invocation to fail,
// which triggers the normal worker retry/dead-letter mechanism.
type CleanupScheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGCleanupScheduler is the production CleanupScheduler backed by worker_jobs.
type PGCleanupScheduler struct {
	pool *pgxpool.Pool
}

// NewPGCleanupScheduler wraps a pgx pool into a PGCleanupScheduler.
func NewPGCleanupScheduler(pool *pgxpool.Pool) *PGCleanupScheduler {
	return &PGCleanupScheduler{pool: pool}
}

// ScheduleNext implements CleanupScheduler by inserting a worker_jobs row with
// status='pending' and scheduled_at=at.
func (s *PGCleanupScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, CleanupJobType, at); err != nil {
		return fmt.Errorf("idempotency: schedule next cleanup: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// CleanupOptions + NewCleanupHandler
// -----------------------------------------------------------------------------

// CleanupOptions configures the handler returned by NewCleanupHandler.
type CleanupOptions struct {
	// Cleaner is the persistence layer for deleting expired rows. Required.
	Cleaner Cleaner

	// DeletedCounter is incremented by the number of rows purged on each run.
	// nil disables Prometheus recording (useful in unit tests that verify
	// deletion counts another way).
	DeletedCounter prometheus.Counter

	// RetentionBuffer is the additional grace period after a key's expires_at
	// before it is eligible for deletion.
	// Default 0: rows are purged as soon as expires_at < now().
	// Example non-zero use: 5 * time.Minute delays cleanup so that very
	// recently expired keys still appear as "gone" to the middleware (which
	// already filters by expires_at > now()) without being immediately removed.
	RetentionBuffer time.Duration

	// CleanupInterval is the gap between successive cleanup runs. After each
	// successful run the handler enqueues the next job at now()+CleanupInterval.
	// Defaults to DefaultCleanupInterval (1 hour).
	CleanupInterval time.Duration

	// Scheduler enqueues the next cleanup run. nil disables self-scheduling
	// (useful for one-shot tests or environments without a worker queue).
	Scheduler CleanupScheduler
}

// NewCleanupHandler returns a handler function for job_type=CleanupJobType.
//
// The returned func has the same signature as worker.HandlerFunc — it can be
// passed directly to (*worker.Registry).Register — without creating a
// package-level import cycle between idempotency and worker.
func NewCleanupHandler(opts CleanupOptions) func(ctx context.Context, payload []byte) error {
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = DefaultCleanupInterval
	}

	return func(ctx context.Context, _ []byte) error {
		cutoff := time.Now().Add(-opts.RetentionBuffer)

		n, err := opts.Cleaner.DeleteExpired(ctx, cutoff)
		if err != nil {
			return err
		}

		if opts.DeletedCounter != nil {
			opts.DeletedCounter.Add(float64(n))
		}

		if opts.Scheduler != nil {
			nextAt := time.Now().Add(opts.CleanupInterval)
			if schedErr := opts.Scheduler.ScheduleNext(ctx, nextAt); schedErr != nil {
				return fmt.Errorf("idempotency cleanup: schedule next run: %w", schedErr)
			}
		}

		return nil
	}
}

// -----------------------------------------------------------------------------
// ScheduleInitialCleanupJob — called once at arena-worker startup
// -----------------------------------------------------------------------------

// ScheduleInitialCleanupJob inserts an idempotency.cleanup job into worker_jobs
// if no pending or in-progress cleanup job already exists. This ensures the
// first cleanup run happens shortly after the worker process starts.
//
// Subsequent runs are self-scheduled by the handler (cron-like behaviour):
// each successful invocation enqueues the next run at now()+CleanupInterval.
func ScheduleInitialCleanupJob(ctx context.Context, pool *pgxpool.Pool) error {
	const checkSQL = `
		SELECT count(*)
		  FROM worker_jobs
		 WHERE job_type = $1
		   AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, CleanupJobType).Scan(&cnt); err != nil {
		return fmt.Errorf("idempotency: check initial cleanup job: %w", err)
	}
	if cnt > 0 {
		return nil // already scheduled — nothing to do
	}

	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, CleanupJobType); err != nil {
		return fmt.Errorf("idempotency: enqueue initial cleanup job: %w", err)
	}
	return nil
}

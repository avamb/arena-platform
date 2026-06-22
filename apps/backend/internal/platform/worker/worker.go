// Package worker implements the PostgreSQL-backed background job queue
// for arena_new.
//
// The queue is persistent: jobs are rows in the worker_jobs table. Multiple
// worker instances can safely consume the same queue concurrently because
// claims use FOR UPDATE SKIP LOCKED (verified by the table layout in the
// 0001_init.sql migration).
//
// Lifecycle of a single job:
//
//  1. Producer INSERTs a row with status='pending', scheduled_at <= now().
//  2. Worker SELECT … FOR UPDATE SKIP LOCKED returns at most one row
//     whose scheduled_at has arrived, then UPDATEs it to status='claimed',
//     bumps attempts, sets claimed_at and claimed_by. The SELECT + UPDATE
//     run in a single short transaction so the row is owned by exactly one
//     worker instance.
//  3. The registered handler for job_type executes against the payload.
//  4. On nil error, the row is UPDATEd to status='done'.
//  5. On error, the row is UPDATEd back to status='pending' with
//     last_error set; the next poll cycle will pick it up again. If
//     attempts has reached max_attempts the job is UPDATEd to
//     status='failed' and a row is INSERTed into worker_dead_letter.
//
// The graceful-shutdown contract mirrors the HTTP server's: Stop cancels
// the polling context, waits for any in-flight handler to finish (or for
// shutdownTimeout to elapse), then returns. A handler that overruns the
// shutdown budget is logged but not killed — Go cannot safely abort an
// arbitrary goroutine. Operators size shutdownTimeout to accommodate the
// longest job.
//
// This implementation is intentionally minimal — it is the foundation
// scaffold for future business-domain jobs. Backoff policy, batch
// claiming, and Prometheus metrics are out of scope for this milestone
// but the seams (HandlerFunc signature, registry, Queue interface) are
// already shaped to accommodate them.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HandlerFunc is the contract every job-type handler must satisfy.
//
// payload is the raw JSON bytes stored on the worker_jobs row. The handler
// is responsible for decoding it into the shape it expects — keeping the
// registry agnostic of business types lets new job types be added without
// touching this package.
//
// Returning nil marks the job as completed (status='done'). Returning a
// non-nil error puts the job back into the pending queue for retry, or
// moves it to worker_dead_letter once max_attempts is exhausted.
type HandlerFunc func(ctx context.Context, payload []byte) error

// Registry maps a job_type string to its HandlerFunc.
//
// Registry is safe for concurrent reads after Register has finished.
// Workers are expected to register every handler at start-up and never
// mutate the registry while polling.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
}

// NewRegistry returns an empty Registry ready for Register.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]HandlerFunc)}
}

// Register associates a HandlerFunc with a job_type. Re-registering the
// same job_type overwrites the previous handler — useful in tests, never
// expected in production.
func (r *Registry) Register(jobType string, h HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[jobType] = h
}

// Lookup returns the handler for jobType, or (nil, false) if none is
// registered. A worker that pulls a job whose type has no handler treats
// the job as failed and lets the retry / dead-letter pipeline take over.
func (r *Registry) Lookup(jobType string) (HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[jobType]
	return h, ok
}

// Job is the in-memory representation of a worker_jobs row at the moment
// it was claimed. ID, Type, Payload, and Attempts are the only fields the
// runtime needs after the claim transaction returns.
type Job struct {
	ID          string
	Type        string
	Payload     []byte
	Attempts    int
	MaxAttempts int
}

// Queue is the persistence contract for the background job queue.
//
// PGQueue provides the production implementation backed by the worker_jobs
// PostgreSQL table. Tests supply an inMemoryQueue that mirrors the same
// semantics without requiring a live database.
type Queue interface {
	// ClaimNext atomically selects and claims the next pending job whose
	// scheduled_at is in the past, for the given instanceID. Returns
	// (nil, nil) when the queue is empty.
	ClaimNext(ctx context.Context, instanceID string) (*Job, error)

	// MarkDone sets status='done' on the row identified by jobID.
	MarkDone(ctx context.Context, jobID string) error

	// MarkRetry resets the row to status='pending', stores lastErr in
	// last_error, and clears claimed_at / claimed_by so the next worker
	// poll picks it up again.
	MarkRetry(ctx context.Context, jobID, lastErr string) error

	// MarkFailed sets status='failed' on the row and inserts a
	// corresponding record in worker_dead_letter.
	MarkFailed(ctx context.Context, job *Job, lastErr string) error
}

// PGQueue implements Queue against the worker_jobs PostgreSQL table.
type PGQueue struct {
	pool *pgxpool.Pool
}

// NewPGQueue wraps a pgxpool.Pool into a PGQueue.
func NewPGQueue(pool *pgxpool.Pool) *PGQueue {
	return &PGQueue{pool: pool}
}

// ClaimNext implements Queue by running a single CTE that SELECTs with
// FOR UPDATE SKIP LOCKED and then UPDATEs the matching row atomically.
func (q *PGQueue) ClaimNext(ctx context.Context, instanceID string) (*Job, error) {
	if q.pool == nil {
		return nil, errors.New("worker: nil pgxpool")
	}

	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := q.pool.BeginTx(queryCtx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit; pgx documents this
	// explicitly. We keep the defer to recover from any error path.
	defer func() { _ = tx.Rollback(context.Background()) }()

	const claimSQL = `
		WITH next AS (
			SELECT id
			  FROM worker_jobs
			 WHERE status = 'pending'
			   AND scheduled_at <= now()
			 ORDER BY scheduled_at ASC, created_at ASC
			 FOR UPDATE SKIP LOCKED
			 LIMIT 1
		)
		UPDATE worker_jobs j
		   SET status      = 'claimed',
		       attempts    = j.attempts + 1,
		       claimed_at  = now(),
		       claimed_by  = $1,
		       last_error  = NULL
		  FROM next
		 WHERE j.id = next.id
		 RETURNING j.id::text, j.job_type, j.payload, j.attempts, j.max_attempts
	`

	row := tx.QueryRow(queryCtx, claimSQL, instanceID)

	var (
		id          string
		jobType     string
		payload     []byte
		attempts    int
		maxAttempts int
	)
	if err := row.Scan(&id, &jobType, &payload, &attempts, &maxAttempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Commit(queryCtx)
			return nil, nil
		}
		return nil, fmt.Errorf("claim query: %w", err)
	}

	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}

	return &Job{
		ID:          id,
		Type:        jobType,
		Payload:     payload,
		Attempts:    attempts,
		MaxAttempts: maxAttempts,
	}, nil
}

// MarkDone implements Queue by setting status='done' on the row.
func (q *PGQueue) MarkDone(ctx context.Context, jobID string) error {
	updCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	const sql = `
		UPDATE worker_jobs
		   SET status     = 'done',
		       last_error = NULL
		 WHERE id = $1::uuid
	`
	if _, err := q.pool.Exec(updCtx, sql, jobID); err != nil {
		return fmt.Errorf("update done: %w", err)
	}
	return nil
}

// MarkRetry implements Queue by resetting the row to status='pending'.
func (q *PGQueue) MarkRetry(ctx context.Context, jobID, lastErr string) error {
	updCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	const retrySQL = `
		UPDATE worker_jobs
		   SET status      = 'pending',
		       last_error  = $2,
		       claimed_at  = NULL,
		       claimed_by  = NULL
		 WHERE id = $1::uuid
	`
	if _, err := q.pool.Exec(updCtx, retrySQL, jobID, lastErr); err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}
	return nil
}

// MarkFailed implements Queue by atomically inserting into worker_dead_letter
// and setting status='failed' on the row.
func (q *PGQueue) MarkFailed(ctx context.Context, job *Job, lastErr string) error {
	updCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := q.pool.BeginTx(updCtx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin dead-letter tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const deadLetterSQL = `
		INSERT INTO worker_dead_letter (
			original_job_id, job_type, payload, attempts, last_error,
			original_created_at
		)
		SELECT id, job_type, payload, attempts, $2, created_at
		  FROM worker_jobs
		 WHERE id = $1::uuid
	`
	if _, err := tx.Exec(updCtx, deadLetterSQL, job.ID, lastErr); err != nil {
		return fmt.Errorf("insert dead-letter: %w", err)
	}

	const failSQL = `
		UPDATE worker_jobs
		   SET status     = 'failed',
		       last_error = $2
		 WHERE id = $1::uuid
	`
	if _, err := tx.Exec(updCtx, failSQL, job.ID, lastErr); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}

	if err := tx.Commit(updCtx); err != nil {
		return fmt.Errorf("commit dead-letter tx: %w", err)
	}
	return nil
}

// Options configures a Worker.
type Options struct {
	// Pool is the pgx connection pool. Used to construct a PGQueue
	// automatically when Queue is nil. Required when Queue is nil.
	Pool *database.Pool

	// Queue provides a custom job-queue implementation. When non-nil,
	// Pool is ignored for queue operations. Intended for unit tests.
	Queue Queue

	// Registry holds the handler map. Required.
	Registry *Registry

	// Logger receives structured worker logs. Defaults to slog.Default()
	// when nil.
	Logger *slog.Logger

	// InstanceID identifies this worker process in the claimed_by column.
	// Operators can grep claimed_by to find the container that processed
	// (or got stuck on) a row. Defaults to a hostname-derived value.
	InstanceID string

	// PollInterval is the wait between empty-claim polls. Defaults to
	// 1 second — short enough to make tests responsive, long enough that
	// an idle worker does not burn CPU.
	PollInterval time.Duration

	// ShutdownTimeout bounds the graceful Stop path. After this duration
	// Stop returns even if a handler is still running. Defaults to 20s.
	ShutdownTimeout time.Duration
}

// Worker polls the worker_jobs table and dispatches claimed rows to the
// registered handlers.
type Worker struct {
	queue           Queue
	registry        *Registry
	logger          *slog.Logger
	instanceID      string
	pollInterval    time.Duration
	shutdownTimeout time.Duration

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// New constructs a Worker. Returns an error when required dependencies
// are missing — either a Queue or a non-nil Pool must be supplied, and
// the Registry must be non-nil.
func New(opts Options) (*Worker, error) {
	var q Queue
	if opts.Queue != nil {
		q = opts.Queue
	} else if opts.Pool != nil {
		q = NewPGQueue(opts.Pool.Pool)
	} else {
		return nil, errors.New("worker: either Pool or Queue is required")
	}
	if opts.Registry == nil {
		return nil, errors.New("worker: registry is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	instanceID := opts.InstanceID
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}

	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 20 * time.Second
	}

	return &Worker{
		queue:           q,
		registry:        opts.Registry,
		logger:          logger.With(slog.String("component", "worker"), slog.String("instance_id", instanceID)),
		instanceID:      instanceID,
		pollInterval:    pollInterval,
		shutdownTimeout: shutdownTimeout,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}, nil
}

// InstanceID returns the claimed_by value this worker stamps onto rows.
// Useful for tests that want to assert the column was populated correctly.
func (w *Worker) InstanceID() string { return w.instanceID }

// Run blocks until ctx is cancelled or Stop is called. Each loop iteration
// attempts to claim and execute one job; an empty queue triggers a
// pollInterval sleep before the next attempt.
//
// Run returns nil on clean shutdown. A non-nil return indicates a
// non-recoverable error — typically a pool that has been Closed.
func (w *Worker) Run(ctx context.Context) error {
	// Log "shutdown complete" and close doneCh together so Stop() never
	// sees doneCh closed before the final log line is written.
	defer func() {
		w.logger.Info("shutdown complete")
		close(w.doneCh)
	}()

	w.logger.Info("worker started",
		"poll_interval", w.pollInterval.String(),
		"shutdown_timeout", w.shutdownTimeout.String(),
	)

	for {
		// Honour both the parent context (signal-driven shutdown) and
		// the explicit Stop() channel before each claim attempt.
		select {
		case <-ctx.Done():
			w.logger.Info("worker context cancelled; exiting run loop")
			return nil
		case <-w.stopCh:
			w.logger.Info("worker stop requested; exiting run loop")
			return nil
		default:
		}

		job, err := w.queue.ClaimNext(ctx, w.instanceID)
		if err != nil {
			// Pool closed mid-poll, or context cancellation racing with
			// a query. Both are clean-shutdown signals.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			w.logger.Warn("claim next failed", "error", err.Error())
			if !w.waitOrStop(ctx, w.pollInterval) {
				return nil
			}
			continue
		}

		if job == nil {
			// Empty queue — back off for one poll interval.
			if !w.waitOrStop(ctx, w.pollInterval) {
				return nil
			}
			continue
		}

		w.logger.Info("job claimed",
			"job_id", job.ID,
			"job_type", job.Type,
			"attempt", job.Attempts,
			"max_attempts", job.MaxAttempts,
		)

		// shutdownWatcher: if Stop() or ctx cancellation fires while execute
		// is blocking, log "shutdown initiated" so operators know the worker
		// is draining the in-flight job rather than exiting immediately.
		// jobDone is always closed after execute returns, preventing leaks.
		jobDone := make(chan struct{})
		go func(j *Job) {
			select {
			case <-w.stopCh:
				select {
				case <-jobDone:
					// Job already finished — no drain message needed.
				default:
					w.logger.Info("shutdown initiated, finishing 1 claimed job",
						slog.String("job_id", j.ID),
						slog.String("job_type", j.Type),
					)
				}
			case <-ctx.Done():
				select {
				case <-jobDone:
					// Job already finished — no drain message needed.
				default:
					w.logger.Info("shutdown initiated, finishing 1 claimed job",
						slog.String("job_id", j.ID),
						slog.String("job_type", j.Type),
					)
				}
			case <-jobDone:
				// Job finished before any shutdown signal — nothing to log.
			}
		}(job)

		w.execute(ctx, job)
		close(jobDone) // Release the watcher goroutine.
	}
}

// Stop initiates a graceful shutdown. It returns nil when the Run loop
// has exited, or context.DeadlineExceeded if shutdownTimeout elapsed
// first. Stop is idempotent — calling it twice returns the same result
// each time without panicking.
func (w *Worker) Stop() error {
	w.stopOnce.Do(func() { close(w.stopCh) })

	select {
	case <-w.doneCh:
		// "shutdown complete" already logged by Run() via its defer.
		return nil
	case <-time.After(w.shutdownTimeout):
		w.logger.Warn("worker stop timed out",
			"timeout", w.shutdownTimeout.String(),
		)
		return context.DeadlineExceeded
	}
}

// execute runs the handler for a claimed job and writes the outcome back
// to the row. It never returns an error — failures are persisted on the
// row itself and surfaced through structured logs.
func (w *Worker) execute(ctx context.Context, job *Job) {
	handler, ok := w.registry.Lookup(job.Type)
	if !ok {
		w.logger.Error("no handler registered for job type",
			"job_id", job.ID,
			"job_type", job.Type,
		)
		w.markFailureOrRetry(ctx, job, fmt.Errorf("no handler registered for job_type %q", job.Type))
		return
	}

	// We deliberately do NOT impose a per-job timeout here: the handler
	// owns its own context discipline. This avoids the surprising case
	// where a long-running but legitimate handler gets cancelled mid-way
	// and produces a confusing duplicate-retry pattern.
	start := time.Now()
	handlerErr := safeHandler(ctx, handler, job.Payload)
	dur := time.Since(start)

	if handlerErr != nil {
		w.logger.Warn("handler returned error",
			"job_id", job.ID,
			"job_type", job.Type,
			"attempt", job.Attempts,
			"duration", dur.String(),
			"error", handlerErr.Error(),
		)
		w.markFailureOrRetry(ctx, job, handlerErr)
		return
	}

	// Use context.Background() so a SIGTERM-cancelled parent ctx does not
	// prevent persisting the completed status. The queue methods impose their
	// own 5–10 s timeouts internally.
	if err := w.queue.MarkDone(context.Background(), job.ID); err != nil {
		w.logger.Error("mark done failed",
			"job_id", job.ID,
			"error", err.Error(),
		)
		return
	}
	w.logger.Info("job completed",
		"job_id", job.ID,
		"job_type", job.Type,
		"duration", dur.String(),
	)
}

// safeHandler isolates a panicking handler so it cannot bring down the
// entire worker. A panic is translated to an error with the recovered
// value embedded — the retry/dead-letter machinery then handles it like
// any other failure.
func safeHandler(ctx context.Context, h HandlerFunc, payload []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	return h(ctx, payload)
}

// markFailureOrRetry either schedules a retry (status='pending') or
// finalises the row as failed (status='failed') and copies it into
// worker_dead_letter. The decision is based on attempts vs max_attempts.
func (w *Worker) markFailureOrRetry(ctx context.Context, job *Job, handlerErr error) {
	errText := truncate(handlerErr.Error(), 4000)

	// Use context.Background() for the same reason as MarkDone: a
	// SIGTERM-cancelled ctx must not prevent the outcome from being written.
	if job.Attempts < job.MaxAttempts {
		if err := w.queue.MarkRetry(context.Background(), job.ID, errText); err != nil {
			w.logger.Error("schedule retry failed",
				"job_id", job.ID,
				"error", err.Error(),
			)
		}
		return
	}

	// Final failure: move to dead letter.
	if err := w.queue.MarkFailed(context.Background(), job, errText); err != nil {
		w.logger.Error("mark failed failed",
			"job_id", job.ID,
			"error", err.Error(),
		)
		return
	}

	w.logger.Error("job moved to dead letter",
		"job_id", job.ID,
		"job_type", job.Type,
		"attempts", job.Attempts,
		"max_attempts", job.MaxAttempts,
		"last_error", errText,
	)
}

// waitOrStop sleeps for d, but returns early when ctx is cancelled or
// stopCh is closed. The bool result is true when the wait completed
// normally and false when shutdown was requested during the wait.
func (w *Worker) waitOrStop(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-w.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

// truncate clips s to at most n characters, with an ellipsis when truncated.
// Used so a verbose handler error cannot blow up the row's last_error
// column or saturate a single log record.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// EnqueuePayload is a small convenience for tests and ad-hoc producers:
// it INSERTs a single pending row using a payload that's pre-encoded as
// JSON.
//
// Production code is expected to INSERT directly inside the transaction
// that produced the side effect (the outbox pattern), so this helper is
// deliberately not part of the worker package's public API for the
// general case — it's only here to let integration tests stage rows
// without hand-rolling SQL.
func EnqueuePayload(
	ctx context.Context,
	pool *pgxpool.Pool,
	jobType string,
	payload any,
	maxAttempts int,
) (string, error) {
	if pool == nil {
		return "", errors.New("worker: nil pgxpool")
	}
	if jobType == "" {
		return "", errors.New("worker: job_type is required")
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now())
		RETURNING id::text
	`
	var id string
	if err := pool.QueryRow(ctx, insertSQL, jobType, body, maxAttempts).Scan(&id); err != nil {
		return "", fmt.Errorf("insert worker_job: %w", err)
	}
	return id, nil
}

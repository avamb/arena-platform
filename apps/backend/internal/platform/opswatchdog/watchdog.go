// Package opswatchdog implements the self-scheduling "ops.watchdog" worker
// job: cheap, indexed, bounded (LIMIT) read-only SQL checks over the sales
// path, run every 60s, that turn into Telegram alerts via
// internal/platform/opsalert.
//
// The watchdog is strictly READ-ONLY with respect to every business table
// (orders, tickets, payment_intents, checkout_sessions, refunds,
// worker_jobs, worker_dead_letter, outbox_events, delivery_jobs). The only
// tables it writes to are the two it owns (migration 0104):
// ops_watchdog_state (per-check cursors) and ops_alerts (dedup/lifecycle
// state for standing-condition alerts). It never sits on a money path —
// nothing in this package is called from any request handler or
// transaction that issues tickets, takes payment, or mutates inventory.
package opswatchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// JobType is the worker_jobs.job_type this package handles.
const JobType = "ops.watchdog"

// DefaultInterval is the gap between successive watchdog runs.
const DefaultInterval = 60 * time.Second

// Per-check thresholds. These are deliberately plain constants (not env
// vars) per the task's "env overrides only where cheap" guidance — none of
// these need to be tuned per deployment for the tiny sales volumes this
// watchdog was built for.
const (
	// paidNoTicketsAge is how long a paid order may sit without any issued
	// ticket before it is CRITICAL.
	paidNoTicketsAge = 3 * time.Minute
	// paymentNotCompletedAge is how long a succeeded payment intent may sit
	// without its checkout session reaching 'completed' before it is
	// CRITICAL.
	paymentNotCompletedAge = 3 * time.Minute
	// pendingJobLagThreshold is how old the oldest pending worker_jobs row
	// may be before the queue is considered stuck (WARN).
	pendingJobLagThreshold = 5 * time.Minute
	// outboxBacklogThreshold mirrors outbox.DefaultOutboxLagThreshold (kept
	// as a local constant to avoid an import solely for one number) — the
	// undelivered, non-dead-lettered outbox_events backlog size that trips
	// a WARN.
	outboxBacklogThreshold = int64(100)
	// digestThreshold: more than this many items from a single cursor-based
	// check in one run are batched into one digest message instead of one
	// message each.
	digestThreshold = 5
	// checkFailureAlertThreshold: this many CONSECUTIVE failed runs of the
	// same check raises a "watchdog check X failing" WARN meta-alert.
	checkFailureAlertThreshold = 3
	// checkRowLimit bounds every SQL check's LIMIT clause.
	checkRowLimit = 200
)

// Cursor keys (ops_watchdog_state.key).
const (
	cursorSalesFeed          = "sales_feed"
	cursorRefundsFeed        = "refunds_feed"
	cursorDeadLetterWorker   = "dead_letter_worker_jobs"
	cursorDeadLetterOutbox   = "dead_letter_outbox_events"
	cursorDeadLetterDelivery = "dead_letter_delivery_jobs"
	cursorHeartbeat          = "heartbeat"
)

// allCursorKeys is passed to EnsureInitialCursors at worker startup.
var allCursorKeys = []string{
	cursorSalesFeed, cursorRefundsFeed,
	cursorDeadLetterWorker, cursorDeadLetterOutbox, cursorDeadLetterDelivery,
	cursorHeartbeat,
}

// Alert fingerprint prefixes (ops_alerts.fingerprint = "<prefix>:<id>").
const (
	fpPaidNoTickets     = "paid_no_tickets"
	fpPaymentNotDone    = "payment_not_completed"
	fpManualReview      = "manual_review"
	fpLag               = "lag"
	fpWatchdogSelfCheck = "watchdog_check_failing"
)

// Scheduler enqueues the next watchdog run. Mirrors
// internal/platform/reservationexpiry's Scheduler contract.
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

func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("opswatchdog: schedule next run: %w", err)
	}
	return nil
}

// ScheduleInitialJob enqueues the first watchdog run at arena-worker startup
// unless one is already pending or claimed. Every later run is
// self-scheduled by the handler. Callers should also call
// EnsureInitialCursors before (or right after) this so the very first run
// never replays pre-existing sales/refunds/dead-letter history.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const checkSQL = `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("opswatchdog: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, JobType); err != nil {
		return fmt.Errorf("opswatchdog: enqueue initial job: %w", err)
	}
	return nil
}

// Options configures the handler returned by NewHandler.
type Options struct {
	// Pool is used for every read-only business-table check. Required.
	Pool *pgxpool.Pool
	// Notifier delivers alert/sale/digest messages. Required — pass
	// opsalert.New(...) which degrades to a safe logging no-op when
	// Telegram is not configured.
	Notifier opsalert.Notifier
	// AlertStore persists standing-condition alert state. nil uses
	// NewPGAlertStore(Pool).
	AlertStore AlertStore
	// CursorStore persists per-check cursors. nil uses
	// NewPGCursorStore(Pool).
	CursorStore CursorStore
	// Clock supplies "now". nil uses the real system clock.
	Clock clock.Clock
	// Logger receives one summary line per run plus a line per check
	// failure. nil uses slog.Default().
	Logger *slog.Logger
	// Interval is the gap between runs; defaults to DefaultInterval.
	Interval time.Duration
	// Scheduler enqueues the next run. nil disables self-scheduling (what
	// one-shot tests want).
	Scheduler Scheduler
	// HeartbeatHourUTC is the UTC hour (0-23) at which the once-daily
	// heartbeat digest fires. Defaults to 7.
	HeartbeatHourUTC int
	// RenotifyInterval overrides AlertEngine.RenotifyInterval (tests only).
	RenotifyInterval time.Duration
}

// runner holds the per-process state one watchdog handler closure needs:
// wired dependencies plus the in-memory consecutive-failure counters that
// drive the "watchdog check X failing" meta-alert. State does not need to
// survive a worker restart — losing it merely resets the failure count to
// zero, which just delays that one meta-alert by up to
// checkFailureAlertThreshold more runs.
type runner struct {
	pool             *pgxpool.Pool
	notifier         opsalert.Notifier
	engine           *AlertEngine
	cursors          CursorStore
	clk              clock.Clock
	logger           *slog.Logger
	heartbeatHourUTC int

	mu         sync.Mutex
	failCounts map[string]int
}

// NewHandler returns the handler for job_type=JobType. Its signature
// matches worker.HandlerFunc so it can be passed straight to
// (*worker.Registry).Register without this package importing worker.
//
// The handler always returns nil (success): a single check failing must
// never stop the others or disrupt the self-scheduling cadence (see the
// package doc and the "tolerate any single check failing" requirement) —
// failures are logged and, after checkFailureAlertThreshold consecutive
// misses, raised as their own WARN alert.
func NewHandler(opts Options) func(ctx context.Context, payload []byte) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Clock == nil {
		opts.Clock = clock.New()
	}
	if opts.AlertStore == nil {
		opts.AlertStore = NewPGAlertStore(opts.Pool)
	}
	if opts.CursorStore == nil {
		opts.CursorStore = NewPGCursorStore(opts.Pool)
	}
	r := &runner{
		pool:     opts.Pool,
		notifier: opts.Notifier,
		cursors:  opts.CursorStore,
		clk:      opts.Clock,
		logger:   opts.Logger,
		engine: &AlertEngine{
			Store:            opts.AlertStore,
			Notifier:         opts.Notifier,
			Clock:            opts.Clock,
			RenotifyInterval: opts.RenotifyInterval,
			Logger:           opts.Logger,
		},
		heartbeatHourUTC: opts.HeartbeatHourUTC,
		failCounts:       map[string]int{},
	}

	return func(ctx context.Context, _ []byte) error {
		r.runAll(ctx)

		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return fmt.Errorf("opswatchdog: schedule next run: %w", err)
			}
		}
		return nil
	}
}

// checkDef pairs a check's stable name with its runnable body.
type checkDef struct {
	name string
	run  func(ctx context.Context) error
}

// runAll executes every check, isolating failures from each other.
func (r *runner) runAll(ctx context.Context) {
	checks := []checkDef{
		{"sales_feed", r.checkSalesFeed},
		{"paid_no_tickets", r.checkPaidNoTickets},
		{"payment_not_completed", r.checkPaymentNotCompleted},
		{"manual_review", r.checkManualReview},
		{"dead_letters", r.checkDeadLetters},
		{"lag", r.checkLag},
		{"refunds_feed", r.checkRefundsFeed},
		{"heartbeat", r.checkHeartbeat},
	}
	for _, c := range checks {
		r.runOne(ctx, c.name, c.run)
	}
}

// runOne wraps a single check with panic recovery and the consecutive-
// failure meta-alert.
func (r *runner) runOne(ctx context.Context, name string, fn func(context.Context) error) {
	defer func() {
		if rec := recover(); rec != nil {
			r.recordFailure(ctx, name, fmt.Errorf("panic: %v", rec))
		}
	}()

	if err := fn(ctx); err != nil {
		r.recordFailure(ctx, name, err)
		return
	}
	r.recordSuccess(ctx, name)
}

func (r *runner) recordFailure(ctx context.Context, name string, err error) {
	r.logger.Warn("ops.watchdog: check failed", "check", name, "error", err.Error())

	r.mu.Lock()
	r.failCounts[name]++
	n := r.failCounts[name]
	r.mu.Unlock()

	if n < checkFailureAlertThreshold {
		return
	}
	scrubbed := opsalert.Truncate(opsalert.ScrubEmails(err.Error()), 200)
	_ = r.engine.Raise(ctx, AlertInput{
		Fingerprint: fpWatchdogSelfCheck + ":" + name,
		Severity:    "warn",
		Title:       fmt.Sprintf("watchdog check %q failing", name),
		Details: map[string]any{
			"consecutive_failures": n,
			"last_error":           scrubbed,
		},
	})
}

func (r *runner) recordSuccess(ctx context.Context, name string) {
	r.mu.Lock()
	hadAlerted := r.failCounts[name] >= checkFailureAlertThreshold
	r.failCounts[name] = 0
	r.mu.Unlock()

	if hadAlerted {
		_ = r.engine.Resolve(ctx, fpWatchdogSelfCheck+":"+name, fmt.Sprintf("watchdog check %q failing", name))
	}
}

// EnsureAllInitialCursors seeds a cursor_ts=now() row for every cursor-based
// check this package knows about. Call once at arena-worker startup,
// before (or alongside) ScheduleInitialJob, so the very first watchdog run
// never replays pre-existing sales/refunds/dead-letter history.
func EnsureAllInitialCursors(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	return EnsureInitialCursors(ctx, pool, allCursorKeys, now)
}

// SendStartupMessage sends the one-time "watchdog started" message. Called
// once from cmd/arena-worker at process start, never from the periodic
// handler (which would resend it every DefaultInterval).
func SendStartupMessage(ctx context.Context, notifier opsalert.Notifier, version, commit string) {
	_ = notifier.Send(ctx, formatStartupMessage(version, commit))
}

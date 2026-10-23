package credits

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ExpireJobType is the worker_jobs.job_type of the expiry pass.
const ExpireJobType = "billing.credit_expire"

// DefaultExpireInterval is the gap between expiry passes.
const DefaultExpireInterval = time.Hour

// expireBatch bounds one pass.
const expireBatch = 500

// Expirer takes what is left of expired bonus lots, one lot per transaction
// (41 section 6.5). The reminders 14 and 3 days before an expiry wait for
// their texts (stop condition ST-3).
type Expirer struct {
	pool *pgxpool.Pool
	now  func() time.Time
	log  *slog.Logger
}

// NewExpirer returns an Expirer; now defaults to time.Now.
func NewExpirer(pool *pgxpool.Pool, now func() time.Time, logger *slog.Logger) *Expirer {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Expirer{pool: pool, now: now, log: logger}
}

// Run expires every due lot and returns how many it emptied and the money
// they held, per currency.
func (e *Expirer) Run(ctx context.Context) (int, map[string]int64, error) {
	now := e.now()
	due, err := gen.New(e.pool).ListExpiringCreditLots(ctx, now, expireBatch)
	if err != nil {
		return 0, nil, err
	}
	n, sums := 0, map[string]int64{}
	for _, lot := range due {
		amount, err := e.expireOne(ctx, lot, now)
		if err != nil {
			return n, sums, fmt.Errorf("credits: expire lot %s: %w", lot.ID, err)
		}
		if amount > 0 {
			n++
			sums[lot.Currency] += amount
		}
	}
	return n, sums, nil
}

func (e *Expirer) expireOne(ctx context.Context, lot gen.CreditLotRow, now time.Time) (int64, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	amount, err := ExpireLot(ctx, gen.New(tx), lot.ID, now)
	if err != nil {
		return 0, err
	}
	return amount, tx.Commit(ctx)
}

// Scheduler enqueues the next pass.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext inserts a pending job due at `at` unless one is pending.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`
	if _, err := s.pool.Exec(ctx, q, ExpireJobType, at); err != nil {
		return fmt.Errorf("billing credit expire: schedule next pass: %w", err)
	}
	return nil
}

// ScheduleInitialJob seeds the first pass at worker start unless a job is
// already pending or running.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now() + interval '5 minutes'
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`
	if _, err := pool.Exec(ctx, q, ExpireJobType); err != nil {
		return fmt.Errorf("billing credit expire: enqueue initial job: %w", err)
	}
	return nil
}

// HandlerOptions configures NewExpireHandler.
type HandlerOptions struct {
	Expirer   *Expirer
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
}

// NewExpireHandler returns the handler of ExpireJobType. With no Expirer it
// does nothing and does not schedule itself. The next pass is scheduled
// before the work, so a failing pass does not end the chain.
func NewExpireHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	if opts.Interval <= 0 {
		opts.Interval = DefaultExpireInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		if opts.Expirer == nil {
			return nil
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return err
			}
		}
		n, sums, err := opts.Expirer.Run(ctx)
		if err != nil {
			opts.Logger.Error("billing credit expire failed", "error", err.Error())
			return err
		}
		if n > 0 {
			opts.Logger.Info("billing credit expire", "lots", n, "amounts_minor", sums)
		}
		return nil
	}
}

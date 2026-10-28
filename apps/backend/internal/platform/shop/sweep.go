package shop

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// JobType is the self-scheduling worker job that expires unpaid shop orders
// whose hold passed and puts their goods back on sale (spec 47 section 2).
const JobType = "shop.order.expire_sweep"

// SweepInterval is the cadence of JobType.
const SweepInterval = 30 * time.Second

// sweepBatch bounds one transaction; one run makes at most sweepMaxBatches.
const (
	sweepBatch      = 200
	sweepMaxBatches = 10
)

// ExpireDue expires at most one batch of due orders in one transaction and
// journals their releases; it returns how many it expired. A pending order
// whose payment already succeeded (the webhook is on its way) or is parked
// for review is never expired here.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	n := 0
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		due, err := q.ExpireShopOrders(ctx, sweepBatch)
		if err != nil {
			return err
		}
		for _, d := range due {
			if err := q.JournalShopOrderHold(ctx, d[0], d[1], MoveRelease); err != nil {
				return err
			}
		}
		n = len(due)
		return nil
	})
	return n, err
}

// Scheduler queues the next run of JobType.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler queues the next run in worker_jobs.
type PGScheduler struct{ db gen.DBTX }

// NewPGScheduler builds the scheduler over a pool.
func NewPGScheduler(db gen.DBTX) *PGScheduler { return &PGScheduler{db: db} }

// ScheduleNext queues one pending run at at.
func (p *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	if _, err := p.db.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)`, JobType, at); err != nil {
		return fmt.Errorf("shop: schedule next sweep: %w", err)
	}
	return nil
}

// ScheduleInitialJob queues the first run when none is pending or claimed, so
// the self-scheduling chain starts once per deployment.
func ScheduleInitialJob(ctx context.Context, db gen.DBTX) error {
	var n int64
	if err := db.QueryRow(ctx, `SELECT count(*) FROM worker_jobs
		WHERE job_type = $1 AND status IN ('pending', 'claimed')`, JobType).Scan(&n); err != nil {
		return fmt.Errorf("shop: check initial sweep job: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType); err != nil {
		return fmt.Errorf("shop: enqueue initial sweep job: %w", err)
	}
	return nil
}

// SweepOptions wires the worker handler.
type SweepOptions struct {
	Service   *Service
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
}

// NewSweepHandler is the worker handler of JobType: it expires due orders in
// batches, then queues its own next run Interval later.
func NewSweepHandler(opts SweepOptions) func(ctx context.Context, payload []byte) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = SweepInterval
	}
	return func(ctx context.Context, _ []byte) error {
		total := 0
		for i := 0; i < sweepMaxBatches; i++ {
			n, err := opts.Service.ExpireDue(ctx)
			if err != nil {
				return fmt.Errorf("shop: expire sweep: %w", err)
			}
			total += n
			if n < sweepBatch || ctx.Err() != nil {
				break
			}
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return err
			}
		}
		if total > 0 {
			opts.Logger.Info("shop order expire sweep complete", "expired", total)
		}
		return nil
	}
}

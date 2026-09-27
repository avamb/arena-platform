// Package tierchain runs tier.chain_sweep: when a category's sale window
// closes, its free places move to the next category of its chain and that
// category opens (migration 0112).
//
// Organizers sell one hall through a chain of price categories — "Early
// bird" until one date, "Friends" until the next, "Last minute" after that.
// Before this job the switch had to be done by hand at midnight (the Vino&Co
// Ashdod session of 29.10 was switched by a one-off script on a systemd
// timer). The move itself is gaquota.HandOver; this package only finds the
// due links and runs each in its own retried transaction.
//
// The job is self-scheduling like reservation.expire_sweep: each run enqueues
// the next through worker_jobs, and ScheduleInitialJob seeds the queue at
// worker start-up.
package tierchain

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

// JobType is the worker_jobs.job_type this package handles.
const JobType = "tier.chain_sweep"

// DefaultInterval is the gap between sweeps. A category switch is announced
// to the minute ("from 1 October"), so a minute of lag is the budget.
const DefaultInterval = 30 * time.Second

// DefaultBatchSize caps the links handled by one run; the rest wait for the
// next run 30 s later.
const DefaultBatchSize = 200

// Store is what the handler needs from the database.
type Store interface {
	// DueHandOvers lists the links whose source window has closed and that
	// still own a movable free place.
	DueHandOvers(ctx context.Context, limit int32) ([]gen.TierChainRow, error)
	// HandOver moves one link's places in its own transaction.
	HandOver(ctx context.Context, link gen.TierChainRow) (gaquota.HandOverResult, error)
}

// PGStore is the production Store.
type PGStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPGStore wraps a pgx pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, q: gen.New(pool)}
}

// DueHandOvers implements Store.
func (s *PGStore) DueHandOvers(ctx context.Context, limit int32) ([]gen.TierChainRow, error) {
	return s.q.ListDueTierHandOvers(ctx, limit)
}

// HandOver implements Store: one transaction per link, retried on a
// deadlock / serialization failure like every other quota mutation.
func (s *PGStore) HandOver(ctx context.Context, link gen.TierChainRow) (gaquota.HandOverResult, error) {
	var res gaquota.HandOverResult
	err := gaquota.InTx(ctx, s.pool, s.q, func(txq *gen.Queries) error {
		r, err := gaquota.HandOver(ctx, txq, link)
		res = r
		return err
	})
	return res, err
}

// Scheduler enqueues the next sweep run.
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

// ScheduleNext implements Scheduler.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("tierchain: schedule next sweep: %w", err)
	}
	return nil
}

// Options configures the handler returned by NewHandler.
type Options struct {
	Store     Store // required
	Logger    *slog.Logger
	Interval  time.Duration
	BatchSize int32
	// Scheduler enqueues the next run; nil disables self-scheduling.
	Scheduler Scheduler
}

// NewHandler returns the handler for job_type=JobType. Its signature matches
// worker.HandlerFunc.
//
// One failing link never blocks the others: its error is logged, the rest of
// the batch runs, and the link is picked up again by the next sweep (it
// still owns free places, so it is still due).
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
		links, err := opts.Store.DueHandOvers(ctx, opts.BatchSize)
		if err != nil {
			return fmt.Errorf("tierchain: list due hand-overs: %w", err)
		}
		moved, failed := 0, 0
		for _, link := range links {
			res, err := opts.Store.HandOver(ctx, link)
			if err != nil {
				failed++
				opts.Logger.Error("tier chain hand-over failed",
					"session_id", link.SessionID.String(),
					"from_tier", link.TierID.String(),
					"to_tier", link.NextTierID.String(),
					"error", err.Error())
				continue
			}
			moved++
			opts.Logger.Info("tier chain hand-over",
				"session_id", res.SessionID.String(),
				"from_tier", res.FromTier.String(),
				"to_tier", res.ToTier.String(),
				"places", res.Moved,
				"first", res.First)
		}

		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return fmt.Errorf("tierchain: schedule next run: %w", err)
			}
		}
		if len(links) > 0 {
			opts.Logger.Info("tier chain sweep complete", "handed_over", moved, "failed", failed)
		}
		return nil
	}
}

// ScheduleInitialJob enqueues the first sweep at arena-worker start-up unless
// one is already pending or claimed.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const checkSQL = `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("tierchain: check initial sweep job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, JobType); err != nil {
		return fmt.Errorf("tierchain: enqueue initial sweep job: %w", err)
	}
	return nil
}

// job_retention.go — worker_jobs.cleanup (internal/platform/jobretention):
// the self-scheduling job that deletes finished worker_jobs rows after their
// retention period, so the queue table does not grow without bound.
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/jobretention"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerJobRetentionHandler registers worker_jobs.cleanup.
func registerJobRetentionHandler(reg *worker.Registry, pool *pgxpool.Pool, logger *slog.Logger) {
	reg.Register(jobretention.JobType, jobretention.NewHandler(jobretention.Options{
		Store:     jobretention.NewPGStore(pool),
		Scheduler: jobretention.NewPGScheduler(pool),
		Logger:    logger,
		// promo_code_attempts older than 90 days (migration 0166) and guest
		// list deliveries whose moment is older than 90 days (migration 0188).
		Extra: func(ctx context.Context, now time.Time) (string, int64, error) {
			n, err := promoattempts.Purge(ctx, pool, now)
			if err != nil {
				return "promo_code_attempts", n, err
			}
			m, err := gen.New(pool).PurgeGuestListDeliveries(ctx, now)
			return "promo_code_attempts+guest_list_deliveries", n + m, err
		},
	}))
}

// scheduleJobRetention seeds the first worker_jobs.cleanup run at startup.
// Non-fatal: a delayed run only means finished rows stay a little longer.
func scheduleJobRetention(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if err := jobretention.ScheduleInitialJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial worker jobs cleanup job", "error", err.Error())
	}
}

// analytics_jobs.go — analytics.partitions (internal/platform/analytics, spec
// 50 section 6): the self-scheduling job that keeps three future monthly
// partitions of analytics.events ready and drops the ones older than 13
// months. It writes only the analytics schema's own partitions.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

func registerAnalyticsHandlers(reg *worker.Registry, pool *pgxpool.Pool, logger *slog.Logger) {
	reg.Register(analytics.PartitionsJobType, analytics.NewPartitionsHandler(analytics.PartitionsOptions{
		DB:        pool,
		Scheduler: analytics.NewPGPartitionsScheduler(pool),
		Logger:    logger,
	}))
}

func scheduleAnalyticsJobs(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	// Non-fatal: the migration created the current month and three more, so a
	// delayed first run costs nothing for months.
	if err := analytics.SchedulePartitionsJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial analytics partitions job", "error", err.Error())
	}
}

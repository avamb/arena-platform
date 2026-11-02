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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

func registerAnalyticsHandlers(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, logger *slog.Logger) {
	reg.Register(analytics.PartitionsJobType, analytics.NewPartitionsHandler(analytics.PartitionsOptions{
		DB:        pool,
		Scheduler: analytics.NewPGPartitionsScheduler(pool),
		Logger:    logger,
	}))
	// analytics.rollup (stage A5) reads raw events and the business tables and
	// writes only the analytics schema. TGHashSecret is ANALYTICS_TG_HASH_SECRET
	// (the same value as on arena-bot): without it the bot-onboarding join (tg
	// and e-mail) is skipped, everything else runs.
	if cfg.AnalyticsTGHashSecret == "" {
		logger.Warn("ANALYTICS_TG_HASH_SECRET is not set: the analytics rollup skips the Telegram join")
	}
	reg.Register(analytics.RollupJobType, analytics.NewRollupHandler(analytics.RollupHandlerOptions{
		Rollup: analytics.NewRollup(analytics.RollupOptions{
			Pool: pool, TGHashSecret: cfg.AnalyticsTGHashSecret, Logger: logger,
		}),
		Scheduler: analytics.NewPGRollupScheduler(pool),
		Logger:    logger,
		Failures:  metrics.AnalyticsRollupFailedTotal,
	}))
}

func scheduleAnalyticsJobs(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	// Non-fatal: the migration created the current month and three more, so a
	// delayed first run costs nothing for months.
	if err := analytics.SchedulePartitionsJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial analytics partitions job", "error", err.Error())
	}
	if err := analytics.ScheduleRollupJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial analytics rollup job", "error", err.Error())
	}
}

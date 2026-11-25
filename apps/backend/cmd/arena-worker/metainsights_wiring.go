package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metainsights"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// buildMetaInsights builds the Meta insights syncer (spec 66 section 5), or
// nil when the Meta connection is off (cfg.MetaEnabled false or a token key
// that does not open): then neither job is registered nor seeded.
func buildMetaInsights(pool *pgxpool.Pool, cfg *config.Config, notifier *salesnotify.Dispatcher, logger *slog.Logger) *metainsights.Syncer {
	box := buildMetaTokenBox(cfg, logger)
	if box == nil {
		return nil
	}
	var announcer metainsights.Announcer
	if notifier != nil {
		announcer = notifier
	}
	return metainsights.New(metainsights.Options{
		Pool:      pool,
		Box:       box,
		Client:    metainsights.NewClient(cfg.MetaGraphBaseURL, cfg.MetaGraphVersion, nil),
		Announcer: announcer,
		Logger:    logger,
		Every:     cfg.MetaInsightsEvery,

		EventCenterURL: cfg.EventCenterPublicURL,
		OwnerTelegram:  cfg.EventsTelegramBotToken != "",
	})
}

// registerMetaInsights registers meta.insights_sweep and meta.insights_sync
// when the syncer exists.
func registerMetaInsights(reg *worker.Registry, s *metainsights.Syncer, cfg *config.Config, logger *slog.Logger) {
	if s == nil {
		logger.Info("meta insights sync is off: the Meta connection is not configured")
		return
	}
	reg.Register(metainsights.SweepJobType, s.NewSweepHandler())
	reg.Register(metainsights.SyncJobType, s.NewSyncHandler())
	logger.Info("meta insights sync registered",
		"interval", metainsights.DefaultSweepInterval.String(), "every", cfg.MetaInsightsEvery.String())
}

// scheduleMetaInsights seeds the sweep chain at worker start.
func scheduleMetaInsights(ctx context.Context, pool *pgxpool.Pool, s *metainsights.Syncer, logger *slog.Logger) {
	if s == nil {
		return
	}
	if err := metainsights.ScheduleInitialJob(ctx, pool); err != nil {
		// Non-fatal: the ad spend is read at the next worker start.
		logger.Warn("could not schedule initial meta insights sweep job", "error", err.Error())
	}
}

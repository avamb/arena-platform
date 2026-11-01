package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerSalesAlertWatch registers the self-scheduling sales_state.watch job
// (internal/platform/salesalert): it tells the organization's own sales chats
// when a session sold out and when the WordPress sites did not take a sales
// state change within 15 minutes, and queues the e-mail
// (sales_state.alert_email, registered with the other delivery handlers). It
// always registers; without SALES_TELEGRAM_BOT_TOKEN the chats get nothing
// and the e-mail still goes out.
func registerSalesAlertWatch(reg *worker.Registry, pool *pgxpool.Pool, notifier *salesnotify.Dispatcher, logger *slog.Logger) {
	var announcer salesalert.Announcer
	if notifier != nil {
		announcer = notifier
	}
	reg.Register(salesalert.JobType, salesalert.NewHandler(salesalert.Options{
		Pool:      pool,
		Announcer: announcer,
		Logger:    logger,
		Interval:  salesalert.DefaultInterval,
	}))
	logger.Info("sales state watch registered", "interval", salesalert.DefaultInterval.String())
}

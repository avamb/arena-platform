package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	billingreconcile "github.com/abhteam/arena_new/apps/backend/internal/platform/billing/reconcile"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerBillingReconcileHandler registers the daily billing.reconcile job
// (task B2-5). Like billing.collect it works only once the owner names the
// date X (BILLING_COLLECT_SINCE): without it the handler is inert and never
// schedules itself.
func registerBillingReconcileHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, notifier opsalert.Notifier, logger *slog.Logger) {
	var rec *billingreconcile.Reconciler
	if !cfg.BillingCollectSince.IsZero() {
		rec = billingreconcile.New(pool, billingreconcile.Options{
			Since:   cfg.BillingCollectSince,
			Metrics: metrics,
			Alerter: &opswatchdog.AlertEngine{
				Store:    opswatchdog.NewPGAlertStore(pool),
				Notifier: notifier,
				Clock:    clock.New(),
				Logger:   logger,
			},
			Logger: logger,
		})
	}
	reg.Register(billingreconcile.JobType, billingreconcile.NewHandler(billingreconcile.HandlerOptions{
		Reconciler: rec,
		Scheduler:  billingreconcile.NewPGScheduler(pool),
		Logger:     logger,
	}))
}

// scheduleBillingReconcile seeds the first reconcile at start-up. Non-fatal:
// a late first run only delays the first report.
func scheduleBillingReconcile(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) {
	if cfg.BillingCollectSince.IsZero() {
		return
	}
	if err := billingreconcile.ScheduleInitialJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial billing reconcile job", "error", err.Error())
	}
}

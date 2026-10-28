// shop_sweep.go — shop.order.expire_sweep (internal/platform/shop, spec
// 08_architecture/47 section 2): the self-scheduling job that expires unpaid
// shop orders whose hold passed and puts their goods back on sale, every 30
// seconds like reservation.expire_sweep. It also registers shop.order_email.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerShopSweepHandler registers shop.order.expire_sweep.
func registerShopSweepHandler(reg *worker.Registry, pool *pgxpool.Pool, logger *slog.Logger) {
	reg.Register(shop.JobType, shop.NewSweepHandler(shop.SweepOptions{
		Service:   shop.New(pool, logger),
		Scheduler: shop.NewPGScheduler(pool),
		Logger:    logger,
	}))
}

// scheduleShopSweep seeds the first run at start-up. Non-fatal: a late first
// pass only keeps an abandoned cart's goods held a little longer.
func scheduleShopSweep(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if err := shop.ScheduleInitialJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial shop order expire sweep job", "error", err.Error())
	}
}

// registerShopEmailHandler registers shop.order_email, the buyer's letter of a
// merchandise shop order. It shares the sender, media store and templates of
// ticket.deliver (deliveryOpts), so it is built from the same options.
func registerShopEmailHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, deliveryOpts delivery.HandlerOptions) {
	reg.Register(shop.JobTypeOrderEmail, delivery.NewShopEmailHandler(delivery.ShopEmailOptions{
		HandlerOptions: deliveryOpts,
		DB:             pool,
		ShopBaseURL:    cfg.ShopPublicBaseURL,
	}))
}

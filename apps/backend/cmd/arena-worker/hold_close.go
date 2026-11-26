// hold_close.go — hold.close_payment and hold.visitor_hash_purge (spec
// 08_architecture/67 sections 5.2 and 6.1, internal/platform/holdclose).
// The first closes the provider payment page of an expired seat hold before
// its seats go back on sale; the second erases the visitor hashes of holds
// closed more than 24 hours ago.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdclose"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// pageClosingProviders are the payment modules whose checkout page arena can
// read and close (Capabilities.CanExpirePage; Stripe: the embedded and the
// hosted Checkout Session). The read and the close go through the module's
// embedded-checkout interface, so it is required as well.
func pageClosingProviders() []string {
	return paymodules.Registry().NamesWhere(func(d payments.Descriptor) bool {
		return d.Capabilities.CanExpirePage && d.Capabilities.EmbeddedCheckout
	})
}

// registerHoldCloseHandlers registers hold.close_payment and
// hold.visitor_hash_purge. The provider is reached through the same hosted
// payment starter checkout.payment_recover builds (per-organization module
// keys, the Stripe test seam).
func registerHoldCloseHandlers(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, logger *slog.Logger) {
	q := gen.New(pool)
	starter := hfeed.NewStripePaymentStarter(q, q, cfg.StripeAPIBaseURL)
	m := holdclose.NewMetrics()
	if metrics != nil {
		metrics.Registry().MustRegister(m.Collectors()...)
	}
	proc := holdclose.NewProcessor(holdclose.ProcessorOptions{
		Store:     holdclose.NewPGStore(pool),
		Closer:    starter,
		Providers: pageClosingProviders(),
		Metrics:   m,
		Logger:    logger,
	})
	reg.Register(holdclose.JobType, holdclose.NewHandler(holdclose.HandlerOptions{
		Processor: proc,
		Scheduler: holdclose.NewPGScheduler(pool, holdclose.JobType),
		Logger:    logger,
	}))
	reg.Register(holdclose.PurgeJobType, holdclose.NewPurgeHandler(holdclose.PurgeOptions{
		Purger:    q,
		Scheduler: holdclose.NewPGScheduler(pool, holdclose.PurgeJobType),
		Logger:    logger,
	}))
}

// scheduleHoldClose seeds the first run of both jobs. Non-fatal: without it
// the reservation sweep still releases an expired hold after its hard limit.
func scheduleHoldClose(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	for _, jt := range []string{holdclose.JobType, holdclose.PurgeJobType} {
		if err := holdclose.ScheduleInitialJob(ctx, pool, jt); err != nil {
			logger.Warn("could not schedule initial hold job", "job_type", jt, "error", err.Error())
		}
	}
}

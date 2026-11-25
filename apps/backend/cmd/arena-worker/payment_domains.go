// payment_domains.go — the wallet-domain jobs (internal/platform/paymentdomains,
// spec 65 section 5): payment_domains.sync registers the domains of one
// organization in its Stripe account, payment_domains.sweep does it for all
// and schedules itself every 24 hours.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentdomains"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

func buildPaymentDomainsSyncer(pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) *paymentdomains.Syncer {
	return &paymentdomains.Syncer{
		Queries:         gen.New(pool),
		Options:         payments.Options{StripeAPIBaseURL: cfg.StripeAPIBaseURL},
		PlatformDomains: cfg.WalletPlatformDomains(),
		Logger:          logger,
	}
}

// registerPaymentDomainsHandlers registers both wallet-domain jobs.
func registerPaymentDomainsHandlers(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) {
	buildPaymentDomainsSyncer(pool, cfg, logger).Register(reg)
}

// schedulePaymentDomainsSweep seeds the first sweep, which also catches up
// configurations that existed before the feature. Non-fatal.
func schedulePaymentDomainsSweep(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) {
	if err := buildPaymentDomainsSyncer(pool, cfg, logger).ScheduleSweep(ctx); err != nil {
		logger.Warn("could not schedule initial payment domains sweep job", "error", err.Error())
	}
}

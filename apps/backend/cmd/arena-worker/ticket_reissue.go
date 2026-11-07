package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ticketreissue"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerTicketReissueHandler registers reissue.provider_email (spec 63,
// decision R-17): after a reissue moved the order to the buyer's real
// address, the provider's receipt address follows (Stripe). It runs outside
// the reissue's transactions, so a provider failure never undoes it.
func registerTicketReissueHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) {
	q := gen.New(pool)
	reg.Register(ticketreissue.ProviderEmailJobType, ticketreissue.NewProviderEmailHandler(ticketreissue.ProviderJobOptions{
		DB: pool,
		Modules: hcheckout.NewRefundModuleSource(q, payments.Options{
			StripeAPIBaseURL:  cfg.StripeAPIBaseURL,
			FlittAPIBaseURL:   cfg.FlittAPIBaseURL,
			AllPayAPIBaseURL:  cfg.AllPayAPIBaseURL,
			PayPlusAPIBaseURL: cfg.PayPlusAPIBaseURL,
		}),
		Logger: logger,
	}))
}

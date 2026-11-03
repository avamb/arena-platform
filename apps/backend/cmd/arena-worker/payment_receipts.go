package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentreceipts"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerPaymentReceiptHandler registers payment.receipt_archive (spec 51
// section 2): the copy of the provider's receipt of a paid payment. Without a
// media store the link and number are still stored, the copy is not kept.
func registerPaymentReceiptHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, repo *mediastore.Repo, logger *slog.Logger) {
	q := gen.New(pool)
	var store paymentreceipts.Store
	if repo != nil {
		store = paymentreceipts.MediaStore{Repo: repo}
	}
	reg.Register(paymentreceipts.JobType, paymentreceipts.NewHandler(paymentreceipts.Options{
		Queries: q,
		Modules: hcheckout.NewRefundModuleSource(q, payments.Options{
			StripeAPIBaseURL:  cfg.StripeAPIBaseURL,
			FlittAPIBaseURL:   cfg.FlittAPIBaseURL,
			AllPayAPIBaseURL:  cfg.AllPayAPIBaseURL,
			PayPlusAPIBaseURL: cfg.PayPlusAPIBaseURL,
		}),
		Store:  store,
		Logger: logger,
	}))
}

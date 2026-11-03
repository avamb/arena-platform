// disputes.go — the dispute jobs (internal/platform/disputes, spec 51.3):
// dispute.build_package collects the facts and renders the evidence
// package (and saves the provider draft, never the final answer),
// dispute.send_email sends an answer the organizer approved to a provider
// that takes disputes by e-mail, and dispute.watch tells the organization
// about new disputes and coming deadlines.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

func buildDisputeService(pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) *disputes.Service {
	return disputes.New(disputes.Options{
		DB: pool,
		Modules: hcheckout.NewRefundModuleSource(gen.New(pool), payments.Options{
			StripeAPIBaseURL:  cfg.StripeAPIBaseURL,
			FlittAPIBaseURL:   cfg.FlittAPIBaseURL,
			AllPayAPIBaseURL:  cfg.AllPayAPIBaseURL,
			PayPlusAPIBaseURL: cfg.PayPlusAPIBaseURL,
		}),
		Logger: logger,
	})
}

// registerDisputeHandlers registers the three dispute jobs.
func registerDisputeHandlers(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, chats *salesnotify.Dispatcher, ops opsalert.Notifier, sender email.Sender, logger *slog.Logger) {
	svc := buildDisputeService(pool, cfg, logger)
	reg.Register(disputes.JobBuildPackage, svc.BuildJobHandler())
	reg.Register(disputes.JobSendEmail, svc.EmailJobHandler(sender))
	n := disputes.Notifier{Ops: ops, Sender: sender, EventCenterURL: cfg.EventCenterPublicURL}
	if chats != nil {
		n.Announcer = chats
	}
	reg.Register(disputes.JobWatch, svc.WatchHandler(n))
}

// scheduleDisputeWatch seeds the first dispute.watch. Non-fatal: a late
// first pass only delays a notice.
func scheduleDisputeWatch(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if err := disputes.ScheduleWatch(ctx, pool); err != nil {
		logger.Warn("could not schedule initial dispute watch job", "error", err.Error())
	}
}

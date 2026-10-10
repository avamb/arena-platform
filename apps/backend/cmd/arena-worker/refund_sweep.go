// refund_sweep.go — refund.sweep (internal/platform/refunds, PAY-03): the
// self-scheduling job that retries refund calls whose outcome is unknown,
// reads pending refunds back from their provider, parks refunds stuck for
// 24 hours in manual_review with an ops alert, and cancels the ticket of an
// accepted refund when a crash left it active.
//
// The engine here is the same one arena-api builds (httpserver/
// refund_engine_shims.go): payment modules from the registry and each
// organization's own payment config, ticket cancellation through the
// AB-49 transaction, v1.ticket.cancelled and v1.ticket.refunded through the
// outbox.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hscanner"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// buildRefundEngine assembles the worker's refund engine.
func buildRefundEngine(pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, logger *slog.Logger) *refunds.Engine {
	q := gen.New(pool)
	auditWriter := audit.NewPGWriter(pool)
	scanner := hscanner.New(q, q, pool, outbox.NewPGEventsWriter(pool), nil, logger)
	tickets := htickets.New(
		q,    // ticketQ
		q,    // credentialQ — revoked on cancellation
		nil,  // complimentaryQ
		q,    // inventoryQ — capacity restore on cancellation
		q,    // reservationQ
		q,    // barcodeQ — revoked on cancellation
		nil,  // deliveryJobQ
		nil,  // feedTokenQ
		pool, // workerPool
		pool, // pool (TxStarter)
		auditWriter,
		logger,
		nil, // publishTicketIssuedEvents
		nil, // publishTicketRevokedV1Events
		scanner.PublishTicketCancelledEvent,
	)
	return refunds.New(refunds.Options{
		DB: pool,
		Modules: hcheckout.NewRefundModuleSource(q, payments.Options{
			StripeAPIBaseURL: cfg.StripeAPIBaseURL,
			FlittAPIBaseURL:  cfg.FlittAPIBaseURL,
		}),
		CancelTicket:    tickets.RefundCanceller(),
		PublishRefunded: scanner.PublishTicketRefundedV1Events,
		Audit:           auditWriter,
		Metrics:         metrics,
		Logger:          logger,
	})
}

// registerRefundSweepHandler registers refund.sweep.
func registerRefundSweepHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, notifier opsalert.Notifier, logger *slog.Logger) {
	reg.Register(refunds.JobType, refunds.NewSweepHandler(refunds.SweepOptions{
		Engine:    buildRefundEngine(pool, cfg, metrics, logger),
		Notifier:  notifier,
		Scheduler: refunds.NewPGScheduler(pool),
		Logger:    logger,
	}))
}

// scheduleRefundSweep seeds the first refund.sweep at start-up. Non-fatal:
// a late first pass only delays retries and lookups.
func scheduleRefundSweep(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if err := refunds.ScheduleInitialJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial refund sweep job", "error", err.Error())
	}
}

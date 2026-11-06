// checkout_payment_recover.go — checkout.payment_recover (П-19,
// internal/platform/paymentrecover): settles hosted payment attempts recorded
// before the provider call that never got their payment page. A paid one goes
// through the payment webhook's own state machine; an unpaid or unknown one
// gets its page asked for again under the same reference (AllPay order_id).
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentrecover"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// recoverProcessor adapts hfeed.PaymentRecoverer to paymentrecover.Processor.
type recoverProcessor struct{ r *hfeed.PaymentRecoverer }

func (p recoverProcessor) RecoverStalledPayments(ctx context.Context, limit int32) (paymentrecover.Stats, error) {
	s, err := p.r.RecoverStalledPayments(ctx, limit)
	return paymentrecover.Stats{
		Examined: s.Examined, Applied: s.Applied, Relinked: s.Relinked,
		Pending: s.Pending, TooLate: s.TooLate, Failed: s.Failed,
	}, err
}

// buildPaymentRecoverer assembles the recovery from the same pieces arena-api
// uses: the hosted payment starter (per-organization credentials, provider
// test seams, the per-config callback URL) and the payment webhook handler.
func buildPaymentRecoverer(pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) *hfeed.PaymentRecoverer {
	q := gen.New(pool)
	starter := hfeed.NewStripePaymentStarter(q, q, cfg.StripeAPIBaseURL).
		WithFlitt(cfg.FlittAPIBaseURL, cfg.APIPublicBaseURL()).
		WithAllPay(cfg.AllPayAPIBaseURL).
		WithPayPlus(cfg.PayPlusAPIBaseURL)
	// Only what processPaymentWebhook touches is wired: checkout, payment
	// intents, the reservation conversion (reservations + inventory) and
	// the pool. Ticket issuance is a worker job the webhook enqueues.
	webhook := hcheckout.New(
		q, q, q, q, // checkoutQ, reservationQ, inventoryQ, paymentIntentQ
		nil, nil, nil, nil, nil, nil, // refundQ, promoQ, tierQ, ticketQ, channelQ, orgQ
		pool,
		logger,
		hcheckout.PricingRules{},
		"", "", // legacy-route webhook secrets: unused here
		nil, nil, nil,
	)
	return &hfeed.PaymentRecoverer{
		Store:    q,
		Provider: starter,
		Apply: func(ctx context.Context, src hcheckout.ConfirmedPaymentSource, ev payments.NormalizedEvent) error {
			_, _, err := webhook.ApplyProviderConfirmedPayment(ctx, src, ev)
			return err
		},
		ReturnURLs: httpserver.CheckoutReturnURLPolicy(cfg),
		Grace:      httpserver.WidgetPaymentGrace(cfg),
		Logger:     logger,
	}
}

// registerPaymentRecoverHandler registers checkout.payment_recover.
func registerPaymentRecoverHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) {
	reg.Register(paymentrecover.JobType, paymentrecover.NewHandler(paymentrecover.Options{
		Processor: recoverProcessor{r: buildPaymentRecoverer(pool, cfg, logger)},
		Scheduler: paymentrecover.NewPGScheduler(pool),
		Logger:    logger,
	}))
}

// schedulePaymentRecover seeds the first run at start-up. Non-fatal: the
// webhook still settles every attempt whose page the buyer reached.
func schedulePaymentRecover(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if err := paymentrecover.ScheduleInitialJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial checkout payment recovery job", "error", err.Error())
	}
}

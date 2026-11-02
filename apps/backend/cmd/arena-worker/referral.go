// referral.go — the "Refer a friend" jobs (internal/platform/referral, spec 52):
// referral.payout pays one cashback as a partial refund through the refund
// engine, referral.payout_sweep is the self-scheduling safety net that
// requeues, reads back, records friends who took their money back, and
// switches off dead codes.
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerReferralHandlers registers referral.payout and referral.payout_sweep.
// The two letters (referral.cashback_email, referral.reversal_email) are
// registered with the other delivery handlers.
// The engine is the same one refund.sweep drives.
func registerReferralHandlers(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, notifier *salesnotify.Dispatcher, logger *slog.Logger) {
	// The organizer notice (spec 52 section 5.2): the sales chats and a letter
	// to the owners. Without the sales bot the chats get nothing and the
	// letter still goes out.
	var announcer referral.OrgAnnouncer
	if notifier != nil {
		announcer = notifier
	}
	svc := referral.NewService(referral.Options{
		Pool:     pool,
		Engine:   buildRefundEngine(pool, cfg, metrics, logger),
		Notifier: referral.NewOrganizerNotifier(pool, announcer, logger),
		Logger:   logger,
	})
	reg.Register(referral.JobTypePayout, referral.NewPayoutHandler(svc))
	reg.Register(referral.JobTypeSweep, referral.NewSweepHandler(svc, referral.NewPGScheduler(pool)))
}

// scheduleReferralSweep seeds the first sweep at start-up. Non-fatal: a late
// first pass only delays the safety net, every payout has its own job.
func scheduleReferralSweep(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if err := referral.ScheduleInitialJob(ctx, pool); err != nil {
		logger.Warn("could not schedule initial referral sweep job", "error", err.Error())
	}
}

package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// registerGuestListSchedule registers the self-scheduling guest_list.schedule
// job (internal/platform/guestlist): every minute it writes the guest lists
// whose moment came (24 h, 3 h before the start, doors open) for the members
// who turned them on, and mails the ones of members who are not linked to the
// event center bot. The bot process sends the others to private chats.
func registerGuestListSchedule(reg *worker.Registry, pool *pgxpool.Pool, sender email.Sender, logger *slog.Logger) {
	reg.Register(guestlist.JobType, guestlist.NewHandler(guestlist.Options{
		Pool:     pool,
		Sender:   sender,
		Logger:   logger,
		Interval: guestlist.DefaultInterval,
	}))
	logger.Info("guest list schedule registered", "interval", guestlist.DefaultInterval.String())
}

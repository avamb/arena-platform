package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlistteam"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
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

// registerGuestListTeamSend registers guest_list.team_send
// (internal/platform/guestlistteam): every 5 minutes it sends the current
// guest list of a session 24 h and 3 h before the start to the organization's
// owners and managers by e-mail and to its own sales Telegram groups (through
// the SALES bot; without SALES_TELEGRAM_BOT_TOKEN only e-mail goes).
func registerGuestListTeamSend(reg *worker.Registry, pool *pgxpool.Pool, sender email.Sender, cfg *config.Config, logger *slog.Logger) {
	o := guestlistteam.Options{
		Pool:     pool,
		Sender:   sender,
		Chats:    salesnotify.NewPGStore(pool),
		Logger:   logger,
		Interval: guestlistteam.DefaultInterval,
	}
	if cfg.SalesTelegramBotToken != "" {
		o.Docs = salesnotify.NewTelegramSender(cfg.SalesTelegramBotToken, "")
	}
	reg.Register(guestlistteam.JobType, guestlistteam.New(o).Handler())
	logger.Info("guest list team send registered", "interval", guestlistteam.DefaultInterval.String())
}

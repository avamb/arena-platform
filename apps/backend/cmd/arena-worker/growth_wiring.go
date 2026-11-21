package main

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

// buildGrowthLetterOptions assembles the dependencies of the growth letters
// (cart reminder, marketing confirmation, late payment) from the delivery
// options, so they share the sender, the templates AND the media resolver of
// the ticket letters. Like buildDeliveryHandlerOptions, it exists to be unit
// tested: until 2026-11-07 the literal in run() carried no Media, so the
// marketing confirmation left without the organization's logo.
func buildGrowthLetterOptions(d delivery.HandlerOptions, db emailstatus.DB, cfg *config.Config, logger *slog.Logger) delivery.GrowthLetterOptions {
	return delivery.GrowthLetterOptions{
		Sender:     d.Sender,
		Templates:  d.Templates,
		DB:         db,
		Media:      d.Media,
		APIBaseURL: cfg.APIPublicBaseURL(),
		Logger:     logger,
	}
}

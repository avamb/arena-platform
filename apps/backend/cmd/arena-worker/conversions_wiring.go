package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// buildMetaTokenBox opens META_TOKEN_ENC_KEY when the Meta connection is on
// (cfg.MetaEnabled). nil means the conversions ignore the connection and read
// only org_conversion_settings.
func buildMetaTokenBox(cfg *config.Config, logger *slog.Logger) *secretbox.Box {
	if !cfg.MetaEnabled() {
		return nil
	}
	box, err := secretbox.ParseKeys(cfg.MetaTokenEncKey)
	if err != nil {
		if logger != nil {
			logger.Error("conversions: META_TOKEN_ENC_KEY is invalid, the Meta connection is ignored")
		}
		return nil
	}
	return box
}

// buildConversionsStore is the ONE conversions store of the worker: the
// outbox leg (legConversions) and the conversions.sweep handler both use it,
// so a connection-only organization is queued AND sent. A Meta connection
// that breaks (code 190) tells its owners by e-mail and, with
// EVENTS_TELEGRAM_BOT_TOKEN, through the event-center bot.
func buildConversionsStore(pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) *conversions.PGStore {
	return conversions.NewPGStore(pool).WithMetaBox(buildMetaTokenBox(cfg, logger)).
		WithOwnerNotice(cfg.EventCenterPublicURL, cfg.EventsTelegramBotToken != "", logger)
}

// conversionsSenders aims the Meta sender at META_GRAPH_BASE_URL (Graph when
// empty) and META_GRAPH_VERSION, for both token sources.
func conversionsSenders(cfg *config.Config) conversions.Senders {
	var s conversions.Senders
	if cfg != nil {
		s.Meta = conversions.MetaSender{BaseURL: cfg.MetaGraphBaseURL, Version: cfg.MetaGraphVersion}
	}
	return s
}

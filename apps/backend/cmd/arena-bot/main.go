// arena-bot is the Telegram event-center bot process
// (08_architecture/28_telegram_event_center_bot_ru.md §2).
//
// It is the fourth binary of the arena image next to arena-api,
// arena-worker and arena-migrate: same config schema, same logger and
// metrics, one replica, no public port. It long-polls Telegram, keeps its
// own bot_* tables (links, invitations, drafts) and calls arena-api over
// REST as the linked user for everything else.
//
// Environment (on top of the shared schema):
//
//	EVENTS_TELEGRAM_BOT_TOKEN    the bot's token — required
//	EVENTS_TELEGRAM_BOT_USERNAME the bot's @username (deep links in mails)
//	BOT_SERVICE_TOKEN            shared with arena-api for /v1/bot/invitations/accept
//	BOT_ARENA_API_URL            http://api:8080 inside compose
//	BOT_METRICS_ADDR             sidecar /healthz and /metrics (default :9092)
//	JWT_SIGNING_SECRET           the API's own secret: the bot mints user JWTs with it
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "arena-bot: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := logging.NewWithOptions(logging.Options{
		Writer:  os.Stdout,
		Format:  cfg.LogFormat,
		Level:   cfg.LogLevel,
		App:     "arena-bot",
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	if cfg.EventsTelegramBotToken == "" {
		return errors.New("EVENTS_TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.JWTSecretStub == "" {
		return errors.New("JWT_SIGNING_SECRET is required: the bot mints user tokens with the API's secret")
	}
	if cfg.BotServiceToken == "" {
		logger.Warn("arena-bot: BOT_SERVICE_TOKEN is empty — invitations cannot be accepted until it is set on both api and bot")
	}
	logger.Info("arena-bot starting", slog.String("arena_api", cfg.BotArenaAPIURL))

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := observability.MustNew(nil)

	connectCtx, cancelConnect := context.WithTimeout(rootCtx, 60*time.Second)
	pool, err := database.Open(connectCtx, cfg, logger)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	bundle, err := i18n.NewBundle()
	if err != nil {
		return fmt.Errorf("load i18n bundle: %w", err)
	}

	bot, err := eventbot.New(eventbot.Options{
		Token:   cfg.EventsTelegramBotToken,
		Queries: gen.New(pool.Pool),
		Arena:   eventbot.NewArenaClient(cfg.BotArenaAPIURL, cfg.BotServiceToken, nil),
		Minter:  eventbot.NewTokenMinter(cfg.JWTSecretStub, cfg.JWTIssuer, cfg.JWTAudience),
		Texts:   eventbot.NewTexts(bundle),
		Logger:  logger,

		TicketsBaseURL: cfg.PublicTicketsBaseURL,
	})
	if err != nil {
		return err
	}

	// Sidecar /healthz and /metrics, guarded exactly like the worker's.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", httpserver.RequireMetricsBearerToken(cfg.MetricsBearerToken, metrics.Handler()))
	srv := &http.Server{Addr: cfg.BotMetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	srvErr := make(chan error, 1)
	go func() {
		logger.Info("arena-bot metrics server listening", "addr", cfg.BotMetricsAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
		close(srvErr)
	}()

	botErr := make(chan error, 1)
	go func() {
		botErr <- bot.Run(rootCtx)
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("arena-bot: shutdown signal received")
	case err := <-srvErr:
		if err != nil {
			return fmt.Errorf("metrics server: %w", err)
		}
	case err := <-botErr:
		if err != nil {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	logger.Info("arena-bot stopped")
	return nil
}

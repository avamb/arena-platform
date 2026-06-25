// Package main is the entry point for the arena-worker background worker.
//
// arena-worker polls the worker_jobs table for ready rows, dispatches
// each one to a handler registered against its job_type, and writes the
// outcome (done / retry / failed) back to the row. Jobs survive the
// worker being offline — that is the contract the platform tables
// (feature #20) are designed to fulfil.
//
// Feature #102 additions:
//
//   - Observability stack shared with arena-api (same config schema,
//     same Prometheus registry shape, same OTel init path).
//   - OutboxBacklogPoller: a background ticker (default 5 s) that runs
//     SELECT count(*) FROM outbox WHERE dispatched_at IS NULL and
//     stores the result in the arena_outbox_backlog Prometheus gauge.
//   - Placeholder job handler ("placeholder.log") registered via
//     worker.ShouldRunPlaceholderJob / worker.PlaceholderJobHandler.
//
// Feature #109 additions:
//
//   - /healthz and /metrics HTTP endpoints served by a lightweight sidecar
//     HTTP server bound to WORKER_METRICS_ADDR (default :9091). This
//     lets ops teams scrape the worker's Prometheus metrics and probe its
//     liveness independently of arena-api.
//
// This binary is intentionally lean: it loads configuration, opens a
// pgx pool, builds a handler registry, runs the worker loop, and exits
// cleanly on SIGINT/SIGTERM.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "arena-worker: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Configuration --------------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. Logger ---------------------------------------------------------------
	logger := logging.NewWithOptions(logging.Options{
		Writer:  os.Stdout,
		Format:  cfg.LogFormat,
		Level:   cfg.LogLevel,
		App:     "arena-worker",
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	logger.Info("arena-worker starting")

	// 3. Signal-bound root context --------------------------------------------
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. Observability (shared with arena-api) --------------------------------
	// Worker and API use identical Prometheus metric shapes so dashboards
	// can scrape either process without reconfiguration (feature #102,
	// step 6: "Worker and API share config schema and observability stack").
	metrics := observability.MustNew(nil)

	tracerCtx, cancelTracer := context.WithTimeout(rootCtx, 10*time.Second)
	_, tracerShutdown, err := observability.InitTracer(tracerCtx, observability.TracingOptions{
		Endpoint:       cfg.OTLPEndpoint,
		Insecure:       cfg.OTELInsecure,
		ServiceName:    coalesce(cfg.OTELServiceName, "arena-worker"),
		ServiceVersion: cfg.AppVersion,
		Environment:    string(cfg.AppEnv),
		SamplerRatio:   cfg.OTELTracesSampler,
	})
	cancelTracer()
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(flushCtx); err != nil {
			logger.Warn("tracer shutdown failed", "error", err.Error())
		}
	}()

	logger.Info("observability initialized",
		"otlp_endpoint", cfg.OTLPEndpoint,
	)

	// 5. Database pool --------------------------------------------------------
	// Use a bounded connect deadline so the container fails fast if
	// Postgres is genuinely unreachable rather than hanging on the
	// docker-compose dependency check.
	connectCtx, cancelConnect := context.WithTimeout(rootCtx, 60*time.Second)
	pool, err := database.Open(connectCtx, cfg, logger)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	// 6. Outbox backlog poller (feature #102, step 2) -------------------------
	// A lightweight background goroutine that refreshes the
	// arena_outbox_backlog Prometheus gauge every 5 s by running a
	// single COUNT query against the outbox table. This gives ops teams
	// a real-time view of undelivered domain events without touching the
	// dispatch path.
	poller := worker.NewOutboxBacklogPoller(worker.OutboxBacklogPollerOptions{
		Querier:      worker.NewPGOutboxBacklogQuerier(pool.Pool),
		Gauge:        metrics.OutboxBacklog,
		Logger:       logger,
		PollInterval: worker.DefaultOutboxBacklogPollInterval, // 5 s
	})
	pollerErrCh := make(chan error, 1)
	go func() { pollerErrCh <- poller.Run(rootCtx) }()

	// 6b. Outbox events dispatcher (feature #110) --------------------------------
	// Polls the outbox_events table (populated transactionally by domain
	// mutations, e.g. POST /v1/echo) and delivers each unprocessed row to the
	// configured Dispatcher implementation.
	//
	// When OUTBOX_WEBHOOK_URL is set the dispatcher POSTs each event to that
	// URL with an HMAC-SHA256 X-Arena-Signature header (using OUTBOX_SIGNING_SECRET).
	// When OUTBOX_WEBHOOK_URL is empty, the NoopDispatcher is used so the worker
	// starts cleanly in environments that have not yet wired a webhook target.
	outboxDispatcher := buildOutboxDispatcher(cfg, logger)
	outboxStore := outbox.NewPGOutboxEventStore(pool.Pool)
	outboxEventsDisp, outboxDispErr := outbox.NewOutboxEventsDispatcher(outbox.OutboxEventsDispatcherOptions{
		Store:           outboxStore,
		Dispatcher:      outboxDispatcher,
		Logger:          logger,
		PollInterval:    cfg.OutboxPollInterval,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	if outboxDispErr != nil {
		return fmt.Errorf("init outbox events dispatcher: %w", outboxDispErr)
	}
	go func() { _ = outboxEventsDisp.Run(rootCtx) }()
	logger.Info("outbox events dispatcher started",
		"webhook_url", cfg.OutboxWebhookURL,
		"signed", cfg.OutboxSigningSecret != "",
		"poll_interval", cfg.OutboxPollInterval.String(),
	)

	// 7. Handler registry -----------------------------------------------------
	// The platform foundation milestone ships two handlers:
	//   noop.test         — used by the worker_jobs persistence test (#20)
	//   placeholder.log   — demonstrates ShouldRunPlaceholderJob (step 3)
	//   idempotency.cleanup — purges expired idempotency_keys (feature #48)
	registry := worker.NewRegistry()
	registerBuiltinHandlers(registry, pool.Pool, metrics, logger)

	// 7b. Idempotency cleanup startup scheduling (feature #48) ---------------
	// Enqueue an idempotency.cleanup job immediately if none is already
	// pending in the queue. The handler self-schedules the next run after
	// each completion, providing cron-like periodic execution.
	if err := idempotency.ScheduleInitialCleanupJob(rootCtx, pool.Pool); err != nil {
		// Non-fatal: the cleanup job is a maintenance task. Log the failure
		// and continue — data correctness is not compromised if the first
		// cleanup run is delayed.
		logger.Warn("could not schedule initial idempotency cleanup job", "error", err.Error())
	} else {
		logger.Info("idempotency cleanup job scheduled at startup")
	}

	// 8. Metrics + healthz HTTP server (feature #109, step 6) ----------------
	// A lightweight sidecar HTTP server exposes:
	//   GET /healthz  — liveness probe (always 200 while the process is up)
	//   GET /metrics  — Prometheus scrape endpoint
	//
	// The server is bound to WORKER_METRICS_ADDR (default :9091) so it does
	// not conflict with arena-api on :8080. Both endpoints are intentionally
	// unauthenticated because they are expected to sit inside a private
	// network boundary — the same posture as the arena-api /metrics endpoint.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	metricsMux.Handle("/metrics", metrics.Handler())

	metricsSrv := &http.Server{
		Addr:         cfg.WorkerMetricsAddr,
		Handler:      metricsMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	metricsSrvErrCh := make(chan error, 1)
	go func() {
		logger.Info("arena-worker metrics server listening", "addr", cfg.WorkerMetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			metricsSrvErrCh <- err
		} else {
			metricsSrvErrCh <- nil
		}
	}()

	// 9. Worker loop ----------------------------------------------------------
	w, err := worker.New(worker.Options{
		Pool:            pool,
		Registry:        registry,
		Logger:          logger,
		PollInterval:    time.Second,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct worker: %w", err)
	}

	logger.Info("arena-worker ready",
		"instance_id", w.InstanceID(),
		"poll_interval", "1s",
		"outbox_backlog_interval", worker.DefaultOutboxBacklogPollInterval.String(),
		"metrics_addr", cfg.WorkerMetricsAddr,
	)

	// Run the loop in a goroutine so we can react to the signal-bound
	// rootCtx without blocking the main goroutine on Run.
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- w.Run(rootCtx) }()

	// 10. Wait for shutdown signal or fatal error ------------------------------
	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received; stopping worker")
	case err := <-runErrCh:
		// Worker.Run returned on its own — propagate any unexpected error.
		if err != nil {
			return fmt.Errorf("worker run: %w", err)
		}
		logger.Info("worker run exited cleanly without signal")
		// Shut down the metrics server too before returning.
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = metricsSrv.Shutdown(shutCtx)
		return nil
	case err := <-metricsSrvErrCh:
		// Metrics server crashed — surface the error but keep going;
		// the job queue must not stop just because the scrape port is busy.
		logger.Error("metrics server failed", "error", err)
	}

	// 11. Graceful shutdown ---------------------------------------------------
	if err := w.Stop(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		logger.Warn("worker stop returned error", "error", err.Error())
	}

	// Drain runErrCh so the goroutine doesn't leak after Stop returns.
	select {
	case err := <-runErrCh:
		if err != nil {
			logger.Warn("worker exited with error", "error", err.Error())
		}
	case <-time.After(cfg.ShutdownTimeout):
		logger.Warn("worker goroutine did not exit within shutdown timeout")
	}

	// Shut down the metrics/healthz HTTP server gracefully.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := metricsSrv.Shutdown(shutCtx); err != nil {
		logger.Warn("metrics server shutdown error", "error", err.Error())
	}

	// The poller goroutine exits when rootCtx is cancelled (already done).
	// Drain its channel to avoid a goroutine leak.
	select {
	case <-pollerErrCh:
	case <-time.After(2 * time.Second):
		logger.Warn("outbox backlog poller did not stop within 2s")
	}

	// Gracefully stop the outbox events dispatcher (feature #110).
	if err := outboxEventsDisp.Stop(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		logger.Warn("outbox events dispatcher stop returned error", "error", err.Error())
	}

	logger.Info("arena-worker stopped cleanly")
	return nil
}

// registerBuiltinHandlers attaches every job type the foundation
// milestone ships with.
func registerBuiltinHandlers(reg *worker.Registry, pool *pgxpool.Pool, metrics *observability.Metrics, logger *slog.Logger) {
	// noop.test exists for feature #20 (worker job persistence) and for
	// any future smoke test that wants to prove the queue plumbing
	// without exercising business code. It always succeeds.
	reg.Register("noop.test", func(_ context.Context, payload []byte) error {
		logger.Info("noop.test handler invoked", "payload_bytes", len(payload))
		return nil
	})

	// placeholder.log is registered for feature #102 (step 3). It
	// demonstrates that ShouldRunPlaceholderJob() can gate dispatch
	// decisions and that the handler stub logs and returns nil (success).
	if worker.ShouldRunPlaceholderJob() {
		reg.Register("placeholder.log", worker.PlaceholderJobHandler(logger))
	}

	// idempotency.cleanup purges expired idempotency_keys rows and
	// self-schedules the next run (cron-like, default interval 1 hour).
	// Feature #48.
	reg.Register(idempotency.CleanupJobType, idempotency.NewCleanupHandler(idempotency.CleanupOptions{
		Cleaner:        idempotency.NewPGCleaner(pool),
		DeletedCounter: metrics.IdempotencyCleanupDeletedTotal,
		Scheduler:      idempotency.NewPGCleanupScheduler(pool),
	}))

	// ticket.deliver sends transactional emails with PDF attachments for
	// issued tickets. Feature #141.
	// In development (no SMTP configured), a LogSender writes emails to
	// the structured logger instead of delivering them.
	queries := gen.New(pool)
	reg.Register(delivery.JobType, delivery.NewHandler(delivery.HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             buildEmailSender(logger),
		FromAddress:        coalesce(getEmailFrom(), "tickets@arena.example.com"),
		Logger:             logger,
	}))
}

// buildEmailSender returns an email.Sender appropriate for the current
// environment.  When SMTP_HOST is set, an SMTPSender is returned;
// otherwise a LogSender is returned so development/CI environments work
// without an SMTP server.
func buildEmailSender(logger *slog.Logger) email.Sender {
	host := coalesce(os.Getenv("SMTP_HOST"), "")
	if host == "" {
		logger.Info("email: SMTP_HOST not configured; using LogSender (emails logged, not sent)")
		return &email.LogSender{Logger: logger}
	}
	return email.NewSMTPSender(email.SMTPConfig{
		Host:     host,
		Port:     coalesce(os.Getenv("SMTP_PORT"), "25"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     coalesce(os.Getenv("SMTP_FROM"), "tickets@arena.example.com"),
		UseTLS:   os.Getenv("SMTP_USE_TLS") == "true",
	})
}

// getEmailFrom returns the SMTP_FROM environment variable.
func getEmailFrom() string { return os.Getenv("SMTP_FROM") }

// buildOutboxDispatcher constructs the Dispatcher for the outbox events loop.
//
// When OUTBOX_WEBHOOK_URL is configured a WebhookDispatcher is returned that
// POSTs signed payloads to that URL. Otherwise NoopDispatcher is returned so
// the dispatcher starts cleanly in environments without a webhook target.
func buildOutboxDispatcher(cfg *config.Config, logger *slog.Logger) outbox.Dispatcher {
	if cfg.OutboxWebhookURL == "" {
		logger.Info("outbox events dispatcher: no OUTBOX_WEBHOOK_URL configured; using noop dispatcher")
		return outbox.NoopDispatcher{}
	}
	d, err := outbox.NewWebhookDispatcher(outbox.WebhookDispatcherOptions{
		TargetURL:     cfg.OutboxWebhookURL,
		SigningSecret: []byte(cfg.OutboxSigningSecret),
	})
	if err != nil {
		// TargetURL is non-empty — this should never fail. Fall back to noop
		// and log the error rather than crashing the worker.
		logger.Error("outbox events dispatcher: failed to build webhook dispatcher; falling back to noop",
			"error", err.Error(),
		)
		return outbox.NoopDispatcher{}
	}
	return d
}

// coalesce returns the first non-empty argument.
func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

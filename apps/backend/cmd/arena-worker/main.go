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
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/backfill"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/brevo"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/convertjob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customerimport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/mediaresolver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/reservationexpiry"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
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

	// Brevo sender verification is separate from transactional mail delivery.
	// It only reads platform-owned API credentials and updates durable org state.
	senderVerificationPoller := brevo.NewPoller(pool.Pool, brevo.New(cfg.BrevoAPIKey, cfg.BrevoAPIBaseURL, nil), logger, cfg.SenderVerificationPollInterval)
	go senderVerificationPoller.Run(rootCtx)

	// 6b. Outbox events dispatcher (feature #110) --------------------------------
	// Polls the outbox_events table (populated transactionally by domain
	// mutations, e.g. POST /v1/echo) and delivers each unprocessed row to the
	// configured Dispatcher implementation.
	//
	// When OUTBOX_WEBHOOK_URL is set the dispatcher POSTs each event to that
	// URL with an HMAC-SHA256 X-Arena-Signature header (using OUTBOX_SIGNING_SECRET).
	// When OUTBOX_WEBHOOK_URL is empty, the NoopDispatcher is used so the worker
	// starts cleanly in environments that have not yet wired a webhook target.
	//
	// AB-50c: MACS dispatcher is added to the fan-out. It delivers MACS-shaped
	// payloads to per-org MACS webhook subscribers for ticket lifecycle events.
	// Delivery failure returns an error so the outbox row retries (at-least-once).
	//
	// W1-B7c (#506): the bil24_wp dispatcher is the third member. It delivers
	// order/ticket/catalog events to the migrated WordPress sites in Bil24's
	// own webhook vocabulary, routed by the sales channel each site subscribed
	// to. It shares the same at-least-once contract: non-2xx retries.
	baseOutboxDispatcher := buildOutboxDispatcher(cfg, logger)
	macsDispatcher := macs.NewDispatcher(pool.Pool, macs.WithLogger(logger))
	bil24WPDispatcher := bil24wire.NewDispatcher(pool.Pool)
	outboxDispatcher := &multiDispatcher{dispatchers: []outbox.Dispatcher{
		baseOutboxDispatcher,
		macsDispatcher,
		bil24WPDispatcher,
	}}
	outboxStore := outbox.NewPGOutboxEventStore(pool.Pool)
	outboxEventsDisp, outboxDispErr := outbox.NewOutboxEventsDispatcher(outbox.OutboxEventsDispatcherOptions{
		Store:           outboxStore,
		Dispatcher:      outboxDispatcher,
		Logger:          logger,
		PollInterval:    cfg.OutboxPollInterval,
		ShutdownTimeout: cfg.ShutdownTimeout,
		// AB-50c: MACS contract is "retry over 24 hours". With the default
		// backoff (2^n min, capped at 1h) 30 attempts span ~24h; the
		// previous default of 5 dead-lettered after ~31 minutes.
		MaxAttempts: 30,
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
	//
	// opsNotifier degrades to a logging no-op when OPS_TELEGRAM_BOT_TOKEN /
	// OPS_TELEGRAM_CHAT_ID are unset, so this wiring is always safe in
	// dev/test/CI. Built here (before the registry) so both the handler
	// registration below and the startup message at 7e share one instance.
	opsNotifier := opsalert.New(cfg.OpsTelegramBotToken, cfg.OpsTelegramChatID, cfg.OpsAlertEnvLabel, logger)

	// The media store is built ONCE here and shared by every handler that
	// needs it: media-gc and customer.import (which used to build it inside
	// their own registration function) and — since 2026-09-20 — ticket
	// delivery, which needs it to put the organizer logo and the event
	// poster on the e-ticket. nil when MEDIA_BACKEND is unset or storage
	// cannot be opened; every consumer degrades on its own terms.
	mediaRepo := buildMediaRepo(pool.Pool, cfg, logger)

	registry := worker.NewRegistry()
	registerBuiltinHandlers(registry, pool.Pool, cfg, metrics, mediaRepo, logger)
	registerMediaGCHandler(registry, pool.Pool, cfg, mediaRepo, logger)
	registerOpsWatchdogHandler(registry, pool.Pool, cfg, opsNotifier, logger)
	registerSalesNotifyHandler(registry, pool.Pool, cfg, logger)

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

	// 7c. Order expire-sweep startup scheduling (feature #487) ---------------
	// Same cron-like pattern as the idempotency cleanup above: seed the queue
	// once, and every subsequent run is enqueued by the handler itself.
	if err := ordering.ScheduleInitialExpireSweepJob(rootCtx, pool.Pool); err != nil {
		// Non-fatal: a delayed first sweep only means an already-dead order
		// lingers in pending_payment a little longer. This job only closes
		// the order aggregate — the held inventory itself is released by
		// reservation.expire_sweep, scheduled separately below.
		logger.Warn("could not schedule initial order expire sweep job", "error", err.Error())
	} else {
		logger.Info("order expire sweep job scheduled at startup")
	}

	// 7d. Reservation expire-sweep startup scheduling ------------------------
	// Same cron-like pattern: seed the queue once, and every subsequent run
	// is enqueued by the handler itself. This is the job that actually
	// releases TTL-expired reservation holds (session_seats/ga_unit rows and
	// inventory_ledger.capacity_held) — without it a reservation the buyer
	// abandoned stays held forever and the session shows sold out with
	// nothing sold.
	if err := reservationexpiry.ScheduleInitialJob(rootCtx, pool.Pool); err != nil {
		// Non-fatal: a delayed first sweep only means an already-dead hold
		// lingers a little longer before its inventory is released.
		logger.Warn("could not schedule initial reservation expire sweep job", "error", err.Error())
	} else {
		logger.Info("reservation expire sweep job scheduled at startup")
	}

	// 7e. Ops watchdog startup scheduling -------------------------------------
	// ops.watchdog (internal/platform/opswatchdog) is a strictly read-only,
	// self-scheduling job that turns sales and early-warning conditions into
	// Telegram messages (internal/platform/opsalert) — built for the first
	// real ticket sales, which start with no ops staff watching dashboards.
	if err := opswatchdog.EnsureAllInitialCursors(rootCtx, pool.Pool, time.Now().UTC()); err != nil {
		// Non-fatal, but logged loudly: without seeded cursors the first
		// watchdog run would fall back to seeding them itself on first use,
		// which is equally safe (never replays history) — this call just
		// does it eagerly and up front.
		logger.Warn("could not seed initial ops watchdog cursors", "error", err.Error())
	}
	if err := opswatchdog.ScheduleInitialJob(rootCtx, pool.Pool); err != nil {
		logger.Warn("could not schedule initial ops watchdog job", "error", err.Error())
	} else {
		logger.Info("ops watchdog job scheduled at startup")
	}
	opswatchdog.SendStartupMessage(rootCtx, opsNotifier, cfg.AppVersion, cfg.AppCommit)

	// 7f. Sales notifications to organizers' Telegram groups ---------------
	// sales.notify (internal/platform/salesnotify) follows the same cursor
	// scheme as the watchdog; the cursors are seeded at "now" so turning it
	// on never replays past sales into a client's chat.
	if err := salesnotify.EnsureInitialCursors(rootCtx, pool.Pool, time.Now().UTC()); err != nil {
		logger.Warn("could not seed initial sales notify cursors", "error", err.Error())
	}
	if err := salesnotify.ScheduleInitialJob(rootCtx, pool.Pool); err != nil {
		logger.Warn("could not schedule initial sales notify job", "error", err.Error())
	}

	// 8. Metrics + healthz HTTP server (feature #109, step 6) ----------------
	// A lightweight sidecar HTTP server exposes:
	//   GET /healthz  — liveness probe (always 200 while the process is up)
	//   GET /metrics  — Prometheus scrape endpoint
	//
	// The server is bound to WORKER_METRICS_ADDR (default :9091) so it does
	// not conflict with arena-api on :8080. /healthz stays unauthenticated
	// (it carries no information). /metrics is guarded by
	// METRICS_BEARER_TOKEN exactly like arena-api's own /metrics
	// (httpserver.RequireMetricsBearerToken) — when the token is unset
	// (local compose / same-network Prometheus) behaviour is unchanged.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	metricsMux.Handle("/metrics", httpserver.RequireMetricsBearerToken(cfg.MetricsBearerToken, metrics.Handler()))

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
		PollInterval:    cfg.WorkerPollInterval,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct worker: %w", err)
	}

	logger.Info("arena-worker ready",
		"instance_id", w.InstanceID(),
		"poll_interval", cfg.WorkerPollInterval.String(),
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
//
// mediaRepo may be nil (MEDIA_BACKEND unset): ticket delivery then renders
// exactly as it did before media storage existed — platform logo, no poster.
func registerBuiltinHandlers(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, metrics *observability.Metrics, mediaRepo *mediastore.Repo, logger *slog.Logger) {
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

	// order.expire_sweep flips pending_payment orders whose hold deadline
	// passed (and whose checkout never produced a succeeded payment intent)
	// to 'expired', then self-schedules the next run a minute later.
	// W1-A6b, feature #487, spec §14.1.
	reg.Register(ordering.ExpireSweepJobType, ordering.NewExpireSweepHandler(ordering.ExpireSweepOptions{
		Store:     gen.New(pool),
		Logger:    logger,
		Scheduler: ordering.NewPGSweepScheduler(pool),
	}))

	// reservation.expire_sweep releases the inventory (held session_seats /
	// ga_unit rows plus the inventory_ledger.capacity_held counter) behind a
	// reservation whose TTL passed, then self-schedules the next run 30s
	// later. This is the job that was missing entirely before this fix:
	// hcheckout.ReservationProcessor existed since feature #131 but nothing
	// in arena-api or arena-worker ever called it, so an abandoned hold
	// stayed 'held' forever.
	reservationQueries := gen.New(pool)
	reg.Register(reservationexpiry.JobType, reservationexpiry.NewHandler(reservationexpiry.Options{
		Processor: hcheckout.NewReservationProcessor(pool, reservationQueries, logger).
			WithCheckoutQueries(reservationQueries),
		Logger:    logger,
		Scheduler: reservationexpiry.NewPGScheduler(pool),
	}))

	// ticket.deliver sends transactional emails with PDF attachments for
	// issued tickets. Feature #141.
	// In development (no SMTP configured), a LogSender writes emails to
	// the structured logger instead of delivering them.
	// The dependencies are assembled by buildDeliveryHandlerOptions so the
	// wiring is unit-testable — this is the ONLY place in the platform where
	// a delivery handler is built, so a dependency missing here (Media was,
	// for every ticket ever sent) is invisible everywhere else.
	queries := gen.New(pool)
	reg.Register(delivery.JobType, delivery.NewHandler(buildDeliveryHandlerOptions(
		cfg, queries, buildDeliveryMediaResolver(mediaRepo, cfg, logger), logger,
	)))

	// auth.email_verification and auth.password_reset_email deliver
	// account-management emails for the registration and password-reset flows.
	// Links are built from AppPublicURL (configured in the environment) so they
	// are never derived from untrusted HTTP headers. Feature PR-02.
	authEmailHandler := authemail.NewHandler(authemail.HandlerOptions{
		Sender:       buildEmailSender(cfg, logger),
		AppPublicURL: cfg.AppPublicURL,
		FromAddress:  coalesce(cfg.SMTPFrom, "noreply@arena.example.com"),
		Logger:       logger,
	})
	reg.Register(authemail.JobTypeEmailVerification, authEmailHandler.HandleEmailVerification)
	reg.Register(authemail.JobTypePasswordResetEmail, authEmailHandler.HandlePasswordResetEmail)

	// checkout.issue_tickets issues tickets after a payment.succeeded webhook
	// (feature #363, PR2-07). The handler is enqueued atomically alongside the
	// idempotency event INSERT and state transition UPDATE in
	// HandlePaymentIntentWebhook. IssueTicketsForCheckout (feature #366) is
	// idempotent — re-runs after a crash detect already-issued tickets and
	// return them without duplicate INSERTs. Delivery is enqueued inside
	// IssueTicketsForCheckout (feature #367).
	ticketIssueHandler := htickets.New(
		queries, // ticketQ
		queries, // credentialQ (not used in issuance path)
		nil,     // complimentaryQ — not needed for webhook issuance
		nil,     // inventoryQ — not needed for webhook issuance
		queries, // reservationQ
		nil,     // barcodeQ — not needed for webhook issuance
		queries, // deliveryJobQ — used by EnqueueDeliveryJobs inside IssueTicketsForCheckout
		nil,     // feedTokenQ — not needed for webhook issuance
		pool,    // workerPool for delivery job enqueue
		pool,    // pool (TxStarter) for advisory-lock transaction
		nil,     // audit — not required for background issuance
		logger,
		nil, // publishTicketIssuedEvents — scanner events not required in worker context
		nil, // publishTicketRevokedV1Events — revocation events not required here
		nil, // publishTicketCancelledEvent — AB-49 cancellation is an admin-API action, not a worker one
		// v1.order.paid is written on the issuance transaction itself
		// (feature #488), so the worker — which is where issuance actually
		// runs in production — must carry the outbox writer.
	).WithOutboxWriter(outbox.NewPGEventsWriter(pool))
	reg.Register(issuejob.JobType, issuejob.NewHandler(issuejob.HandlerOptions{
		Queries:      queries,
		IssueTickets: ticketIssueHandler.IssueTicketsForCheckout,
		Logger:       logger,
	}))

	// checkout.convert_reservation: durable held→sold conversion for the webhook
	// path (PR2-27, feature #383). A minimal Handler is built here — only the
	// fields needed by convertReservationTx (reservationQueries, inventoryQueries,
	// pool) need to be non-nil; the rest are wired as nil/zero.
	convertHandler := hcheckout.New(
		nil,     // checkoutQ — not needed for conversion
		queries, // reservationQ
		queries, // inventoryQ
		nil,     // paymentIntentQ — not needed
		nil,     // refundQ — not needed
		nil,     // promoQ — not needed
		nil,     // tierQ — not needed
		nil,     // ticketQ — not needed
		nil,     // channelQ — not needed
		nil,     // orgQ — not needed
		pool,    // pool (TxStarter)
		logger,
		hcheckout.PricingRules{},
		"", "", // webhookStripeSecret, webhookAllPaySecret
		nil, nil, nil, // issueTickets, publishRefunded, publishRefundedV1
	)
	reg.Register(convertjob.JobType, convertjob.NewHandler(convertjob.HandlerOptions{
		ConvertFn: convertHandler.ConvertReservationTx,
		Logger:    logger,
	}))

	// tickets.backfill_ean13 mints the EAN-13 credential + platform barcode
	// for tickets issued before feature #502 wired that into issuance.
	// Feature #503, W1-B6b. Not self-scheduling — an operator/stand-setup
	// script enqueues it (repeatedly, if the batch is large); each run is
	// a bounded, idempotent pass.
	reg.Register(backfill.JobType, backfill.NewHandler(backfill.Options{
		Store:  backfill.NewPGStore(queries),
		Logger: logger,
	}))
}

// registerOpsWatchdogHandler registers the self-scheduling ops.watchdog job
// (internal/platform/opswatchdog). It always registers — unlike
// registerMediaGCHandler, there is no configuration state under which the
// watchdog should be entirely absent, since it degrades to a logging no-op
// notifier rather than needing a feature flag.
func registerOpsWatchdogHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, notifier opsalert.Notifier, logger *slog.Logger) {
	reg.Register(opswatchdog.JobType, opswatchdog.NewHandler(opswatchdog.Options{
		Pool:             pool,
		Notifier:         notifier,
		Logger:           logger,
		Interval:         opswatchdog.DefaultInterval,
		Scheduler:        opswatchdog.NewPGScheduler(pool),
		HeartbeatHourUTC: cfg.OpsWatchdogHeartbeatHourUTC,
	}))
	logger.Info("ops.watchdog handler registered",
		"interval", opswatchdog.DefaultInterval.String(),
		"heartbeat_hour_utc", cfg.OpsWatchdogHeartbeatHourUTC,
	)
}

// registerSalesNotifyHandler registers sales.notify. Without
// SALES_TELEGRAM_BOT_TOKEN the job still runs and advances its cursors, so
// adding the token later starts from that moment, not from a backlog.
func registerSalesNotifyHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) {
	opts := salesnotify.Options{
		Store:     salesnotify.NewPGStore(pool),
		Cursors:   opswatchdog.NewPGCursorStore(pool),
		Scheduler: salesnotify.NewPGScheduler(pool),
		Logger:    logger,
	}
	if cfg.SalesTelegramBotToken != "" {
		opts.Sender = salesnotify.NewTelegramSender(cfg.SalesTelegramBotToken, "")
	}
	reg.Register(salesnotify.JobType, salesnotify.NewHandler(opts))
	logger.Info("sales.notify handler registered", "telegram_configured", opts.Sender != nil)
}

// buildMediaRepo opens the media storage backend and wraps it in a
// mediastore.Repo, or returns nil when MEDIA_BACKEND is unset or the backend
// cannot be opened. Never fatal: a worker that cannot reach media storage
// must still deliver tickets, run the sweeps and drain the outbox.
//
// SigningSecret mirrors arena-api (cmd/arena-api/main.go): the two processes
// must sign /v1/media-files/{id} with the SAME key, or a URL this worker
// puts in an e-mail is rejected by the API that serves it.
func buildMediaRepo(pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger) *mediastore.Repo {
	if cfg.MediaBackend == "" {
		logger.Info("media storage not configured (MEDIA_BACKEND not set)")
		return nil
	}
	st, err := storage.NewFromConfig(storage.Config{
		Backend:           storage.Backend(cfg.MediaBackend),
		LocalRoot:         cfg.MediaLocalRoot,
		S3Endpoint:        cfg.MediaS3Endpoint,
		S3Region:          cfg.MediaS3Region,
		S3Bucket:          cfg.MediaS3Bucket,
		S3AccessKeyID:     cfg.MediaS3AccessKeyID,
		S3SecretAccessKey: cfg.MediaS3SecretAccessKey,
		S3UsePathStyle:    cfg.MediaS3UsePathStyle,
	})
	if err != nil {
		logger.Error("media storage init failed", "error", err.Error())
		return nil
	}
	repo, err := mediastore.New(mediastore.Options{
		Pool:          pool,
		Storage:       st,
		SigningSecret: cfg.MediaSigningKey(),
	})
	if err != nil {
		logger.Error("media repo init failed", "error", err.Error())
		return nil
	}
	logger.Info("media storage configured", "backend", cfg.MediaBackend)
	return repo
}

// buildDeliveryMediaResolver adapts the shared media repo to the narrow
// resolver the ticket-delivery handler uses for the organizer logo and the
// event poster. A nil repo yields a nil resolver, which is exactly the
// pre-fix behaviour: platform logo in the e-mail, no poster on the PDF.
//
// The API's public origin is needed because the signed download path is
// host-relative and an e-mail <img src> must be absolute; it is the same
// value the API builds its own site-facing links on (API_PUBLIC_URL,
// falling back to APP_PUBLIC_URL). Unset is tolerated — the logo <img> is
// then simply dropped by the template rather than pointing nowhere.
func buildDeliveryMediaResolver(repo *mediastore.Repo, cfg *config.Config, logger *slog.Logger) delivery.MediaResolver {
	if repo == nil {
		return nil
	}
	base := cfg.APIPublicBaseURL()
	if base == "" {
		logger.Warn("ticket delivery: neither API_PUBLIC_URL nor APP_PUBLIC_URL is set; " +
			"the e-ticket PDF still embeds the organizer logo but the e-mail header cannot link to it")
	}
	return mediaresolver.New(repo, base, logger)
}

// buildDeliveryHandlerOptions assembles the ticket.deliver handler's
// dependencies. Extracted from registerBuiltinHandlers so the wiring —
// specifically, that Media is actually populated — is testable without a
// database: arena-worker is the ONLY process that renders live ticket
// e-mails, and it shipped with Media nil, so no organizer logo ever reached
// a real ticket.
func buildDeliveryHandlerOptions(cfg *config.Config, queries *gen.Queries, media delivery.MediaResolver, logger *slog.Logger) delivery.HandlerOptions {
	return delivery.HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             buildEmailSender(cfg, logger),
		FromAddress:        coalesce(getEmailFrom(cfg), "tickets@arena.example.com"),
		Media:              media,
		Logger:             logger,
	}
}

// registerMediaGCHandler registers the media-gc worker handler when the
// media storage backend is configured. When MEDIA_BACKEND is unset the
// handler is not registered — any media-gc job sitting in the queue
// fails fast with worker.ErrUnknownJobType, surfacing the
// misconfiguration to operators instead of silently leaking bytes.
func registerMediaGCHandler(reg *worker.Registry, pool *pgxpool.Pool, cfg *config.Config, repo *mediastore.Repo, logger *slog.Logger) {
	if repo == nil {
		logger.Info("media-gc handler skipped (media storage not configured)")
		return
	}
	reg.Register(mediastore.JobType, mediastore.NewGCHandler(mediastore.GCHandlerOptions{
		Repo:   repo,
		Logger: logger,
	}))
	logger.Info("media-gc handler registered",
		"backend", cfg.MediaBackend,
		"retention", mediastore.DefaultRetention.String(),
	)

	// customer.import (feature #519) reads its uploaded export through the
	// same mediastore.Repo, so it shares this MEDIA_BACKEND-gated wiring
	// point rather than duplicating the storage.NewFromConfig call. No
	// HTTP endpoint enqueues this job yet — feature #520 builds the
	// create-import endpoint; until then a row inserted into worker_jobs
	// by hand (or a future admin tool) is the only producer.
	reg.Register(customerimport.JobType, customerimport.NewHandler(customerimport.Options{
		Pool:   pool,
		Media:  repo,
		Logger: logger,
	}))
	logger.Info("customer.import handler registered", "backend", cfg.MediaBackend)
}

// buildEmailSender returns an email.Sender appropriate for the current
// environment. EMAIL_MODE=smtp (required in production) returns an
// SMTPSender using the typed SMTP_* config values. Any other mode (or
// empty) returns a LogSender so development/CI environments work without
// an SMTP server.
func buildEmailSender(cfg *config.Config, logger *slog.Logger) email.Sender {
	if cfg.EmailMode == config.EmailModeSMTP {
		logger.Info("email: using SMTP sender", "host", cfg.SMTPHost, "from", cfg.SMTPFrom)
		return email.NewSMTPSender(email.SMTPConfig{
			Host:     cfg.SMTPHost,
			Port:     coalesce(cfg.SMTPPort, "587"),
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     coalesce(cfg.SMTPFrom, "tickets@arena.example.com"),
			UseTLS:   cfg.SMTPUseTLS,
		})
	}
	logger.Info("email: EMAIL_MODE is not 'smtp'; using LogSender (emails logged, not sent)",
		"email_mode", string(cfg.EmailMode))
	return &email.LogSender{Logger: logger}
}

// getEmailFrom returns the canonical from-address from config.
func getEmailFrom(cfg *config.Config) string { return cfg.SMTPFrom }

// buildOutboxDispatcher constructs the Dispatcher for the outbox events loop
// based on cfg.OutboxMode.
//
//   - webhook  → WebhookDispatcher posting signed payloads to OUTBOX_WEBHOOK_URL.
//   - disabled → DisabledDispatcher; the OutboxEventsDispatcher loop skips
//     ClaimNext entirely so no rows are ever consumed.
//   - noop/"" → NoopDispatcher (dev/test only; rejected in production by PR-00).
func buildOutboxDispatcher(cfg *config.Config, logger *slog.Logger) outbox.Dispatcher {
	switch cfg.OutboxMode {
	case config.OutboxModeDisabled:
		logger.Info("outbox events dispatcher: OUTBOX_MODE=disabled; no events will be claimed or consumed")
		return outbox.DisabledDispatcher{}

	case config.OutboxModeWebhook:
		if cfg.OutboxWebhookURL == "" {
			// Config validation should have caught this. Fail-safe to noop.
			logger.Error("outbox: OUTBOX_MODE=webhook but OUTBOX_WEBHOOK_URL is empty; falling back to noop (check config)")
			return outbox.NoopDispatcher{}
		}
		d, err := outbox.NewWebhookDispatcher(outbox.WebhookDispatcherOptions{
			TargetURL:     cfg.OutboxWebhookURL,
			SigningSecret: []byte(cfg.OutboxSigningSecret),
		})
		if err != nil {
			logger.Error("outbox events dispatcher: failed to build webhook dispatcher; falling back to noop",
				"error", err.Error(),
			)
			return outbox.NoopDispatcher{}
		}
		logger.Info("outbox events dispatcher: OUTBOX_MODE=webhook",
			"webhook_url", cfg.OutboxWebhookURL,
			"signed", cfg.OutboxSigningSecret != "",
		)
		return d

	default: // OutboxModeNoop, "" — dev/test only; rejected in production by PR-00
		logger.Info("outbox events dispatcher: no webhook configured; using noop dispatcher (dev/test only)")
		return outbox.NoopDispatcher{}
	}
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

// multiDispatcher fans out Dispatch calls to multiple Dispatcher implementations
// in order. If any dispatcher returns an error, delivery stops and the error is
// returned (causing the outbox to retry the entire row). This is at-least-once
// delivery: on retry, all dispatchers are called again.
type multiDispatcher struct {
	dispatchers []outbox.Dispatcher
}

func (m *multiDispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	for _, d := range m.dispatchers {
		if err := d.Dispatch(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

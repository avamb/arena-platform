package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/redissession"
)

// Options bundles the dependencies that New requires. Using a struct rather
// than positional parameters keeps the constructor stable as more boundaries
// are bolted on by later features (PermissionBoundary, OutboxDispatcher, …).
//
// Most *Queries fields are optional: when nil and PgxPool is non-nil, the
// constructor calls gen.New(PgxPool). Tests may inject gen.New(nil) to mount
// routes without a real database (the route mounts guard against pool-less
// writes).
type Options struct {
	Config *config.Config
	Logger *slog.Logger

	// DB is the legacy Pinger contract used by /readyz. When non-nil it
	// is wrapped as a "database" ReadinessProbe and prepended to Probes.
	DB     Pinger
	Probes []ReadinessProbe

	Pool    PoolDB
	Auth    *auth.StubProvider
	Audit   audit.Writer
	Idem    idempotency.Store
	PgxPool *pgxpool.Pool

	MetricsHandler http.Handler
	Metrics        *observability.Metrics

	FaultInjectOutboxAfterAudit bool
	SlowDelay                   time.Duration
	DebugRoutesEnabled          bool
	DebugSlowDelay              time.Duration
	// Bil24CompatEnabled mounts /compat/bil24/* when true. Corresponds to env
	// var BIL24_COMPAT_ENABLED=true (feature #157). MUST remain false in
	// production deployments unless explicitly required for a migration window.
	Bil24CompatEnabled bool

	// Per-domain sqlc *Queries. See struct docs above.
	SuperadminQueries     *gen.Queries
	AllocationQueries     *gen.Queries
	ComplimentaryQueries  *gen.Queries
	BarcodeBatchQueries   *gen.Queries
	WebhookSubQueries     *gen.Queries
	ReconciliationQueries *gen.Queries
	NetworkQueries        *gen.Queries
	GeoQueries            *gen.Queries
	OrgQueries            *gen.Queries
	ChannelQueries        *gen.Queries
	MembershipQueries     *gen.Queries
	VenueQueries          *gen.Queries
	FeedTokenQueries      *gen.Queries
	EventQueries          *gen.Queries
	PublicationQueries    *gen.Queries
	PublicFeedQueries     *gen.Queries
	SessionQueries        *gen.Queries
	GDPRQueries           *gen.Queries
	TierQueries           *gen.Queries
	InventoryQueries      *gen.Queries
	ReservationQueries    *gen.Queries
	PromoQueries          *gen.Queries
	PricingRules          PricingRules
	CheckoutQueries       *gen.Queries
	PaymentIntentQueries  *gen.Queries
	TicketQueries         *gen.Queries
	CredentialQueries     *gen.Queries
	RefundQueries         *gen.Queries
	BarcodeQueries        *gen.Queries
	ReportQueries         *gen.Queries
	BillingQueries        *gen.Queries
	DeliveryJobQueries    *gen.Queries
	WorkerPool            *pgxpool.Pool
	EmailSender           email.Sender

	Bundle      *i18n.Bundle
	Outbox      outbox.Writer
	Permissions permissions.Checker
	Clock       clock.Clock

	StripeConnect                stripeConnectHelper
	StripeBilling                stripeBillingHelper
	SessionStore                 redissession.Store
	MaxConcurrentSessionsPerUser int
}

// pickQueries returns the explicitly-injected *gen.Queries, or one constructed
// from pool when the inject value is nil and pool is non-nil. Returns nil when
// both are absent — the corresponding route mount must guard against that.
func pickQueries(inject *gen.Queries, pool *pgxpool.Pool) *gen.Queries {
	if inject != nil {
		return inject
	}
	if pool != nil {
		return gen.New(pool)
	}
	return nil
}

// New constructs (but does not start) the HTTP server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Build the chi router via the adapter so the canonical middleware
	// chain is applied uniformly across every arena_new HTTP listener.
	r := httpadapter.NewRouter(httpadapter.Deps{
		Logger:         logger,
		RequestTimeout: opts.Config.RequestTimeout,
		BodyLimitBytes: opts.Config.BodyLimitBytes,
		Metrics:        opts.Metrics,
		AppEnv:         string(opts.Config.AppEnv),
	})

	// Wire locale middleware when a Bundle is provided.
	if opts.Bundle != nil {
		defaultLocale := ""
		var supported []string
		if opts.Config != nil {
			defaultLocale = opts.Config.DefaultLocale
			supported = opts.Config.ActiveLocales
		}
		r.Use(i18n.LocaleMiddleware(opts.Bundle, defaultLocale, supported))
	}

	// Lazily construct PG-backed audit + idempotency + outbox stores when
	// the caller didn't supply concrete implementations.
	auditWriter := opts.Audit
	if auditWriter == nil && opts.PgxPool != nil {
		auditWriter = audit.NewPGWriter(opts.PgxPool)
	}
	idemStore := opts.Idem
	if idemStore == nil && opts.PgxPool != nil {
		idemStore = idempotency.NewPGStore(opts.PgxPool)
	}
	outboxWriter := opts.Outbox
	if outboxWriter == nil && opts.PgxPool != nil {
		outboxWriter = outbox.NewPGWriter(opts.PgxPool)
	}

	// Permissions: DB-backed when a pool is available, AllowAll otherwise.
	permsChecker := opts.Permissions
	if permsChecker == nil && opts.PgxPool != nil {
		permsChecker = permissions.NewDBChecker(gen.New(opts.PgxPool))
	} else if permsChecker == nil {
		permsChecker = permissions.AllowAll()
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}

	// sqlc Queries for /v1/server-info.
	var siQueries *gen.Queries
	if opts.PgxPool != nil {
		siQueries = gen.New(opts.PgxPool)
	}

	// Extend the permission checker with membership-derived role resolution
	// (feature #120 step 3) when a pool is available.
	if dbChecker, ok := permsChecker.(*permissions.DBChecker); ok && opts.PgxPool != nil {
		permsChecker = dbChecker.WithMembershipQuerier(gen.New(opts.PgxPool))
	}

	// Assemble the readiness probe list.
	probes := make([]ReadinessProbe, 0, 1+len(opts.Probes))
	if opts.DB != nil {
		probes = append(probes, &pingerProbe{name: "database", p: opts.DB})
	}
	probes = append(probes, opts.Probes...)

	s := &Server{
		cfg:          opts.Config,
		logger:       logger,
		router:       r,
		probes:       probes,
		pool:         opts.Pool,
		stub:         opts.Auth,
		audit:        auditWriter,
		idem:         idemStore,
		metrics:      opts.MetricsHandler,
		typedMetrics: opts.Metrics,
		outboxWriter: outboxWriter,
		perms:        permsChecker,
		clk:          clk,
		siQueries:    siQueries,

		faultInjectOutboxAfterAudit: opts.FaultInjectOutboxAfterAudit,
		slowDelay:                   opts.SlowDelay,
		debugRoutesEnabled:          opts.DebugRoutesEnabled,
		debugSlowDelay:              opts.DebugSlowDelay,
		bil24Enabled:                opts.Bil24CompatEnabled,

		geoQueries:            pickQueries(opts.GeoQueries, opts.PgxPool),
		orgQueries:            pickQueries(opts.OrgQueries, opts.PgxPool),
		channelQueries:        pickQueries(opts.ChannelQueries, opts.PgxPool),
		membershipQueries:     pickQueries(opts.MembershipQueries, opts.PgxPool),
		venueQueries:          pickQueries(opts.VenueQueries, opts.PgxPool),
		feedTokenQueries:      pickQueries(opts.FeedTokenQueries, opts.PgxPool),
		eventQueries:          pickQueries(opts.EventQueries, opts.PgxPool),
		publicationQueries:    pickQueries(opts.PublicationQueries, opts.PgxPool),
		publicFeedQueries:     pickQueries(opts.PublicFeedQueries, opts.PgxPool),
		publicFeedRL:          newPublicFeedRateLimiter(100, 300),
		sessionQueries:        pickQueries(opts.SessionQueries, opts.PgxPool),
		gdprQueries:           pickQueries(opts.GDPRQueries, opts.PgxPool),
		tierQueries:           pickQueries(opts.TierQueries, opts.PgxPool),
		inventoryQueries:      pickQueries(opts.InventoryQueries, opts.PgxPool),
		reservationQueries:    pickQueries(opts.ReservationQueries, opts.PgxPool),
		promoQueries:          pickQueries(opts.PromoQueries, opts.PgxPool),
		pricingRules:          opts.PricingRules,
		checkoutQueries:       pickQueries(opts.CheckoutQueries, opts.PgxPool),
		paymentIntentQueries:  pickQueries(opts.PaymentIntentQueries, opts.PgxPool),
		ticketQueries:         pickQueries(opts.TicketQueries, opts.PgxPool),
		credentialQueries:     pickQueries(opts.CredentialQueries, opts.PgxPool),
		refundQueries:         pickQueries(opts.RefundQueries, opts.PgxPool),
		barcodeQueries:        pickQueries(opts.BarcodeQueries, opts.PgxPool),
		deliveryJobQueries:    pickQueries(opts.DeliveryJobQueries, opts.PgxPool),
		reportQueries:         pickQueries(opts.ReportQueries, opts.PgxPool),
		billingQueries:        pickQueries(opts.BillingQueries, opts.PgxPool),
		workerPool:            opts.WorkerPool,
		emailSender:           opts.EmailSender,
		stripeConnect:         opts.StripeConnect,
		stripeBilling:         opts.StripeBilling,
		sessionStore:          opts.SessionStore,
		maxConcurrentSessions: opts.MaxConcurrentSessionsPerUser,
		superadminQueries:     pickQueries(opts.SuperadminQueries, opts.PgxPool),
		allocationQueries:     pickQueries(opts.AllocationQueries, opts.PgxPool),
		complimentaryQueries:  pickQueries(opts.ComplimentaryQueries, opts.PgxPool),
		barcodeBatchQueries:   pickQueries(opts.BarcodeBatchQueries, opts.PgxPool),
		webhookSubQueries:     pickQueries(opts.WebhookSubQueries, opts.PgxPool),
		reconciliationQueries: pickQueries(opts.ReconciliationQueries, opts.PgxPool),
		networkQueries:        pickQueries(opts.NetworkQueries, opts.PgxPool),
	}

	s.mountOperationalRoutes()
	s.mountV1Routes()
	s.mountCompatRoutes()

	s.srv = &http.Server{
		Addr:              opts.Config.HTTPListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       opts.Config.RequestTimeout + 5*time.Second,
		WriteTimeout:      opts.Config.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	return s
}

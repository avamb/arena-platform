// Package httpserver wires the chi-based HTTP listener, standard middleware
// chain, and the operational and /v1 endpoints required by the foundation
// milestone.
//
// The server exposes:
//
//   - /healthz, /readyz       — operational probes (liveness + readiness)
//   - /v1/info                — service metadata + real SELECT against PG
//   - /v1/dev/token           — dev-only JWT mint (StubProvider, gated by ENABLE_DEV_AUTH)
//   - /v1/echo                — example transactional command (audit + outbox
//                                + idempotency, JWT-protected)
//
// Dev-only routes (/v1/dev/*, /v1/debug/*) are runtime-gated by ENABLE_DEV_AUTH
// and DEBUG_ROUTES_ENABLED respectively. They are not registered in the router
// when those environment variables are false (production default).
//
// Additional /v1 routes can be attached by later features through Router().
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
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
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pinger is the legacy readiness-probe contract kept for backward
// compatibility with database.Pool (IsHealthy / LastError). New code should
// prefer ReadinessProbe; Server wraps a Pinger in pingerProbe automatically.
type Pinger interface {
	IsHealthy() bool
	LastError() string
}

// ReadinessProbe is a named health check included in the /readyz response.
// Each probe corresponds to a single downstream dependency (e.g. "database",
// "redis"). Server iterates all registered probes and aggregates their results
// into the /readyz checks map; if any probe returns a non-nil error the
// response is 503.
type ReadinessProbe interface {
	// ProbeName returns the stable key used in the checks map.
	// Example values: "database", "redis", "outbox".
	ProbeName() string
	// Ping returns nil when the dependency is reachable, or any non-nil
	// error to indicate the dependency is unhealthy.
	Ping(ctx context.Context) error
}

// pingerProbe adapts the legacy Pinger interface to ReadinessProbe so callers
// that pass Options.DB continue to work without changes.
type pingerProbe struct {
	name string
	p    Pinger
}

func (pp *pingerProbe) ProbeName() string { return pp.name }
func (pp *pingerProbe) Ping(_ context.Context) error {
	if pp.p.IsHealthy() {
		return nil
	}
	msg := pp.p.LastError()
	if msg == "" {
		msg = "unhealthy"
	}
	return errors.New(msg)
}

// compile-time guard
var _ ReadinessProbe = (*pingerProbe)(nil)

// PoolDB is the narrow subset of *pgxpool.Pool consumed by /v1 handlers
// (info, echo). Defining it as an interface keeps the package testable —
// unit tests can supply a fake without spinning up PostgreSQL — while the
// production wiring still passes the real *pgxpool.Pool from database.Open.
type PoolDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Server is the long-lived HTTP listener that hosts the arena-api.
//
// All wired-in dependencies are nilable at construction time so tests can
// build a Server with only the pieces they need (e.g. a fake DB or a
// disabled auth stub). The route mounts guard against missing dependencies
// rather than panicking at startup.
type Server struct {
	cfg     *config.Config
	logger  *slog.Logger
	router  chi.Router
	srv     *http.Server
	probes  []ReadinessProbe
	pool    PoolDB
	stub    *auth.StubProvider
	audit   audit.Writer
	idem    idempotency.Store
	metrics       http.Handler
	typedMetrics  *observability.Metrics
	outboxWriter  outbox.Writer
	perms         permissions.Checker

	// clock provides the wall-clock time used by handleServerInfo (and any
	// future handler that needs deterministic time in tests). Defaults to
	// clock.New() (real system clock) when nil.
	clk clock.Clock

	// siQueries is the sqlc Queries instance used by handleServerInfo to
	// execute SelectServerTime. Nil when no PgxPool was supplied — in that
	// case handleServerInfo falls back to the server's clock.
	siQueries *gen.Queries

	// geoQueries is the sqlc Queries instance used by the geo reference
	// endpoints (GET /v1/geo/countries, GET /v1/geo/cities, and the admin
	// POST/PATCH endpoints). Nil when no PgxPool was supplied.
	geoQueries *gen.Queries

	// orgQueries is the sqlc Queries instance used by the organization CRUD
	// endpoints (POST/GET/PATCH/DELETE /v1/organizations). Nil when no
	// PgxPool was supplied. Feature #119.
	orgQueries *gen.Queries

	// channelQueries is the sqlc Queries instance used by the sales channel
	// CRUD endpoints (POST/GET/PATCH/DELETE /v1/organizations/{org_id}/channels).
	// Nil when no PgxPool was supplied. Feature #121.
	channelQueries *gen.Queries

	// membershipQueries is the sqlc Queries instance used by the membership
	// grant/revoke/list endpoints (POST/GET/DELETE /v1/organizations/{org_id}/members).
	// Nil when no PgxPool was supplied. Feature #120.
	membershipQueries *gen.Queries

	// venueQueries is the sqlc Queries instance used by the venue CRUD endpoints.
	// Read endpoints (GET /v1/venues, GET /v1/venues/{id}) are shared across orgs.
	// Write endpoints (POST/PATCH/DELETE /v1/organizations/{org_id}/venues/*) are
	// gated to the owning org. Nil when no PgxPool was supplied. Feature #124.
	venueQueries *gen.Queries

	// feedTokenQueries is the sqlc Queries instance used by the agent feed token
	// management endpoints and the public feed read endpoint.
	// Nil when no PgxPool was supplied. Feature #122.
	feedTokenQueries *gen.Queries

	// eventQueries is the sqlc Queries instance used by the event CRUD endpoints.
	// Read endpoints (GET /v1/events, GET /v1/events/{id}) are shared across orgs.
	// Write endpoints (POST/PATCH/DELETE /v1/organizations/{org_id}/events/*) are
	// gated to the owning org. Nil when no PgxPool was supplied. Feature #125.
	eventQueries *gen.Queries

	// publicationQueries is the sqlc Queries instance used by the event publication
	// endpoints (publish/unpublish/list). Nil when no PgxPool was supplied. Feature #151.
	publicationQueries *gen.Queries

	// publicFeedQueries is the sqlc Queries instance used by the unauthenticated
	// public feed event endpoints (GET /v1/public/feeds/{token}/events and
	// GET /v1/public/feeds/{token}/events/{event_id}). Feature #152.
	publicFeedQueries *gen.Queries

	// publicFeedRL is the in-memory rate limiter for public feed endpoints.
	// Limits: 100 req/min per token, 300 req/min per IP. Feature #152.
	publicFeedRL *publicFeedRateLimiter

	// sessionQueries is the sqlc Queries instance used by the session CRUD endpoints.
	// Sessions are scoped to an event. All write endpoints require the org_id in the
	// path to match the event's owning org (enforced via the event hierarchy).
	// Nil when no PgxPool was supplied. Feature #126.
	sessionQueries *gen.Queries

	// gdprQueries is the sqlc Queries instance used by the GDPR data subject request
	// endpoints (POST /v1/me/data-export, POST /v1/me/data-delete, GET /v1/me/data-requests,
	// POST /v1/me/consent). Nil when no PgxPool was supplied. Feature #164.
	gdprQueries *gen.Queries

	// tierQueries is the sqlc Queries instance used by the ticket tier CRUD endpoints.
	// Tiers are scoped to a session. All write endpoints require auth + tier permission.
	// Nil when no PgxPool was supplied. Feature #127.
	tierQueries *gen.Queries

	// inventoryQueries is the sqlc Queries instance used by the inventory ledger
	// endpoints and by the session capacity propagation hook.
	// Nil when no PgxPool was supplied. Feature #130.
	inventoryQueries *gen.Queries

	// reservationQueries is the sqlc Queries instance used by the reservation
	// endpoints and TTL processor. Nil when no PgxPool was supplied. Feature #131.
	reservationQueries *gen.Queries

	// promoQueries is the sqlc Queries instance used by the promo code endpoints.
	// Nil when no PgxPool was supplied. Feature #128.
	promoQueries *gen.Queries

	// pricingRules holds the platform fee and tax basis-point rates used by the
	// pricing pipeline (GET /v1/checkout/quote). Zero value is valid (all rates 0).
	// Feature #129.
	pricingRules PricingRules

	// checkoutQueries is the sqlc Queries instance used by the checkout session
	// state machine endpoints. Nil when no PgxPool was supplied. Feature #132.
	checkoutQueries *gen.Queries

	// paymentIntentQueries is the sqlc Queries instance used by the payment intent
	// state machine endpoints (SCA-aware, webhook idempotency). Nil when no
	// PgxPool was supplied. Feature #137.
	paymentIntentQueries *gen.Queries

	// ticketQueries is the sqlc Queries instance used by the ticket issuance
	// helper (issueTicketsForCheckout) and the GET /v1/checkout/{id}/tickets
	// read endpoint. Nil when no PgxPool was supplied. Feature #139.
	ticketQueries *gen.Queries

	// credentialQueries is the sqlc Queries instance used by the ticket
	// credential generation and retrieval endpoint
	// (GET /v1/tickets/{id}/credential). Nil when no PgxPool was supplied.
	// Feature #140.
	credentialQueries *gen.Queries

	// refundQueries is the sqlc Queries instance used by the refund state machine
	// endpoints (feature #138). Nil when no PgxPool was supplied.
	refundQueries *gen.Queries

	// barcodeQueries is the sqlc Queries instance used by the barcode authority
	// federation endpoints: authority management, barcode registration, and the
	// scan validation endpoint (POST /v1/scan). Nil when no PgxPool was supplied.
	// Feature #142.
	barcodeQueries *gen.Queries

	// reportQueries is the sqlc Queries instance used by the post-event report
	// generation endpoints (GET /v1/events/{id}/report, POST /v1/events/{id}/report).
	// Nil when no PgxPool was supplied. Feature #159.
	reportQueries *gen.Queries

	// billingQueries is the sqlc Queries instance used by the service billing
	// ledger endpoints (tariffs, usage_records, invoices, invoice_lines).
	// Nil when no PgxPool was supplied. Feature #161.
	billingQueries *gen.Queries

	// deliveryJobQueries is the sqlc Queries instance used to insert and update
	// delivery_jobs rows. When non-nil, issueTicketsForCheckout enqueues a
	// ticket.deliver worker job for each issued ticket that has a recipient email.
	// Nil when no PgxPool was supplied. Feature #141.
	deliveryJobQueries *gen.Queries

	// workerPool is the raw *pgxpool.Pool used to insert worker_jobs rows for
	// ticket.deliver jobs. Separate from the PoolDB interface (which may not
	// implement QueryRow with the same signature as pgxpool.Pool). When nil,
	// delivery job enqueueing is skipped. Feature #141.
	workerPool *pgxpool.Pool

	// emailSender is the email.Sender used by the delivery worker handler to
	// send transactional ticket emails. When nil, the delivery handler logs
	// the email instead of sending it (development/test mode). Feature #141.
	emailSender email.Sender

	// stripeConnect is the helper used by the Stripe Connect OAuth onboarding
	// endpoints (GET /v1/stripe/connect/authorize and …/callback). Nil when
	// Stripe Connect is not configured. When nil these routes are not mounted.
	// Feature #135.
	stripeConnect stripeConnectHelper

	// stripeBilling is the Stripe Billing adapter used by:
	//   POST /v1/billing/stripe/push-invoice/{id} — push SaaS invoice to Stripe
	//   POST /v1/billing/stripe/webhook           — receive Stripe Billing events
	// Nil when not configured. When nil these routes are not mounted. Feature #162.
	stripeBilling stripeBillingHelper

	// sessionStore is the Redis-backed store for refresh token tracking and fast
	// revocation lookups. Nil when Redis is not configured or the SessionStore
	// option was not supplied. When nil, session management degrades gracefully:
	// revocation is DB-only, concurrent-session limits are not enforced.
	// Feature #118.
	sessionStore redissession.Store

	// maxConcurrentSessions is the maximum number of simultaneous active refresh
	// token sessions permitted per user. 0 = unlimited (default). When > 0,
	// the oldest sessions are evicted on login to enforce the limit.
	// Feature #118.
	maxConcurrentSessions int

	// faultInjectOutboxAfterAudit is a dev/test-only fault injection flag.
	// When true, handleEcho forces a transaction rollback immediately after
	// the audit_events INSERT succeeds, before writing to outbox_events.
	// This proves that both writes are in the same transaction: neither row
	// persists when the fault fires. Enabled by FAULT_INJECT_OUTBOX_AFTER_AUDIT=true.
	faultInjectOutboxAfterAudit bool

	// slowDelay is the artificial sleep used by GET /v1/info-slow to simulate
	// long-running requests for graceful-shutdown testing. Defaults to 5s when
	// zero. Only meaningful in development/test environments.
	slowDelay time.Duration

	// debugRoutesEnabled controls whether the /v1/debug/* routes are mounted.
	// These routes exist solely to facilitate integration tests and developer
	// tooling. In particular, GET /v1/debug/panic intentionally panics to
	// exercise the Recoverer middleware. They MUST NOT be enabled in production.
	// Corresponds to env var DEBUG_ROUTES_ENABLED=true.
	debugRoutesEnabled bool

	// debugSlowDelay is the artificial sleep used by GET /v1/debug/slow to
	// simulate a long-running request for request-timeout testing. Defaults to
	// 35s when zero (longer than the 30s REQUEST_TIMEOUT_SECONDS default so the
	// timeout always fires in the default configuration). Only meaningful in
	// development/test environments and only when debugRoutesEnabled is true.
	debugSlowDelay time.Duration

	// bil24Enabled controls whether the /compat/bil24/* gateway subtree is
	// mounted. Disabled by default; set BIL24_COMPAT_ENABLED=true to enable.
	// Feature #157.
	bil24Enabled bool

	// superadminQueries is the sqlc Queries instance used by the platform
	// superadmin console endpoints (GET /v1/admin/organizations, /orders,
	// /tickets, /refunds). Nil when no PgxPool was supplied. Feature #166.
	superadminQueries *gen.Queries

	// allocationQueries is the sqlc Queries instance used by the external
	// allocation quota endpoints (POST/GET/PATCH /v1/organizations/{org_id}/external-allocations).
	// Nil when no PgxPool was supplied. Feature #145.
	allocationQueries *gen.Queries

	// complimentaryQueries is the sqlc Queries instance used by the complimentary
	// ticket issuance endpoints (POST/GET /v1/organizations/{org_id}/complimentary).
	// Nil when no PgxPool was supplied. Feature #148.
	complimentaryQueries *gen.Queries

	// barcodeBatchQueries is the sqlc Queries instance used by the barcode batch
	// import endpoints (upload CSV, approve/reject). Nil when no PgxPool was supplied.
	// Feature #146.
	barcodeBatchQueries *gen.Queries

	// webhookSubQueries is the sqlc Queries instance used by the webhook subscriber
	// management endpoints (POST/GET/DELETE /v1/webhooks/subscribers). Nil when no
	// PgxPool was supplied. Feature #156.
	webhookSubQueries *gen.Queries

	// reconciliationQueries is the sqlc Queries instance used by the external
	// reconciliation endpoints (POST /v1/reconciliation/reports, etc.).
	// Nil when no PgxPool was supplied. Feature #147.
	reconciliationQueries *gen.Queries
}

// Options bundles the dependencies that New requires. Using a struct rather
// than positional parameters keeps the constructor stable as more boundaries
// are bolted on by later features (PermissionBoundary, OutboxDispatcher, …).
type Options struct {
	Config *config.Config
	Logger *slog.Logger
	// DB carries the legacy Pinger contract used by /readyz. When non-nil it
	// is wrapped as a "database" ReadinessProbe and prepended to Probes.
	// Prefer Probes for new callers.
	DB Pinger
	// Probes is the ordered list of ReadinessProbe implementations whose
	// results are aggregated into the /readyz response. When empty and DB is
	// also nil, /readyz always returns 200 {checks:{}}.
	Probes []ReadinessProbe
	// Pool is the concrete pgxpool used by /v1 handlers. It is typically
	// the same *database.Pool passed as DB (database.Pool embeds *pgxpool.Pool
	// and exposes both contracts).
	Pool PoolDB
	// Auth is the dev-stub JWT provider. Pass nil to disable /v1/echo and
	// /v1/dev/token entirely.
	Auth *auth.StubProvider
	// Audit is the AuditWriter implementation. Defaults to a Postgres
	// writer constructed from a *pgxpool.Pool when Audit is nil and
	// PgxPool is non-nil.
	Audit audit.Writer
	// Idem is the idempotency Store implementation. Defaults to a Postgres
	// store constructed from PgxPool when Idem is nil and PgxPool is non-nil.
	Idem idempotency.Store
	// PgxPool is the concrete pool used to lazily construct PG-backed
	// Audit and Idem writers when those fields are not supplied. Optional.
	PgxPool *pgxpool.Pool
	// MetricsHandler is the Prometheus scrape handler exposed at /metrics.
	// When nil, the /metrics route is not mounted — useful for tests and for
	// deployments where metrics are scraped from a sidecar instead.
	MetricsHandler http.Handler
	// Metrics is the typed *observability.Metrics whose HTTP histogram +
	// counter back the prometheusMiddleware in the adapter chain. When nil
	// the middleware is omitted, so unit tests that don't care about
	// metrics can leave this unset without polluting a shared registry.
	Metrics *observability.Metrics
	// FaultInjectOutboxAfterAudit enables fault injection for transaction
	// atomicity testing. When true, handleEcho rolls back the transaction
	// after writing the audit_events row and before writing outbox_events,
	// returning 500 with code='internal.transaction_failed'. This proves
	// that both rows are in the same transaction (neither persists on fault).
	// Only meaningful in development/test environments.
	// Corresponds to env var FAULT_INJECT_OUTBOX_AFTER_AUDIT=true.
	FaultInjectOutboxAfterAudit bool

	// SlowDelay overrides the sleep duration used by GET /v1/info-slow.
	// Defaults to 5s when zero. Set to a small value in tests so graceful-
	// shutdown assertions complete quickly. Only meaningful in development/test.
	SlowDelay time.Duration

	// DebugRoutesEnabled mounts the /v1/debug/* routes when true. These routes
	// exist for integration tests and developer tooling. In particular,
	// GET /v1/debug/panic intentionally panics to exercise the Recoverer
	// middleware. MUST NOT be enabled in production.
	// Corresponds to env var DEBUG_ROUTES_ENABLED=true.
	DebugRoutesEnabled bool

	// DebugSlowDelay overrides the sleep duration used by GET /v1/debug/slow.
	// Defaults to 35s when zero (longer than the default 30s request timeout so
	// the timeout always fires in the default configuration). Set to a small value
	// in tests so request-timeout assertions complete quickly. Only meaningful in
	// development/test environments when DebugRoutesEnabled=true.
	DebugSlowDelay time.Duration

	// Bil24CompatEnabled mounts the /compat/bil24/* gateway subtree when true.
	// Disabled by default; set BIL24_COMPAT_ENABLED=true in the environment to
	// enable the legacy Bil24 command API compatibility layer (feature #157).
	// MUST remain false in production deployments unless explicitly required for
	// a migration window.
	Bil24CompatEnabled bool

	// SuperadminQueries injects a pre-constructed *gen.Queries for the platform
	// superadmin console endpoints (GET /v1/admin/organizations, /orders,
	// /tickets, /refunds). When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need superadmin routes mounted without a real pool.
	// Feature #166.
	SuperadminQueries *gen.Queries

	// AllocationQueries injects a pre-constructed *gen.Queries for the external
	// allocation quota endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need allocation routes mounted without a real pool.
	// Feature #145.
	AllocationQueries *gen.Queries

	// ComplimentaryQueries injects a pre-constructed *gen.Queries for the
	// complimentary ticket issuance endpoints
	// (POST/GET /v1/organizations/{org_id}/complimentary).
	// When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need complimentary routes mounted without a real pool.
	// Feature #148.
	ComplimentaryQueries *gen.Queries

	// BarcodeBatchQueries injects a pre-constructed *gen.Queries for the barcode
	// batch import endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need barcode batch routes mounted without a real pool.
	// Feature #146.
	BarcodeBatchQueries *gen.Queries

	// WebhookSubQueries injects a pre-constructed *gen.Queries for the webhook subscriber
	// management endpoints (POST/GET/DELETE /v1/webhooks/subscribers). When nil and
	// PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need webhook subscriber routes without a real pool.
	// Feature #156.
	WebhookSubQueries *gen.Queries

	// ReconciliationQueries injects a pre-constructed *gen.Queries for the external
	// reconciliation endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need reconciliation routes without a real pool.
	// Feature #147.
	ReconciliationQueries *gen.Queries

	// Bundle is the go-i18n/v2 message catalog bundle used by LocaleMiddleware
	// to localize error messages. When non-nil, LocaleMiddleware is added to
	// the middleware chain (after requestContext, before route handlers) so that
	// every request carries a locale-aware Localizer in its context.
	// When nil, locale negotiation still occurs (for the active_locale response
	// field in /v1/info) but error messages fall back to hardcoded English strings.
	Bundle *i18n.Bundle

	// Outbox is the outbox.Writer used by scanner event handlers (POST /v1/scan)
	// to publish domain events within a transaction.
	// When nil and PgxPool is non-nil, a PGWriter is constructed lazily.
	Outbox outbox.Writer

	// Permissions is the permissions.Checker used by authenticated write endpoints.
	// When nil, AllowAllChecker is used (foundation milestone placeholder).
	Permissions permissions.Checker

	// Clock overrides the time source used by handleServerInfo. When nil,
	// clock.New() (real system clock) is used. Inject clock.NewFake in tests
	// to make time deterministic.
	Clock clock.Clock

	// GeoQueries injects a pre-constructed *gen.Queries for the geo reference
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject an explicit value in tests that need geo routes mounted without a
	// real *pgxpool.Pool (e.g. passing gen.New(nil) to exercise auth guards).
	GeoQueries *gen.Queries

	// OrgQueries injects a pre-constructed *gen.Queries for the organization
	// CRUD endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need org routes mounted without a real pool.
	// Feature #119.
	OrgQueries *gen.Queries

	// ChannelQueries injects a pre-constructed *gen.Queries for the sales channel
	// CRUD endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need channel routes mounted without a real pool.
	// Feature #121.
	ChannelQueries *gen.Queries

	// MembershipQueries injects a pre-constructed *gen.Queries for the membership
	// grant/revoke/list endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need membership routes mounted without a real pool.
	// Feature #120.
	MembershipQueries *gen.Queries

	// VenueQueries injects a pre-constructed *gen.Queries for the venue CRUD endpoints.
	// When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need venue routes mounted without a real pool.
	// Feature #124.
	VenueQueries *gen.Queries

	// FeedTokenQueries injects a pre-constructed *gen.Queries for the agent feed
	// token management endpoints and the public feed read endpoint.
	// When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need feed token routes without a real pool.
	// Feature #122.
	FeedTokenQueries *gen.Queries

	// EventQueries injects a pre-constructed *gen.Queries for the event CRUD
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need event routes mounted without a real pool.
	// Feature #125.
	EventQueries *gen.Queries

	// PublicationQueries injects a pre-constructed *gen.Queries for the event
	// publication endpoints (publish/unpublish/list).
	// When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need publication routes mounted without a real pool.
	// Feature #151.
	PublicationQueries *gen.Queries

	// PublicFeedQueries injects a pre-constructed *gen.Queries for the public feed
	// event endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need public feed routes without a real pool.
	// Feature #152.
	PublicFeedQueries *gen.Queries

	// SessionQueries injects a pre-constructed *gen.Queries for the session CRUD
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need session routes mounted without a real pool.
	// Feature #126.
	SessionQueries *gen.Queries

	// GDPRQueries injects a pre-constructed *gen.Queries for the GDPR data subject
	// request endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need GDPR routes mounted without a real pool.
	// Feature #164.
	GDPRQueries *gen.Queries

	// TierQueries injects a pre-constructed *gen.Queries for the ticket tier CRUD
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need tier routes mounted without a real pool.
	// Feature #127.
	TierQueries *gen.Queries

	// InventoryQueries injects a pre-constructed *gen.Queries for the inventory
	// ledger endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need inventory routes mounted without a real pool.
	// Feature #130.
	InventoryQueries *gen.Queries

	// ReservationQueries injects a pre-constructed *gen.Queries for the reservation
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need reservation routes mounted without a real pool.
	// Feature #131.
	ReservationQueries *gen.Queries

	// PromoQueries injects a pre-constructed *gen.Queries for the promo code
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need promo routes mounted without a real pool.
	// Feature #128.
	PromoQueries *gen.Queries

	// PricingRules sets the platform fee and tax basis-point rates for the
	// pricing pipeline (GET /v1/checkout/quote). Zero value is valid (all rates 0).
	// Feature #129.
	PricingRules PricingRules

	// CheckoutQueries injects a pre-constructed *gen.Queries for the checkout
	// session state machine endpoints. When nil and PgxPool is non-nil,
	// gen.New(PgxPool) is used. Inject gen.New(nil) in tests that need checkout
	// routes mounted without a real pool. Feature #132.
	CheckoutQueries *gen.Queries

	// PaymentIntentQueries injects a pre-constructed *gen.Queries for the payment
	// intent state machine endpoints (SCA, webhook idempotency). When nil and
	// PgxPool is non-nil, gen.New(PgxPool) is used. Inject gen.New(nil) in tests
	// that need payment intent routes mounted without a real pool. Feature #137.
	PaymentIntentQueries *gen.Queries

	// TicketQueries injects a pre-constructed *gen.Queries for the ticket issuance
	// helper and GET /v1/checkout/{id}/tickets read endpoint. When nil and
	// PgxPool is non-nil, gen.New(PgxPool) is used. Inject gen.New(nil) in tests
	// that need ticket routes mounted without a real pool. Feature #139.
	TicketQueries *gen.Queries

	// CredentialQueries injects a pre-constructed *gen.Queries for the ticket
	// credential generation and retrieval endpoint
	// (GET /v1/tickets/{id}/credential). When nil and PgxPool is non-nil,
	// gen.New(PgxPool) is used. Inject gen.New(nil) in tests that need
	// credential routes mounted without a real pool. Feature #140.
	CredentialQueries *gen.Queries

	// RefundQueries injects a pre-constructed *gen.Queries for the refund state
	// machine endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need refund routes mounted without a real pool.
	// Feature #138.
	RefundQueries *gen.Queries

	// BarcodeQueries injects a pre-constructed *gen.Queries for the barcode
	// authority federation endpoints (authority management, barcode registration,
	// scan validation). When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need barcode routes mounted without a real pool.
	// Feature #142.
	BarcodeQueries *gen.Queries

	// ReportQueries injects a pre-constructed *gen.Queries for the post-event report
	// generation endpoints (GET/POST /v1/events/{id}/report). When nil and PgxPool
	// is non-nil, gen.New(PgxPool) is used. Inject gen.New(nil) in tests that need
	// report routes mounted without a real pool. Feature #159.
	ReportQueries *gen.Queries

	// BillingQueries injects a pre-constructed *gen.Queries for the service billing
	// ledger endpoints (tariffs, usage_records, invoices, invoice_lines). When nil
	// and PgxPool is non-nil, gen.New(PgxPool) is used. Inject gen.New(nil) in tests
	// that need billing routes mounted without a real pool. Feature #161.
	BillingQueries *gen.Queries

	// DeliveryJobQueries injects a pre-constructed *gen.Queries for the
	// delivery_jobs tracking table. When nil and PgxPool is non-nil,
	// gen.New(PgxPool) is used. Inject gen.New(nil) in tests that want
	// delivery routes wired without a real pool. Feature #141.
	DeliveryJobQueries *gen.Queries

	// WorkerPool is the *pgxpool.Pool used to enqueue ticket.deliver jobs into
	// worker_jobs. When nil, delivery job enqueueing is skipped gracefully.
	// Feature #141.
	WorkerPool *pgxpool.Pool

	// EmailSender is the email.Sender used by the delivery worker handler.
	// When nil, the handler writes the email to the logger instead of sending
	// it (development / test mode). Feature #141.
	EmailSender email.Sender

	// StripeConnect injects the Stripe Connect OAuth helper used by
	// GET /v1/stripe/connect/authorize and GET /v1/stripe/connect/callback.
	// When nil the Stripe Connect routes are not mounted.
	// In production, pass a *stripe.Adapter constructed from the platform's
	// Stripe credentials. In tests, pass a minimal stub that implements
	// stripeConnectHelper (defined in stripe_connect.go).
	// Feature #135.
	StripeConnect stripeConnectHelper

	// StripeBilling injects the Stripe Billing adapter used by:
	//   POST /v1/billing/stripe/push-invoice/{id} — push SaaS invoice to Stripe
	//   POST /v1/billing/stripe/webhook           — receive Stripe Billing events
	// When nil these routes are not mounted. In production pass a
	// *stripebilling.Adapter. In tests pass a minimal stub that implements
	// stripeBillingHelper (defined in stripe_billing.go). Feature #162.
	StripeBilling stripeBillingHelper

	// SessionStore injects the Redis-backed store used for refresh token tracking
	// and fast revocation checks (feature #118). When nil, session management
	// degrades gracefully: revocation is DB-only, concurrent-session limits are
	// not enforced. Inject a *redissession.MemStore in unit tests that want to
	// exercise session management without a real Redis instance.
	SessionStore redissession.Store

	// MaxConcurrentSessionsPerUser limits the number of simultaneous refresh
	// token sessions allowed per user. 0 = unlimited (default). When > 0,
	// the oldest sessions beyond the limit are revoked on every login.
	// Feature #118.
	MaxConcurrentSessionsPerUser int
}

// New constructs (but does not start) the HTTP server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Build the chi router via the adapter so the canonical middleware
	// chain (panicRecoverer → RealIP → RequestID → requestContext → logger →
	// prometheus → tracer → Timeout → bodyLimit) is applied uniformly
	// across every arena_new HTTP listener. The Server is responsible only
	// for the lifecycle (http.Server, listen, graceful shutdown) and for
	// mounting the operational + /v1 routes on the returned router.
	r := httpadapter.NewRouter(httpadapter.Deps{
		Logger:         logger,
		RequestTimeout: opts.Config.RequestTimeout,
		BodyLimitBytes: opts.Config.BodyLimitBytes,
		Metrics:        opts.Metrics,
		AppEnv:         string(opts.Config.AppEnv),
	})

	// Wire locale middleware when a Bundle is provided. The middleware runs
	// inside the existing chi router after all cross-cutting middlewares
	// (request_id, trace_id, logger) so the locale-negotiated Localizer is
	// available to every handler via i18n.Localize(r.Context(), ...).
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
	permsChecker := opts.Permissions
	if permsChecker == nil && opts.PgxPool != nil {
		// Wire the real RBAC engine when a PgxPool is available (feature #117).
		// The DBChecker reads roles/permissions/role_permissions created by
		// migration 0008_rbac and resolves permissions from the actor's JWT roles.
		permsChecker = permissions.NewDBChecker(gen.New(opts.PgxPool))
	} else if permsChecker == nil {
		// No pool → fall back to AllowAll for dev/test environments that run
		// without a database (e.g. unit tests with fake pool adapters).
		permsChecker = permissions.AllowAll()
	}

	// Clock defaults to the real system clock.
	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}

	// sqlc Queries for GET /v1/server-info — constructed from PgxPool when available.
	var siQueries *gen.Queries
	if opts.PgxPool != nil {
		siQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for geo reference endpoints.
	// Prefer the explicitly injected value (opts.GeoQueries) so tests can wire
	// a gen.New(nil) to exercise auth guards without a real *pgxpool.Pool.
	geoQueries := opts.GeoQueries
	if geoQueries == nil && opts.PgxPool != nil {
		geoQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for organization CRUD endpoints (feature #119).
	orgQueries := opts.OrgQueries
	if orgQueries == nil && opts.PgxPool != nil {
		orgQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for sales channel CRUD endpoints (feature #121).
	channelQueries := opts.ChannelQueries
	if channelQueries == nil && opts.PgxPool != nil {
		channelQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for membership grant/revoke/list endpoints (feature #120).
	membershipQueries := opts.MembershipQueries
	if membershipQueries == nil && opts.PgxPool != nil {
		membershipQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for venue CRUD endpoints (feature #124).
	venueQueries := opts.VenueQueries
	if venueQueries == nil && opts.PgxPool != nil {
		venueQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for agent feed token management endpoints (feature #122).
	feedTokenQueries := opts.FeedTokenQueries
	if feedTokenQueries == nil && opts.PgxPool != nil {
		feedTokenQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for event CRUD endpoints (feature #125).
	eventQueries := opts.EventQueries
	if eventQueries == nil && opts.PgxPool != nil {
		eventQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for event publication endpoints (feature #151).
	publicationQueries := opts.PublicationQueries
	if publicationQueries == nil && opts.PgxPool != nil {
		publicationQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for public feed event endpoints (feature #152).
	publicFeedQueries := opts.PublicFeedQueries
	if publicFeedQueries == nil && opts.PgxPool != nil {
		publicFeedQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for session CRUD endpoints (feature #126).
	sessionQueries := opts.SessionQueries
	if sessionQueries == nil && opts.PgxPool != nil {
		sessionQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for GDPR data subject request endpoints (feature #164).
	gdprQueries := opts.GDPRQueries
	if gdprQueries == nil && opts.PgxPool != nil {
		gdprQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for ticket tier CRUD endpoints (feature #127).
	tierQueries := opts.TierQueries
	if tierQueries == nil && opts.PgxPool != nil {
		tierQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for inventory ledger endpoints (feature #130).
	inventoryQueries := opts.InventoryQueries
	if inventoryQueries == nil && opts.PgxPool != nil {
		inventoryQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for reservation endpoints (feature #131).
	reservationQueries := opts.ReservationQueries
	if reservationQueries == nil && opts.PgxPool != nil {
		reservationQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for promo code endpoints (feature #128).
	promoQueries := opts.PromoQueries
	if promoQueries == nil && opts.PgxPool != nil {
		promoQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for checkout session state machine endpoints (feature #132).
	checkoutQueries := opts.CheckoutQueries
	if checkoutQueries == nil && opts.PgxPool != nil {
		checkoutQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for payment intent state machine endpoints (feature #137).
	paymentIntentQueries := opts.PaymentIntentQueries
	if paymentIntentQueries == nil && opts.PgxPool != nil {
		paymentIntentQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for ticket issuance and read endpoints (feature #139).
	ticketQueries := opts.TicketQueries
	if ticketQueries == nil && opts.PgxPool != nil {
		ticketQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for ticket credential generation and retrieval (feature #140).
	credentialQueries := opts.CredentialQueries
	if credentialQueries == nil && opts.PgxPool != nil {
		credentialQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for refund state machine endpoints (feature #138).
	refundQueries := opts.RefundQueries
	if refundQueries == nil && opts.PgxPool != nil {
		refundQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for barcode authority federation endpoints (feature #142).
	barcodeQueries := opts.BarcodeQueries
	if barcodeQueries == nil && opts.PgxPool != nil {
		barcodeQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for post-event report endpoints (feature #159).
	reportQueries := opts.ReportQueries
	if reportQueries == nil && opts.PgxPool != nil {
		reportQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for service billing ledger endpoints (feature #161).
	billingQueries := opts.BillingQueries
	if billingQueries == nil && opts.PgxPool != nil {
		billingQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for delivery_jobs table (feature #141).
	deliveryJobQueries := opts.DeliveryJobQueries
	if deliveryJobQueries == nil && opts.PgxPool != nil {
		deliveryJobQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for platform superadmin console endpoints (feature #166).
	superadminQueries := opts.SuperadminQueries
	if superadminQueries == nil && opts.PgxPool != nil {
		superadminQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for external allocation quota endpoints (feature #145).
	allocationQueries := opts.AllocationQueries
	if allocationQueries == nil && opts.PgxPool != nil {
		allocationQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for complimentary ticket issuance endpoints (feature #148).
	complimentaryQueries := opts.ComplimentaryQueries
	if complimentaryQueries == nil && opts.PgxPool != nil {
		complimentaryQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for barcode batch import endpoints (feature #146).
	barcodeBatchQueries := opts.BarcodeBatchQueries
	if barcodeBatchQueries == nil && opts.PgxPool != nil {
		barcodeBatchQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for webhook subscriber management endpoints (feature #156).
	webhookSubQueries := opts.WebhookSubQueries
	if webhookSubQueries == nil && opts.PgxPool != nil {
		webhookSubQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for external reconciliation endpoints (feature #147).
	reconciliationQueries := opts.ReconciliationQueries
	if reconciliationQueries == nil && opts.PgxPool != nil {
		reconciliationQueries = gen.New(opts.PgxPool)
	}

	// Extend the permission checker with membership-derived role resolution
	// (feature #120 step 3). When a PgxPool is available, the DBChecker is
	// augmented so that each Check() call unions the JWT roles with the user's
	// active membership roles fetched fresh from the DB. This makes grant/revoke
	// operations take effect on the next request without a new JWT.
	if dbChecker, ok := permsChecker.(*permissions.DBChecker); ok && opts.PgxPool != nil {
		permsChecker = dbChecker.WithMembershipQuerier(gen.New(opts.PgxPool))
	}

	// Assemble the readiness probe list.
	// If the legacy DB Pinger is set, prepend it as a "database" probe so
	// existing callers (main.go, integration tests) continue to work without
	// any changes at the call site.
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
		geoQueries:                  geoQueries,
		orgQueries:                  orgQueries,
		channelQueries:              channelQueries,
		membershipQueries:           membershipQueries,
		venueQueries:                venueQueries,
		feedTokenQueries:            feedTokenQueries,
		eventQueries:                eventQueries,
		publicationQueries:          publicationQueries,
		publicFeedQueries:           publicFeedQueries,
		publicFeedRL:                newPublicFeedRateLimiter(100, 300),
		sessionQueries:              sessionQueries,
		gdprQueries:                 gdprQueries,
		tierQueries:                 tierQueries,
		inventoryQueries:            inventoryQueries,
		reservationQueries:          reservationQueries,
		promoQueries:                promoQueries,
		pricingRules:                opts.PricingRules,
		checkoutQueries:             checkoutQueries,
		paymentIntentQueries:        paymentIntentQueries,
		ticketQueries:               ticketQueries,
		credentialQueries:           credentialQueries,
		refundQueries:               refundQueries,
		barcodeQueries:              barcodeQueries,
		deliveryJobQueries:          deliveryJobQueries,
		reportQueries:               reportQueries,
		billingQueries:              billingQueries,
		workerPool:                  opts.WorkerPool,
		emailSender:                 opts.EmailSender,
		stripeConnect:               opts.StripeConnect,
		stripeBilling:               opts.StripeBilling,
		sessionStore:                opts.SessionStore,
		maxConcurrentSessions:       opts.MaxConcurrentSessionsPerUser,
		superadminQueries:           superadminQueries,
		allocationQueries:           allocationQueries,
		complimentaryQueries:        complimentaryQueries,
		barcodeBatchQueries:         barcodeBatchQueries,
		webhookSubQueries:           webhookSubQueries,
		reconciliationQueries:       reconciliationQueries,
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

// Router exposes the underlying chi router so additional routes can be
// attached by later features.
func (s *Server) Router() chi.Router { return s.router }

// ListenAndServe starts the listener. Blocks until the underlying http.Server
// returns. http.ErrServerClosed signals a clean shutdown and should be
// treated as a non-error by the caller.
func (s *Server) ListenAndServe() error {
	s.logger.Info("http server listening", "addr", s.cfg.HTTPListenAddr)
	return s.srv.ListenAndServe()
}

// Shutdown attempts a graceful shutdown bounded by ctx.
// It logs "shutdown initiated" before stopping the listener and
// "shutdown complete" once all in-flight requests have drained.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutdown initiated")
	err := s.srv.Shutdown(ctx)
	s.logger.Info("shutdown complete")
	return err
}

// -----------------------------------------------------------------------------
// route mounts
// -----------------------------------------------------------------------------

func (s *Server) mountOperationalRoutes() {
	s.router.Get("/healthz", s.handleHealthz)
	s.router.Get("/readyz", s.handleReadyz)
	// /metrics is only mounted when the caller supplies a handler. The
	// scrape endpoint is intentionally unauthenticated for the foundation
	// milestone — Dokploy's reverse proxy enforces network-level
	// restriction (only the Prometheus scraper VLAN reaches it).
	if s.metrics != nil {
		s.router.Method(http.MethodGet, "/metrics", s.metrics)
	}
	// Custom 404 handler: returns the standard JSON error envelope instead of
	// chi's default plain-text "404 page not found\n" response. Every unknown
	// path therefore still carries Content-Type: application/json, X-Request-Id,
	// and the structured error body that clients can parse uniformly.
	s.router.NotFound(handleNotFound)
	// Custom 405 handler: when the path is known but the HTTP method is not
	// supported, chi populates the Allow response header (listing the methods
	// that ARE registered) and then calls this handler.  We keep the Allow
	// header intact and wrap the 405 in the standard JSON error envelope so
	// clients receive a parseable, machine-readable error body (feature #13).
	s.router.MethodNotAllowed(handleMethodNotAllowed)
}

func (s *Server) mountV1Routes() {
	s.router.Route("/v1", func(r chi.Router) {
		// Anonymous (or authenticated) routes
		r.Get("/info", s.handleInfo)
		// GET /v1/server-info — minimal public endpoint demonstrating the full
		// router → handler → sqlc → response chain (feature #104). No auth required.
		r.Get("/server-info", s.handleServerInfo)

		// Debug routes — only mounted when DEBUG_ROUTES_ENABLED=true. These
		// routes exist solely for integration tests and developer tooling; they
		// MUST NOT be enabled in production. The panic endpoint intentionally
		// triggers a panic to exercise the Recoverer middleware.
		if s.debugRoutesEnabled {
			r.Get("/debug/panic", s.handleDebugPanic)
			// GET /v1/debug/slow — sleeps for debugSlowDelay (default 35s) to
			// exercise the per-request timeout (feature #53). Returns 503 with
			// code='http.request_timeout' when the context deadline fires.
			r.Get("/debug/slow", s.handleDebugSlow)
		}

		// /v1/info-slow is a synthetic endpoint used to test graceful shutdown.
		// It sleeps for slowDelay (default 5s) before responding so integration
		// tests can verify that in-flight requests complete when SIGTERM is sent.
		// This endpoint is always mounted (not guarded by stub auth) because it
		// must be reachable without credentials during graceful-shutdown tests.
		r.Get("/info-slow", s.handleInfoSlow)

		// Dev-only token mint endpoints — only mounted when the stub provider is on.
		if s.stub != nil && s.stub.Enabled() {
			// /v1/dev/token — original endpoint using the manual HMAC issuer (StubProvider).
			r.Post("/dev/token", s.handleDevToken)
			// /v1/dev/auth/token — new endpoint using the jwt/v5-backed IssueJWT issuer
			// (AuthContext boundary placeholder, feature #96).
			r.Post("/dev/auth/token", s.handleDevAuthToken)
		}

		// Authenticated transactional routes (echo). Requires:
		//   - stub auth enabled (real IdP not in scope this milestone)
		//   - idempotency store wired
		//   - audit writer wired
		//   - pgx pool wired (echo writes audit + outbox in a single tx)
		if s.stub != nil && s.stub.Enabled() && s.idem != nil && s.audit != nil && s.pool != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				idemOpts := idempotency.Options{
					Scope: "POST /v1/echo",
					TTL:   24 * time.Hour,
					ActorID: func(ctx context.Context) string {
						if a, ok := auth.ActorFromContext(ctx); ok {
							return a.ID
						}
						return ""
					},
				}
				if s.typedMetrics != nil {
					idemOpts.OnReplay = func() {
						s.typedMetrics.IdempotencyReplaysTotal.Inc()
					}
				}
				pr.Use(idempotency.Middleware(s.idem, idemOpts))
				pr.Post("/echo", s.handleEcho)
			})
		}

		// POST /v1/auth/register              — public registration endpoint (feature #114).
		// GET  /v1/auth/verify                — email verification (feature #114).
		// POST /v1/auth/login                 — email+password → JWT + refresh token (feature #115).
		// POST /v1/auth/refresh               — refresh token → new JWT access token (feature #115).
		// POST /v1/auth/password-reset/request  — request a password-reset link (feature #116).
		// POST /v1/auth/password-reset/confirm  — confirm reset with token + new password (feature #116).
		// All require the pool to be wired; no auth header needed (they are
		// intentionally public endpoints used before or during authentication).
		if s.pool != nil {
			r.Post("/auth/register", s.handleAuthRegister)
			r.Get("/auth/verify", s.handleAuthVerifyEmail)
			r.Post("/auth/login", s.handleAuthLogin)
			r.Post("/auth/refresh", s.handleAuthRefresh)
			r.Post("/auth/password-reset/request", s.handleAuthPasswordResetRequest)
			r.Post("/auth/password-reset/confirm", s.handleAuthPasswordResetConfirm)
		}

		// POST /v1/auth/logout — JWT-protected; revokes the supplied refresh token
		// and removes it from the Redis session store (feature #118).
		// Requires stub auth + pool. Returns 204 on success (idempotent).
		if s.stub != nil && s.stub.Enabled() && s.pool != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Post("/auth/logout", s.handleAuthLogout)
			})
		}

		// ── Geo reference data (feature #123) ──────────────────────────────────
		//
		// Public read routes: no authentication required.
		//   GET /v1/geo/countries — list all countries with localized names
		//   GET /v1/geo/cities   — list cities (optional ?country_id= filter)
		//
		// Admin write routes: mounted only when stub auth + pool are available.
		//   POST  /v1/admin/geo/countries
		//   PATCH /v1/admin/geo/countries/{iso2}
		//   POST  /v1/admin/geo/cities
		//   PATCH /v1/admin/geo/cities/{id}
		if s.geoQueries != nil {
			r.Get("/geo/countries", s.handleListCountries)
			r.Get("/geo/cities", s.handleListCities)
		}
		if s.stub != nil && s.stub.Enabled() && s.geoQueries != nil && s.pool != nil {
			r.Route("/admin/geo", func(ar chi.Router) {
				ar.Group(func(pr chi.Router) {
					pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
					pr.Use(permissions.RequirePermission(s.perms, "geo.admin", "geo"))
					pr.Post("/countries", s.handleCreateCountry)
					pr.Patch("/countries/{iso2}", s.handleUpdateCountry)
					pr.Post("/cities", s.handleCreateCity)
					pr.Patch("/cities/{id}", s.handleUpdateCity)
				})
			})
		}

		// ── Organizations (feature #119) ──────────────────────────────────────
		//
		// All org endpoints require JWT auth. Write endpoints require specific
		// permissions. Read endpoints require "org.read" to keep the org
		// registry non-enumerable without authentication.
		//
		//   POST   /v1/organizations        — create (org.create)
		//   GET    /v1/organizations        — list   (org.read)
		//   GET    /v1/organizations/{id}   — get    (org.read)
		//   PATCH  /v1/organizations/{id}   — update (org.update)
		//   DELETE /v1/organizations/{id}   — delete (org.delete)
		//
		// Routes are registered directly (not via r.Route) to avoid trailing-slash
		// path canonicalization by chi. Each permission is enforced in a separate
		// group so GET and POST on the same base path can carry different permissions.
		if s.stub != nil && s.stub.Enabled() && s.orgQueries != nil && s.pool != nil {
			// GET /v1/organizations and GET /v1/organizations/{id} (org.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.read", "organizations"))
				pr.Get("/organizations", s.handleListOrgs)
				pr.Get("/organizations/{id}", s.handleGetOrg)
			})
			// POST /v1/organizations (org.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.create", "organizations"))
				pr.Post("/organizations", s.handleCreateOrg)
			})
			// PATCH /v1/organizations/{id} (org.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.update", "organizations"))
				pr.Patch("/organizations/{id}", s.handleUpdateOrg)
			})
			// DELETE /v1/organizations/{id} (org.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.delete", "organizations"))
				pr.Delete("/organizations/{id}", s.handleDeleteOrg)
			})
		}

		// ── Sales Channels (feature #121) ────────────────────────────────────
		//
		// All channel endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/channels so the
		// org scope is enforced at the path level and in every query.
		//
		//   POST   /v1/organizations/{org_id}/channels        — create (channel.create)
		//   GET    /v1/organizations/{org_id}/channels        — list   (channel.read)
		//   GET    /v1/organizations/{org_id}/channels/{id}   — get    (channel.read)
		//   PATCH  /v1/organizations/{org_id}/channels/{id}   — update (channel.update)
		//   DELETE /v1/organizations/{org_id}/channels/{id}   — delete (channel.delete)
		if s.stub != nil && s.stub.Enabled() && s.channelQueries != nil && s.pool != nil {
			// GET /v1/organizations/{org_id}/channels and GET …/{id} (channel.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.read", "channels"))
				pr.Get("/organizations/{org_id}/channels", s.handleListChannels)
				pr.Get("/organizations/{org_id}/channels/{id}", s.handleGetChannel)
			})
			// POST /v1/organizations/{org_id}/channels (channel.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.create", "channels"))
				pr.Post("/organizations/{org_id}/channels", s.handleCreateChannel)
			})
			// PATCH /v1/organizations/{org_id}/channels/{id} (channel.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.update", "channels"))
				pr.Patch("/organizations/{org_id}/channels/{id}", s.handleUpdateChannel)
			})
			// DELETE /v1/organizations/{org_id}/channels/{id} (channel.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.delete", "channels"))
				pr.Delete("/organizations/{org_id}/channels/{id}", s.handleDeleteChannel)
			})
		}

		// ── Memberships (feature #120) ──────────────────────────────────────
		//
		// All membership endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/members.
		//
		//   POST   /v1/organizations/{org_id}/members           — grant (membership.grant)
		//   GET    /v1/organizations/{org_id}/members           — list  (membership.read)
		//   DELETE /v1/organizations/{org_id}/members/{user_id} — revoke (membership.revoke)
		if s.stub != nil && s.stub.Enabled() && s.membershipQueries != nil && s.pool != nil {
			// GET /v1/organizations/{org_id}/members (membership.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "membership.read", "memberships"))
				pr.Get("/organizations/{org_id}/members", s.handleListMembers)
			})
			// POST /v1/organizations/{org_id}/members (membership.grant)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "membership.grant", "memberships"))
				pr.Post("/organizations/{org_id}/members", s.handleGrantMembership)
			})
			// DELETE /v1/organizations/{org_id}/members/{user_id} (membership.revoke)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "membership.revoke", "memberships"))
				pr.Delete("/organizations/{org_id}/members/{user_id}", s.handleRevokeMembership)
			})
		}

		// ── Venues (feature #124) ────────────────────────────────────────────
		//
		// Read endpoints are shared across all organizations (any authenticated
		// user with venue.read can list/get any active venue). Write endpoints
		// are owner-gated: the org_id in the path must match the venue's owning org.
		//
		//   POST   /v1/organizations/{org_id}/venues        — create (venue.create)
		//   GET    /v1/venues                               — list all (venue.read, shared)
		//   GET    /v1/venues/{id}                          — get by ID (venue.read, shared)
		//   GET    /v1/organizations/{org_id}/venues        — list by org (venue.read)
		//   PATCH  /v1/organizations/{org_id}/venues/{id}   — update (venue.update, owner only)
		//   DELETE /v1/organizations/{org_id}/venues/{id}   — soft-delete (venue.delete, owner only)
		if s.stub != nil && s.stub.Enabled() && s.venueQueries != nil {
			// Shared read routes (venue.read) — any org can read any venue.
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.read", "venues"))
				pr.Get("/venues", s.handleListVenues)
				pr.Get("/venues/{id}", s.handleGetVenue)
				pr.Get("/organizations/{org_id}/venues", s.handleListVenuesByOrg)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.venueQueries != nil && s.pool != nil {
			// POST /v1/organizations/{org_id}/venues (venue.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.create", "venues"))
				pr.Post("/organizations/{org_id}/venues", s.handleCreateVenue)
			})
			// PATCH /v1/organizations/{org_id}/venues/{id} (venue.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.update", "venues"))
				pr.Patch("/organizations/{org_id}/venues/{id}", s.handleUpdateVenue)
			})
			// DELETE /v1/organizations/{org_id}/venues/{id} (venue.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.delete", "venues"))
				pr.Delete("/organizations/{org_id}/venues/{id}", s.handleDeleteVenue)
			})
		}

		// ── Agent Feed Tokens (feature #122) ────────────────────────────────
		//
		// Management endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens.
		//
		//   POST   .../feed-tokens        — issue token (feed_token.create)
		//   GET    .../feed-tokens        — list tokens (feed_token.read)
		//   GET    .../feed-tokens/{id}   — get single  (feed_token.read)
		//   DELETE .../feed-tokens/{id}   — revoke token (feed_token.delete)
		//
		// Public feed read endpoint (no JWT required):
		//   GET /v1/feeds/{token} — validates token, updates last_used_at
		if s.feedTokenQueries != nil {
			// Public feed read (no auth — token in path is the credential).
			r.Get("/feeds/{token}", s.handlePublicFeed)
		}
		if s.stub != nil && s.stub.Enabled() && s.feedTokenQueries != nil {
			// GET .../feed-tokens and GET .../feed-tokens/{id} (feed_token.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "feed_token.read", "feed_tokens"))
				pr.Get("/organizations/{org_id}/channels/{channel_id}/feed-tokens", s.handleListFeedTokens)
				pr.Get("/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", s.handleGetFeedToken)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.feedTokenQueries != nil && s.pool != nil {
			// POST .../feed-tokens (feed_token.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "feed_token.create", "feed_tokens"))
				pr.Post("/organizations/{org_id}/channels/{channel_id}/feed-tokens", s.handleCreateFeedToken)
			})
			// DELETE .../feed-tokens/{id} (feed_token.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "feed_token.delete", "feed_tokens"))
				pr.Delete("/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", s.handleRevokeFeedToken)
			})
		}

		// ── Events (feature #125) ────────────────────────────────────────────
		//
		// Read endpoints are shared across all organizations (any authenticated
		// user with event.read can list/get any active event). Write endpoints
		// are owner-gated: the org_id in the path must match the event's owning org.
		//
		//   POST   /v1/organizations/{org_id}/events            — create (event.create)
		//   GET    /v1/events                                   — list public events (event.read, shared)
		//   GET    /v1/events/{id}                              — get by ID (event.read, shared)
		//   GET    /v1/organizations/{org_id}/events            — list by org (event.read)
		//   PATCH  /v1/organizations/{org_id}/events/{id}       — update (event.update, owner only)
		//   POST   /v1/organizations/{org_id}/events/{id}/status — status transition (event.publish)
		//   DELETE /v1/organizations/{org_id}/events/{id}       — soft-delete (event.delete, owner only)
		if s.stub != nil && s.stub.Enabled() && s.eventQueries != nil {
			// Shared read routes (event.read) — any org can read any active event.
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.read", "events"))
				pr.Get("/events", s.handleListEvents)
				pr.Get("/events/{id}", s.handleGetEvent)
				pr.Get("/organizations/{org_id}/events", s.handleListEventsByOrg)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.eventQueries != nil && s.pool != nil {
			// POST /v1/organizations/{org_id}/events (event.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.create", "events"))
				pr.Post("/organizations/{org_id}/events", s.handleCreateEvent)
			})
			// PATCH /v1/organizations/{org_id}/events/{id} (event.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.update", "events"))
				pr.Patch("/organizations/{org_id}/events/{id}", s.handleUpdateEvent)
			})
			// POST /v1/organizations/{org_id}/events/{id}/status (event.publish)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.publish", "events"))
				pr.Post("/organizations/{org_id}/events/{id}/status", s.handleUpdateEventStatus)
			})
			// DELETE /v1/organizations/{org_id}/events/{id} (event.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.delete", "events"))
				pr.Delete("/organizations/{org_id}/events/{id}", s.handleDeleteEvent)
			})
		}

		// ── Sessions (feature #126) ─────────────────────────────────────────
		//
		// Sessions are time slots for an event with independent inventory.
		// All session endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/events/{event_id}/sessions
		// so both the org scope and event scope are enforced at the path level.
		//
		//   POST   .../sessions        — create (session.create)
		//   GET    .../sessions        — list   (session.read)
		//   GET    .../sessions/{id}   — get    (session.read)
		//   PATCH  .../sessions/{id}   — update (session.update)
		//   DELETE .../sessions/{id}   — delete (session.delete)
		if s.stub != nil && s.stub.Enabled() && s.sessionQueries != nil {
			// GET .../sessions and GET .../sessions/{id} (session.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "session.read", "sessions"))
				pr.Get("/organizations/{org_id}/events/{event_id}/sessions", s.handleListSessions)
				pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{id}", s.handleGetSession)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.sessionQueries != nil && s.pool != nil {
			// POST .../sessions (session.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "session.create", "sessions"))
				pr.Post("/organizations/{org_id}/events/{event_id}/sessions", s.handleCreateSession)
			})
			// PATCH .../sessions/{id} (session.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "session.update", "sessions"))
				pr.Patch("/organizations/{org_id}/events/{event_id}/sessions/{id}", s.handleUpdateSession)
			})
			// DELETE .../sessions/{id} (session.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "session.delete", "sessions"))
				pr.Delete("/organizations/{org_id}/events/{event_id}/sessions/{id}", s.handleDeleteSession)
			})
		}

		// ── Ticket Tiers (feature #127) ─────────────────────────────────────
		//
		// Ticket tiers define pricing options within a session.
		// All tier endpoints require JWT auth + a named permission.
		// Routes are nested under .../sessions/{session_id}/tiers so both
		// the org, event, and session scopes are enforced at the path level.
		//
		//   POST   .../tiers        — create (tier.create)
		//   GET    .../tiers        — list   (tier.read)
		//   GET    .../tiers/{id}   — get    (tier.read)
		//   PATCH  .../tiers/{id}   — update (tier.update)
		//   DELETE .../tiers/{id}   — delete (tier.delete)
		if s.stub != nil && s.stub.Enabled() && s.tierQueries != nil {
			// GET .../tiers and GET .../tiers/{id} (tier.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "tier.read", "tiers"))
				pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers", s.handleListTiers)
				pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", s.handleGetTier)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.tierQueries != nil && s.pool != nil {
			// POST .../tiers (tier.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "tier.create", "tiers"))
				pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers", s.handleCreateTier)
			})
			// PATCH .../tiers/{id} (tier.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "tier.update", "tiers"))
				pr.Patch("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", s.handleUpdateTier)
			})
			// DELETE .../tiers/{id} (tier.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "tier.delete", "tiers"))
				pr.Delete("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", s.handleDeleteTier)
			})
		}

		// ── Inventory Ledger (feature #130) ──────────────────────────────────────
		//
		// GA capacity tracking for sessions and their tiers.
		// Endpoints are nested under the session path:
		//
		//   GET  .../inventory          — list all ledger rows (inventory.read)
		//   POST .../inventory          — initialise ledger entry (inventory.reserve)
		//   POST .../inventory/reserve  — reserve capacity (inventory.reserve)
		//   POST .../inventory/release  — release held capacity (inventory.release)
		//   POST .../inventory/confirm  — confirm held → sold (inventory.confirm)
		if s.stub != nil && s.stub.Enabled() && s.inventoryQueries != nil {
			// GET .../inventory (inventory.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "inventory.read", "inventory"))
				pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory", s.handleListInventory)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.inventoryQueries != nil && s.pool != nil {
			// POST .../inventory (init — inventory.reserve)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "inventory.reserve", "inventory"))
				pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory", s.handleInitInventory)
			})
			// POST .../inventory/reserve (inventory.reserve)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "inventory.reserve", "inventory"))
				pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/reserve", s.handleReserveCapacity)
			})
			// POST .../inventory/release (inventory.release)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "inventory.release", "inventory"))
				pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/release", s.handleReleaseCapacity)
			})
			// POST .../inventory/confirm (inventory.confirm)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "inventory.confirm", "inventory"))
				pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/confirm", s.handleConfirmCapacity)
			})
		}

		// ── Reservations (feature #131) ─────────────────────────────────────
		//
		// Reservation state machine endpoints. All require JWT auth.
		//
		//   POST   /v1/reservations              — create draft (reservation.create)
		//   GET    /v1/reservations/{id}         — get by ID (reservation.read)
		//   PATCH  /v1/reservations/{id}/activate — draft → active (reservation.activate)
		//   DELETE /v1/reservations/{id}         — cancel (reservation.cancel)
		if s.stub != nil && s.stub.Enabled() && s.reservationQueries != nil {
			// GET /v1/reservations/{id} (reservation.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "reservation.read", "reservations"))
				pr.Get("/reservations/{id}", s.handleGetReservation)
			})
			// PATCH /v1/reservations/{id}/activate (reservation.activate)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "reservation.activate", "reservations"))
				pr.Patch("/reservations/{id}/activate", s.handleActivateReservation)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.reservationQueries != nil && s.inventoryQueries != nil && s.pool != nil {
			// POST /v1/reservations (reservation.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "reservation.create", "reservations"))
				pr.Post("/reservations", s.handleCreateReservation)
			})
			// DELETE /v1/reservations/{id} (reservation.cancel)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "reservation.cancel", "reservations"))
				pr.Delete("/reservations/{id}", s.handleCancelReservation)
			})
		}

		// ── Event Publications (feature #151) ────────────────────────────────
		//
		// Manage which agent feed tokens an event is published to.  Mirrors
		// the legacy Bil24 Subscriptions panel for migration compatibility.
		//
		//   POST   /v1/events/{event_id}/publications                        — publish (publication.create)
		//   DELETE /v1/events/{event_id}/publications/{feed_token_id}        — unpublish (publication.delete)
		//   GET    /v1/events/{event_id}/publications                        — list (publication.read)
		if s.stub != nil && s.stub.Enabled() && s.publicationQueries != nil {
			// GET /v1/events/{event_id}/publications (publication.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "publication.read", "publications"))
				pr.Get("/events/{event_id}/publications", s.handleListPublications)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.publicationQueries != nil && s.pool != nil {
			// POST /v1/events/{event_id}/publications (publication.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "publication.create", "publications"))
				pr.Post("/events/{event_id}/publications", s.handlePublishEvent)
			})
			// DELETE /v1/events/{event_id}/publications/{feed_token_id} (publication.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "publication.delete", "publications"))
				pr.Delete("/events/{event_id}/publications/{feed_token_id}", s.handleUnpublishEvent)
			})
		}

		// ── Promo Codes (feature #128) ────────────────────────────────────────────
		//
		// Discount codes scoped to an organization, with checkout validation.
		//
		//   POST   /v1/organizations/{org_id}/promo-codes        — create (promo.create)
		//   GET    /v1/organizations/{org_id}/promo-codes        — list   (promo.read)
		//   GET    /v1/organizations/{org_id}/promo-codes/{id}   — get    (promo.read)
		//   PATCH  /v1/organizations/{org_id}/promo-codes/{id}   — update (promo.update)
		//   DELETE /v1/organizations/{org_id}/promo-codes/{id}   — delete (promo.delete)
		//   POST   /v1/checkout/promo-validate                   — validate (promo.validate)
		if s.stub != nil && s.stub.Enabled() && s.promoQueries != nil {
			// GET routes (promo.read) — list and get single
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "promo.read", "promo-codes"))
				pr.Get("/organizations/{org_id}/promo-codes", s.handleListPromoCodes)
				pr.Get("/organizations/{org_id}/promo-codes/{id}", s.handleGetPromoCode)
			})
			// POST /v1/checkout/promo-validate (promo.validate)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "promo.validate", "promo-codes"))
				pr.Post("/checkout/promo-validate", s.handleValidatePromoCode)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.promoQueries != nil && s.pool != nil {
			// POST /v1/organizations/{org_id}/promo-codes (promo.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "promo.create", "promo-codes"))
				pr.Post("/organizations/{org_id}/promo-codes", s.handleCreatePromoCode)
			})
			// PATCH /v1/organizations/{org_id}/promo-codes/{id} (promo.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "promo.update", "promo-codes"))
				pr.Patch("/organizations/{org_id}/promo-codes/{id}", s.handleUpdatePromoCode)
			})
			// DELETE /v1/organizations/{org_id}/promo-codes/{id} (promo.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "promo.delete", "promo-codes"))
				pr.Delete("/organizations/{org_id}/promo-codes/{id}", s.handleDeletePromoCode)
			})
		}

		// ── Pricing calculator (feature #129) ────────────────────────────────────
		//
		// Returns an itemized price quote for a ticket tier + optional promo code.
		// Pure read endpoint; does not write to the database.
		//
		//   GET /v1/checkout/quote  — quote (pricing.quote)
		//     Query params: tier_id, session_id, quantity, org_id, promo_code (opt),
		//                   chosen_price (opt, pwyw tiers only)
		if s.stub != nil && s.stub.Enabled() && s.tierQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "pricing.quote", "checkout"))
				pr.Get("/checkout/quote", s.handleQuote)
			})
		}

		// ── Checkout sessions (feature #132) + price breakdown (feature #163) ──────
		//
		// Checkout session state machine: reservation + pricing + payment intent.
		//
		//   POST /v1/checkout/start                    — create session      (checkout.start)
		//   GET  /v1/checkout/{id}                     — read session        (checkout.read)
		//   GET  /v1/checkout/{id}/price-breakdown     — itemised total       (checkout.read)
		//   POST /v1/checkout/{id}/confirm             — lock in pricing     (checkout.confirm)
		//   POST /v1/checkout/{id}/complete            — mark paid           (checkout.complete)
		//   POST /v1/checkout/{id}/abandon             — abandon session     (checkout.abandon)
		if s.stub != nil && s.stub.Enabled() && s.checkoutQueries != nil {
			// Read routes (no pool required).
			//   GET /v1/checkout/{id}                  — session state (checkout.read)
			//   GET /v1/checkout/{id}/price-breakdown  — itemised pricing (checkout.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "checkout.read", "checkout"))
				pr.Get("/checkout/{id}", s.handleGetCheckoutSession)
				pr.Get("/checkout/{id}/price-breakdown", s.handlePriceBreakdown)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.checkoutQueries != nil && s.pool != nil {
			// POST /v1/checkout/start (checkout.start)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "checkout.start", "checkout"))
				pr.Post("/checkout/start", s.handleStartCheckout)
			})
			// POST /v1/checkout/{id}/confirm (checkout.confirm)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "checkout.confirm", "checkout"))
				pr.Post("/checkout/{id}/confirm", s.handleConfirmCheckout)
			})
			// POST /v1/checkout/{id}/complete (checkout.complete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "checkout.complete", "checkout"))
				pr.Post("/checkout/{id}/complete", s.handleCompleteCheckout)
			})
			// POST /v1/checkout/{id}/abandon (checkout.abandon)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "checkout.abandon", "checkout"))
				pr.Post("/checkout/{id}/abandon", s.handleAbandonCheckout)
			})
		}

		// ── Payment Intents (feature #137) ──────────────────────────────────
		//
		// SCA-aware payment intent state machine with webhook idempotency.
		//
		//   POST /v1/payment-intents                     — create (payment_intent.create)
		//   GET  /v1/payment-intents/{id}                — read   (payment_intent.read)
		//   POST /v1/payment-intents/{id}/transition     — advance state (payment_intent.update)
		//   POST /v1/payment-intents/webhook             — provider webhook (no JWT)
		if s.paymentIntentQueries != nil {
			// Webhook — intentionally unauthenticated; providers deliver these
			// from their own infrastructure. Idempotency handled internally.
			r.Post("/payment-intents/webhook", s.handlePaymentIntentWebhook)
		}
		if s.stub != nil && s.stub.Enabled() && s.paymentIntentQueries != nil {
			// GET /v1/payment-intents/{id} (payment_intent.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "payment_intent.read", "payment_intents"))
				pr.Get("/payment-intents/{id}", s.handleGetPaymentIntent)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.paymentIntentQueries != nil && s.pool != nil {
			// POST /v1/payment-intents (payment_intent.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "payment_intent.create", "payment_intents"))
				pr.Post("/payment-intents", s.handleCreatePaymentIntent)
			})
			// POST /v1/payment-intents/{id}/transition (payment_intent.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "payment_intent.update", "payment_intents"))
				pr.Post("/payment-intents/{id}/transition", s.handleTransitionPaymentIntent)
			})
		}

		// ── Stripe Connect OAuth onboarding (feature #135) ─────────────────
		//
		// Direct-merchant channels require organizers to connect their Stripe
		// account via Stripe Connect Standard OAuth.
		//
		//   GET /v1/stripe/connect/authorize — build OAuth URL (payment_intent.create)
		//   GET /v1/stripe/connect/callback  — exchange code for account ID
		//
		// Routes are only mounted when StripeConnect is wired. The drift-test
		// server does not wire StripeConnect, so these routes are absent from the
		// chi.Walk output and from openapi.yaml (same convention as checkout/payment
		// routes that are not yet in the spec).
		if s.stripeConnect != nil && s.stub != nil && s.stub.Enabled() {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "payment_intent.create", "stripe_connect"))
				pr.Get("/stripe/connect/authorize", s.handleStripeConnectAuthorize)
				pr.Get("/stripe/connect/callback", s.handleStripeConnectCallback)
			})
		}

		// ── Tickets (feature #139) ──────────────────────────────────────────
		//
		// Read endpoint for issued tickets. Tickets are issued automatically when
		// a payment succeeds (webhook) or a free checkout is completed. There is
		// no create endpoint exposed directly — issuance is triggered internally.
		//
		//   GET /v1/checkout/{id}/tickets — list issued tickets (ticket.read)
		if s.stub != nil && s.stub.Enabled() && s.ticketQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "ticket.read", "tickets"))
				pr.Get("/checkout/{id}/tickets", s.handleListTickets)
			})
		}

		// ── Ticket credentials (feature #140) ───────────────────────────────
		//
		// Lazy-generate and return bearer credentials for an issued ticket.
		// The credential is created on first access and stored in the DB.
		// Subsequent requests return the cached credential.
		//
		//   GET /v1/tickets/{id}/credential?type=static_qr  (default)
		//   GET /v1/tickets/{id}/credential?type=pdf
		if s.stub != nil && s.stub.Enabled() && s.credentialQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "credential.read", "credentials"))
				pr.Get("/tickets/{id}/credential", s.handleGetCredential)
			})
		}

		// ── GDPR data subject requests (feature #164) ───────────────────────
		//
		// Self-service GDPR endpoints. All require JWT auth.
		// The authenticated user can only access their own data.
		//
		//   POST /v1/me/data-export     — queue an export request (202 Accepted)
		//   POST /v1/me/data-delete     — queue a deletion request (202 Accepted)
		//   GET  /v1/me/data-requests   — list the user's own GDPR requests
		//   POST /v1/me/consent         — record consent (at registration or update)
		if s.stub != nil && s.stub.Enabled() && s.gdprQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "gdpr.request", "gdpr"))
				pr.Get("/me/data-requests", s.handleListDataRequests)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.gdprQueries != nil && s.pool != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "gdpr.request", "gdpr"))
				pr.Post("/me/data-export", s.handleDataExportRequest)
				pr.Post("/me/data-delete", s.handleDataDeleteRequest)
				pr.Post("/me/consent", s.handleRecordConsent)
			})
		}

		// ── Refunds (feature #138) ──────────────────────────────────────────
		//
		// Refund state machine: requested → approved → provider_pending →
		// succeeded|failed|manual_review. Also: requested → rejected (terminal).
		// Ticket revocation fires on webhook succeeded event.
		//
		//   POST /v1/refunds                  — create refund request (refund.create)
		//   GET  /v1/refunds/{id}             — read refund state (refund.read)
		//   POST /v1/refunds/{id}/approve     — approve refund (refund.approve)
		//   POST /v1/refunds/{id}/reject      — reject refund (refund.approve)
		//   POST /v1/refunds/webhook          — provider webhook (no JWT)
		//
		// Routes not mounted in the drift-test server (refundQueries not wired there)
		// and therefore not in openapi.yaml (following the payment-intents webhook
		// and stripe-connect convention for provider-integration routes).
		if s.refundQueries != nil {
			// Webhook — intentionally unauthenticated.
			r.Post("/refunds/webhook", s.handleRefundWebhook)
		}
		if s.stub != nil && s.stub.Enabled() && s.refundQueries != nil {
			// GET /v1/refunds/{id} (refund.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "refund.read", "refunds"))
				pr.Get("/refunds/{id}", s.handleGetRefund)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.refundQueries != nil && s.pool != nil {
			// POST /v1/refunds (refund.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "refund.create", "refunds"))
				pr.Post("/refunds", s.handleCreateRefund)
			})
			// POST /v1/refunds/{id}/approve and …/{id}/reject (refund.approve)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "refund.approve", "refunds"))
				pr.Post("/refunds/{id}/approve", s.handleApproveRefund)
				pr.Post("/refunds/{id}/reject", s.handleRejectRefund)
			})
		}

		// ── Barcode authority federation (feature #142) ────────────────────
		//
		// Multi-system barcode validation. Each barcode belongs to one authority
		// (platform, legacy_bil24, external_platform, or guest_list). The scan
		// endpoint resolves the authority context first; unknown types are rejected
		// before any barcode lookup occurs.
		//
		//   POST   /v1/barcodes/authorities   — create authority (barcode.create)
		//   GET    /v1/barcodes/authorities   — list authorities (barcode.read)
		//   POST   /v1/barcodes               — register barcode (barcode.create)
		//   GET    /v1/barcodes/{id}          — read barcode    (barcode.read)
		//   DELETE /v1/barcodes/{id}          — revoke barcode  (barcode.revoke)
		//   POST   /v1/scan                   — scan validation (barcode.scan)
		if s.stub != nil && s.stub.Enabled() && s.barcodeQueries != nil {
			// Read routes (barcode.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode.read", "barcodes"))
				pr.Get("/barcodes/authorities", s.handleListBarcodeAuthorities)
				pr.Get("/barcodes/{id}", s.handleGetBarcode)
			})
			// Scan route (barcode.scan)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode.scan", "barcodes"))
				pr.Post("/scan", s.handleScan)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.barcodeQueries != nil && s.pool != nil {
			// Create authority (barcode.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode.create", "barcodes"))
				pr.Post("/barcodes/authorities", s.handleCreateBarcodeAuthority)
				pr.Post("/barcodes", s.handleRegisterBarcode)
			})
			// Revoke barcode (barcode.revoke)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode.revoke", "barcodes"))
				pr.Delete("/barcodes/{id}", s.handleRevokeBarcode)
			})
		}

		// ── Offline scanner snapshot + online validate (feature #144) ───────────
		//
		// Offline scanners download a barcode snapshot for an event session so they
		// can admit tickets without network connectivity. On reconnect they switch
		// back to the online validate endpoint for real-time checks.
		//
		//   GET  /v1/scanner/snapshot   — paginated snapshot with since-cursor delta (barcode.scan)
		//   POST /v1/scanner/validate   — read-only barcode validity check (barcode.scan)
		if s.stub != nil && s.stub.Enabled() && s.barcodeQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode.scan", "barcodes"))
				pr.Get("/scanner/snapshot", s.handleScannerSnapshot)
				pr.Post("/scanner/validate", s.handleScannerValidate)
			})
		}

		// ── Post-event reports (feature #159) ──────────────────────────────────
		//
		// Report endpoints. Auth + permission enforced by route-level middleware.
		//
		//   GET  /v1/events/{event_id}/report  — read latest report + lines (report.read)
		//   POST /v1/events/{event_id}/report  — trigger on-demand generation (report.generate)
		if s.stub != nil && s.stub.Enabled() && s.reportQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "report.read", "reports"))
				pr.Get("/events/{event_id}/report", s.handleGetEventReport)
			})
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "report.generate", "reports"))
				pr.Post("/events/{event_id}/report", s.handleTriggerEventReport)
			})
		}

		// ── Public feed events API (feature #152) ──────────────────────────────
		//
		// Unauthenticated read-only endpoints. The feed token in the path is
		// the credential (ADR-013 federated feeds). No JWT required.
		//
		//   GET  /v1/public/feeds/{feed_token}/events              — list events
		//   GET  /v1/public/feeds/{feed_token}/events/{event_id}   — detail + tiers
		//
		// Rate limit: 100 req/min per token, 300 req/min per IP.
		// Cache-Control set per response (60s for list, 30s for detail).
		if s.publicFeedQueries != nil {
			r.Get("/public/feeds/{feed_token}/events", s.handlePublicFeedEvents)
			r.Get("/public/feeds/{feed_token}/events/{event_id}", s.handlePublicFeedEvent)
		}

		// ── Public feed checkout start (feature #153) ────────────────────────────
		//
		// Unauthenticated checkout initiation via feed token (ADR-013).
		// The feed token + session_id pair is validated before creating the
		// reservation + checkout session.
		//
		//   POST /v1/public/feeds/{feed_token}/checkout/start
		//        body: { tier_id, session_id, qty, holder_email, promo_code? }
		//        returns: { checkout_session, redirect_url }
		//
		// 403 when session_id does not belong to the feed identified by feed_token.
		if s.publicFeedQueries != nil && s.checkoutQueries != nil && s.reservationQueries != nil {
			r.Post("/public/feeds/{feed_token}/checkout/start", s.handlePublicFeedCheckoutStart)
		}

		// ── Service billing ledger (feature #161) ───────────────────────────────
		//
		// Billing is about platform service fees charged to organizers/agents.
		// Separate from customer ticket payments (those are in the payment layer).
		//
		//   POST /v1/billing/tariffs                            — create tariff version (billing.admin)
		//   GET  /v1/billing/tariffs/active                    — get active tariff     (billing.read)
		//   GET  /v1/organizations/{org_id}/billing/usage      — current usage         (billing.read)
		//   POST /v1/billing/invoices/generate                 — month-end batch       (billing.admin)
		//   GET  /v1/organizations/{org_id}/billing/invoices   — list invoices         (billing.read)
		//   GET  /v1/billing/invoices/{id}                     — get invoice + lines   (billing.read)
		//   POST /v1/billing/invoices/{id}/issue               — draft → issued        (billing.admin)
		//   POST /v1/billing/invoices/{id}/pay                 — issued → paid         (billing.admin)
		//   POST /v1/billing/invoices/{id}/void                — void                  (billing.admin)
		if s.stub != nil && s.stub.Enabled() && s.billingQueries != nil {
			// billing.read endpoints
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "billing.read", "billing"))
				pr.Get("/billing/tariffs/active", s.handleGetActiveTariff)
				pr.Get("/billing/invoices/{id}", s.handleGetInvoice)
				pr.Get("/organizations/{org_id}/billing/usage", s.handleGetUsage)
				pr.Get("/organizations/{org_id}/billing/invoices", s.handleListOrgInvoices)
			})
			// billing.admin endpoints
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "billing.admin", "billing"))
				pr.Post("/billing/tariffs", s.handleCreateTariff)
				pr.Post("/billing/invoices/generate", s.handleGenerateInvoices)
				pr.Post("/billing/invoices/{id}/issue", s.handleIssueInvoice)
				pr.Post("/billing/invoices/{id}/pay", s.handlePayInvoice)
				pr.Post("/billing/invoices/{id}/void", s.handleVoidInvoice)
			})
		}

		// ── Stripe Billing adapter (feature #162) ───────────────────────────────
		//
		// Pushes platform SaaS invoices to the platform's Estonia Stripe account
		// and syncs payment status back via webhooks.
		//
		//   POST /v1/billing/stripe/push-invoice/{id} — push invoice to Stripe (billing.admin)
		//   POST /v1/billing/stripe/webhook           — Stripe Billing webhook   (public)
		if s.stub != nil && s.stub.Enabled() && s.stripeBilling != nil && s.billingQueries != nil {
			// push-invoice requires billing.admin JWT
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "billing.admin", "billing"))
				pr.Post("/billing/stripe/push-invoice/{id}", s.handlePushInvoiceToStripe)
			})
			// webhook is public (signature verification inside the handler)
			r.Post("/billing/stripe/webhook", s.handleStripeBillingWebhook)
		}

		// ── Platform superadmin console (feature #166) ──────────────────────────
		//
		// Read-only cross-tenant endpoints for platform operators. Every request
		// requires JWT auth, the platform_superadmin (or admin) role via the
		// superadmin.read permission, and a mandatory X-Admin-Reason header.
		// Every successful read is audit-logged.
		//
		//   GET /v1/admin/organizations  — list all organizations (all tenants)
		//   GET /v1/admin/orders         — list all checkout sessions (all tenants)
		//   GET /v1/admin/tickets        — list all tickets (all tenants)
		//   GET /v1/admin/refunds        — list all refunds (all tenants)
		if s.stub != nil && s.stub.Enabled() && s.superadminQueries != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "superadmin.read", "superadmin"))
				pr.Get("/admin/organizations", s.handleSuperadminListOrganizations)
				pr.Get("/admin/orders", s.handleSuperadminListOrders)
				pr.Get("/admin/tickets", s.handleSuperadminListTickets)
				pr.Get("/admin/refunds", s.handleSuperadminListRefunds)
			})
		}

		// ── Admin impersonation (feature #167) ───────────────────────────────────
		//
		// Platform superadmins can issue a scoped JWT that temporarily acts as a
		// target user, enabling diagnosis of user-specific issues without sharing
		// credentials. Every issuance is audit-logged. Token lifetime ≤ 30 minutes.
		//
		//   POST /v1/admin/impersonate  — issue scoped impersonation JWT (superadmin.read)
		if s.stub != nil && s.stub.Enabled() {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "superadmin.read", "superadmin"))
				pr.Post("/admin/impersonate", s.handleImpersonate)
			})
		}

		// ── External allocation quota model (feature #145) ──────────────────────
		//
		// Partner organisations (resellers/box offices) reserve quota blocks.
		// Allocating reduces platform inventory; reconciliation settles consumption.
		//
		//   POST  /v1/organizations/{org_id}/external-allocations        — create (allocation.create)
		//   GET   /v1/organizations/{org_id}/external-allocations        — list   (allocation.read)
		//   GET   /v1/organizations/{org_id}/external-allocations/{id}   — get    (allocation.read)
		//   PATCH /v1/organizations/{org_id}/external-allocations/{id}   — update (allocation.update)
		if s.stub != nil && s.stub.Enabled() && s.allocationQueries != nil {
			// allocation.read endpoints
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "allocation.read", "allocations"))
				pr.Get("/organizations/{org_id}/external-allocations", s.handleListExternalAllocations)
				pr.Get("/organizations/{org_id}/external-allocations/{id}", s.handleGetExternalAllocation)
			})
			// allocation.create endpoint
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "allocation.create", "allocations"))
				pr.Post("/organizations/{org_id}/external-allocations", s.handleCreateExternalAllocation)
			})
			// allocation.update endpoint
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "allocation.update", "allocations"))
				pr.Patch("/organizations/{org_id}/external-allocations/{id}", s.handlePatchExternalAllocation)
			})
		}

		// ── Complimentary ticket issuance flow (feature #148) ────────────────────
		//
		// Org admins issue complimentary tickets to named recipients without a
		// checkout session or payment. batch_id provides idempotency.
		// Inventory is decremented via ReserveCapacity + ConfirmCapacity.
		//
		//   POST /v1/organizations/{org_id}/complimentary        — issue batch (complimentary.issue)
		//   GET  /v1/organizations/{org_id}/complimentary        — list issuances (complimentary.read)
		//   GET  /v1/organizations/{org_id}/complimentary/{id}   — get detail (complimentary.read)
		if s.stub != nil && s.stub.Enabled() && s.complimentaryQueries != nil {
			// complimentary.read endpoints
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "complimentary.read", "complimentary"))
				pr.Get("/organizations/{org_id}/complimentary", s.handleListComplimentaryIssuances)
				pr.Get("/organizations/{org_id}/complimentary/{id}", s.handleGetComplimentaryIssuance)
			})
			// complimentary.issue endpoint
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "complimentary.issue", "complimentary"))
				pr.Post("/organizations/{org_id}/complimentary", s.handleCreateComplimentaryIssuance)
			})
			// complimentary.revoke endpoint (feature #150)
			// POST /v1/complimentary/{id}/revoke — revokes an issuance by ID.
			// Uses complimentary.issue permission (same actor who can issue can revoke).
			// Scan-status check: if any ticket is scanned → 409 manual_review.
			// On clean revoke: tickets + barcodes + credentials revoked, inventory restored.
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "complimentary.issue", "complimentary"))
				pr.Post("/complimentary/{id}/revoke", s.handleRevokeComplimentaryIssuance)
			})
		}

		// ── External barcode batch import (feature #146) ─────────────────────────
		//
		// Operators upload CSV files of external barcodes. Batches require approval
		// before barcodes are activated for scanning.
		//
		//   POST  /v1/barcode-batches              — upload CSV (barcode_batch.upload)
		//   GET   /v1/barcode-batches              — list batches (barcode_batch.read)
		//   GET   /v1/barcode-batches/{id}         — get detail (barcode_batch.read)
		//   POST  /v1/barcode-batches/{id}/approve — approve (barcode_batch.approve)
		//   POST  /v1/barcode-batches/{id}/reject  — reject  (barcode_batch.approve)
		if s.stub != nil && s.stub.Enabled() && s.barcodeBatchQueries != nil {
			// barcode_batch.read endpoints
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode_batch.read", "barcode_batches"))
				pr.Get("/barcode-batches", s.handleListBarcodeBatches)
				pr.Get("/barcode-batches/{id}", s.handleGetBarcodeBatch)
			})
			// barcode_batch.upload endpoint (multipart/form-data)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode_batch.upload", "barcode_batches"))
				pr.Post("/barcode-batches", s.handleUploadBarcodeBatch)
			})
			// barcode_batch.approve endpoints (platform_operator)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "barcode_batch.approve", "barcode_batches"))
				pr.Post("/barcode-batches/{id}/approve", s.handleApproveBarcodeBatch)
				pr.Post("/barcode-batches/{id}/reject", s.handleRejectBarcodeBatch)
			})
		}

		// ── WordPress Webhook Subscriber Registry (feature #156) ─────────────
		//
		// Allows WordPress sites (or any other HTTP endpoint) to register as
		// webhook subscribers. The registration response includes a generated
		// HMAC-SHA256 signing secret that the WP plugin stores in settings.
		//
		//   POST   /v1/webhooks/subscribers        — register (webhook.subscriber.manage)
		//   GET    /v1/webhooks/subscribers        — list     (webhook.subscriber.manage)
		//   GET    /v1/webhooks/subscribers/{id}   — get      (webhook.subscriber.manage)
		//   DELETE /v1/webhooks/subscribers/{id}   — deactivate (webhook.subscriber.manage)
		if s.stub != nil && s.stub.Enabled() && s.webhookSubQueries != nil {
			// Read routes (GET).
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "webhook.subscriber.manage", "webhooks"))
				pr.Get("/webhooks/subscribers", s.handleListWebhookSubscribers)
				pr.Get("/webhooks/subscribers/{id}", s.handleGetWebhookSubscriber)
			})
			// Write routes (POST, DELETE) — require pool for writes.
			if s.pool != nil {
				r.Group(func(pr chi.Router) {
					pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
					pr.Use(permissions.RequirePermission(s.perms, "webhook.subscriber.manage", "webhooks"))
					pr.Post("/webhooks/subscribers", s.handleRegisterWebhookSubscriber)
					pr.Delete("/webhooks/subscribers/{id}", s.handleDeactivateWebhookSubscriber)
				})
			}
		}

		// ── External Reconciliation (feature #147) ────────────────────────────
		//
		// Partner organisations submit sales/returns reports against their active
		// external allocation quota. The platform auto-matches lines to barcodes
		// and queues unmatched lines for operator review.
		//
		//   POST  /v1/reconciliation/reports                      — submit report (reconciliation.submit)
		//   GET   /v1/reconciliation/reports/{id}                 — get report + lines (reconciliation.read)
		//   GET   /v1/reconciliation/exceptions                   — operator exception queue (reconciliation.review)
		//   PATCH /v1/reconciliation/reports/{id}/review          — mark report reviewed (reconciliation.review)
		//   PATCH /v1/reconciliation/reports/{id}/lines/{line_id} — resolve exception line (reconciliation.review)
		if s.stub != nil && s.stub.Enabled() && s.reconciliationQueries != nil {
			// reconciliation.read endpoints
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "reconciliation.read", "reconciliation"))
				pr.Get("/reconciliation/reports/{id}", s.handleGetReconciliationReport)
			})
			// reconciliation.review endpoints (operator queue)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "reconciliation.review", "reconciliation"))
				pr.Get("/reconciliation/exceptions", s.handleListReconciliationExceptions)
				pr.Patch("/reconciliation/reports/{id}/review", s.handleReviewReconciliationReport)
				pr.Patch("/reconciliation/reports/{id}/lines/{line_id}", s.handleResolveReconciliationException)
			})
			// reconciliation.submit endpoints — require pool for writes
			if s.pool != nil {
				r.Group(func(pr chi.Router) {
					pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
					pr.Use(permissions.RequirePermission(s.perms, "reconciliation.submit", "reconciliation"))
					pr.Post("/reconciliation/reports", s.handleSubmitReconciliationReport)
				})
			}
		}
	})
}

// mountCompatRoutes is defined in bil24_compat.go (feature #157).

// handleNotFound is the chi NotFound handler. It replaces chi's built-in
// plain-text "404 page not found\n" response with the project-standard JSON
// error envelope (feature #12). The handler is invoked after the full
// middleware chain, so X-Request-Id, X-Trace-Id, and the locale-aware
// Localizer (when LocaleMiddleware is wired) are already present in ctx.
func handleNotFound(w http.ResponseWriter, r *http.Request) {
	msg := i18n.Localize(r.Context(), "error.not_found",
		"the requested resource does not exist", nil)
	writeJSON(w, http.StatusNotFound, errorEnvelope("http.not_found", msg, r))
}

// handleMethodNotAllowed is the chi MethodNotAllowed handler. It replaces
// chi's default plain-text 405 response with the project-standard JSON error
// envelope (feature #13).
//
// chi v5 does NOT set the Allow header when a custom MethodNotAllowed handler
// is registered (the default handler sets Allow, but a custom handler bypasses
// that code path). We therefore build the Allow header ourselves by probing
// the chi Routes interface stored in the current RouteContext: for each
// candidate HTTP method, we ask Routes.Match whether it would route that
// method on the same path. Matched methods are joined into the Allow value.
//
// Standard candidates for Allow probing (per RFC 9110 §9):
//
//	GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS
//
// HEAD is always included alongside GET when GET is matched because go's
// net/http automatically handles HEAD on any GET route (RFC requirement).
func handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	// Probe the chi router for methods that ARE allowed on this path.
	// chi.RouteContext(r.Context()).Routes is the live chi.Routes that matched
	// this request; calling Match on a fresh Context is non-destructive.
	rctx := chi.RouteContext(r.Context())
	if rctx != nil && rctx.Routes != nil {
		candidates := []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		}
		var allowed []string
		for _, m := range candidates {
			if m == r.Method {
				// skip the method that was just rejected
				continue
			}
			testCtx := chi.NewRouteContext()
			if rctx.Routes.Match(testCtx, m, r.URL.Path) {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
		}
	}
	msg := i18n.Localize(r.Context(), "http.method_not_allowed",
		"method not allowed", nil)
	writeJSON(w, http.StatusMethodNotAllowed, errorEnvelope("http.method_not_allowed", msg, r))
}

// handleHealthz is a liveness probe: returns 200 unconditionally while the
// process is alive and able to serve HTTP.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleReadyz is a readiness probe: iterates through all registered
// ReadinessProbes and aggregates their results into the /readyz checks map.
// Returns 200 {"status":"ready","checks":{...}} when every probe passes, or
// 503 {"status":"not_ready","checks":{...}} when any probe fails.
// When no probes are registered the server is always considered ready (useful
// during integration tests that wire no dependencies).
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	checks := make(map[string]string, len(s.probes))
	failed := false
	for _, p := range s.probes {
		if err := p.Ping(ctx); err != nil {
			checks[p.ProbeName()] = err.Error()
			failed = true
		} else {
			checks[p.ProbeName()] = "ok"
		}
	}
	if failed {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready",
			"checks": checks,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"checks": checks,
	})
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

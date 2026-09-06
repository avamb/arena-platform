package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/redissession"
)

// Server is the long-lived HTTP listener that hosts the arena-api.
//
// All wired-in dependencies are nilable at construction time so tests can
// build a Server with only the pieces they need (e.g. a fake DB or a
// disabled auth stub). The route mounts guard against missing dependencies
// rather than panicking at startup. See wire.go for the Options/New
// constructor and mount_*.go for the per-domain route registration.
type Server struct {
	// Core lifecycle / cross-cutting wiring.
	cfg          *config.Config
	logger       *slog.Logger
	router       chi.Router
	srv          *http.Server
	probes       []ReadinessProbe
	pool         PoolDB
	stub         *auth.StubProvider
	verifier     auth.Provider
	audit        audit.Writer
	idem         idempotency.Store
	metrics      http.Handler
	typedMetrics *observability.Metrics
	outboxWriter outbox.Writer
	perms        permissions.Checker
	clk          clock.Clock

	// apiKeyStore backs organization API-key authentication (spec §13.1,
	// feature #513). Nil in unit tests that do not exercise service actors;
	// authenticateAPIKey then answers 401 for every `ak_…` bearer.
	apiKeyStore apikeys.Store
	// apiKeyRL enforces APIKeyRateLimit requests per APIKeyRateWindow keyed
	// by api_key.id. Nil disables the limit (tests only).
	apiKeyRL ratelimit.Limiter

	// Per-domain sqlc Queries handles. All are nilable; the corresponding
	// route mounts guard against missing handles. See mount_*.go.
	siQueries      *gen.Queries
	geoQueries     *gen.Queries
	orgQueries     *gen.Queries
	channelQueries *gen.Queries
	// customerQueries backs the customer read surface (feature #482, spec
	// §12.3). It serves customers.sql / orders.sql / reservations.sql methods
	// alike since they are all defined on the same *gen.Queries type.
	customerQueries       *gen.Queries
	paymentConfigQueries  *gen.Queries
	bankAccountQueries    *gen.Queries
	membershipQueries     *gen.Queries
	venueQueries          *gen.Queries
	feedTokenQueries      *gen.Queries
	eventQueries          *gen.Queries
	publicationQueries    *gen.Queries
	publicFeedQueries     *gen.Queries
	publicFeedRL          *publicFeedRateLimiter
	sessionQueries        *gen.Queries
	gdprQueries           *gen.Queries
	tierQueries           *gen.Queries
	inventoryQueries      *gen.Queries
	reservationQueries    *gen.Queries
	promoQueries          *gen.Queries
	pricingRules          PricingRules
	checkoutQueries       *gen.Queries
	paymentIntentQueries  *gen.Queries
	ticketQueries         *gen.Queries
	credentialQueries     *gen.Queries
	funnelQueries         *gen.Queries
	refundQueries         *gen.Queries
	barcodeQueries        *gen.Queries
	reportQueries         *gen.Queries
	billingQueries        *gen.Queries
	deliveryJobQueries    *gen.Queries
	workerPool            *pgxpool.Pool
	emailSender           email.Sender
	stripeConnect         stripeConnectHelper
	stripeBilling         stripeBillingHelper
	sessionStore          redissession.Store
	maxConcurrentSessions int
	superadminQueries     *gen.Queries
	allocationQueries     *gen.Queries
	complimentaryQueries  *gen.Queries
	barcodeBatchQueries   *gen.Queries
	webhookSubQueries     *gen.Queries
	reconciliationQueries *gen.Queries
	networkQueries        *gen.Queries
	// seatingQueries backs the seating-plan CRUD + versions + fork surface
	// (feature #304, Wave SEAT-A3). Nil when neither PgxPool nor an
	// explicit override is supplied; the mount self-gates on nil.
	seatingQueries *gen.Queries
	meQueries      meQuerier
	media          *mediastore.Repo
	// pgxPool is the raw *pgxpool.Pool used by features that need direct
	// pool access beyond the PoolDB interface (e.g. macs export, AB-50b).
	// Wired from Options.PgxPool in wire.go.
	pgxPool *pgxpool.Pool

	// bundle is the platform i18n bundle used by the Bil24 compat gateway
	// (feature #478, W1-A3b) to localize bil24.* description keys per
	// request. Wired from Options.Bundle in wire.go; may be nil in unit
	// tests, in which case the gateway falls back to English descriptions
	// (bil24compat.LocalizeDescription nil-safe).
	bundle *i18n.Bundle

	// Dev / test toggles.
	faultInjectOutboxAfterAudit bool
	slowDelay                   time.Duration
	debugRoutesEnabled          bool
	debugSlowDelay              time.Duration
	bil24Enabled                bool
	// bil24RequireToken mirrors BIL24_REQUIRE_TOKEN (feature #381, PR2-25).
	// When true, bil24Handler() calls WithRequireToken(true) so every
	// state-mutating Bil24 command (RESERVATION, UN_RESERVE) validates the
	// fid/token credential pair against the channel's gateway_token_hash.
	// Defaults to false at the struct level but wired to true in production
	// via Options.Bil24RequireToken (config default: true).
	bil24RequireToken bool
}

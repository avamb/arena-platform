package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
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
	audit        audit.Writer
	idem         idempotency.Store
	metrics      http.Handler
	typedMetrics *observability.Metrics
	outboxWriter outbox.Writer
	perms        permissions.Checker
	clk          clock.Clock

	// Per-domain sqlc Queries handles. All are nilable; the corresponding
	// route mounts guard against missing handles. See mount_*.go.
	siQueries             *gen.Queries
	geoQueries            *gen.Queries
	orgQueries            *gen.Queries
	channelQueries        *gen.Queries
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

	// Dev / test toggles.
	faultInjectOutboxAfterAudit bool
	slowDelay                   time.Duration
	debugRoutesEnabled          bool
	debugSlowDelay              time.Duration
	bil24Enabled                bool
}

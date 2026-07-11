// Package hfeed implements HTTP handlers for the federated feed domain:
// agent feed token management (feature #122), the unauthenticated public
// event feed (feature #152), and the public feed checkout initiation
// endpoint (feature #153).
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin feed_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling / hgeo / hgdpr.
//
// Cross-domain note: the public checkout start flow reuses the checkout
// domain's reservation TTL, pricing pipeline and response mapper via direct
// hcheckout imports (sub-package → sub-package). Promo-code validation is
// injected as a callback because the canonical validator still lives in the
// parent package's pricing domain (pricing_calculator.go).
package hfeed

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// pgUniqueViolation is the PostgreSQL error code for unique-constraint violations.
const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hfeed requires. PoolDB
// satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// RateLimiter is the narrow rate-limiting interface the public feed handlers
// require. The concrete in-memory limiter (publicFeedRateLimiter) stays in
// package httpserver's feed_shims.go because public_feed_152_test.go drives
// its unexported checkToken / checkIP methods directly; it satisfies this
// interface via exported CheckToken / CheckIP adapter methods.
type RateLimiter interface {
	CheckToken(token string) bool
	CheckIP(ip string) bool
}

// PromoValidator checks whether a promo code is applicable for a given order
// amount at a given time. It returns the discount in smallest currency units
// and an empty error code on success, or (0, "<error.code>") on failure.
// The canonical implementation is hcheckout.ValidatePromoCode, injected by
// feed_shims.go.
type PromoValidator func(pc gen.PromoCodeRow, orderAmount int64, now time.Time) (int64, string)

// Handler holds the shared dependencies for all feed-domain HTTP handlers.
type Handler struct {
	feedTokenQueries   *gen.Queries
	publicFeedQueries  *gen.Queries
	sessionQueries     *gen.Queries
	tierQueries        *gen.Queries
	checkoutQueries    *gen.Queries
	reservationQueries *gen.Queries
	inventoryQueries   *gen.Queries
	promoQueries       *gen.Queries
	ticketQueries      *gen.Queries // for WID-0b order-status paid tickets
	credentialQueries  *gen.Queries // for WID-0b human_code + PDF lookup
	funnelQueries      *gen.Queries // for WID-0e funnel telemetry sink
	pool               TxStarter
	logger             *slog.Logger
	audit              audit.Writer
	rl                 RateLimiter
	pricingRules       hcheckout.PricingRules
	validatePromo      PromoValidator
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	feedTokenQ *gen.Queries,
	publicFeedQ *gen.Queries,
	sessionQ *gen.Queries,
	tierQ *gen.Queries,
	checkoutQ *gen.Queries,
	reservationQ *gen.Queries,
	inventoryQ *gen.Queries,
	promoQ *gen.Queries,
	ticketQ *gen.Queries,
	credentialQ *gen.Queries,
	funnelQ *gen.Queries,
	pool TxStarter,
	logger *slog.Logger,
	auditW audit.Writer,
	rl RateLimiter,
	pricingRules hcheckout.PricingRules,
	validatePromo PromoValidator,
) *Handler {
	return &Handler{
		feedTokenQueries:   feedTokenQ,
		publicFeedQueries:  publicFeedQ,
		sessionQueries:     sessionQ,
		tierQueries:        tierQ,
		checkoutQueries:    checkoutQ,
		reservationQueries: reservationQ,
		inventoryQueries:   inventoryQ,
		promoQueries:       promoQ,
		ticketQueries:      ticketQ,
		credentialQueries:  credentialQ,
		funnelQueries:      funnelQ,
		pool:               pool,
		logger:             logger,
		audit:              auditW,
		rl:                 rl,
		pricingRules:       pricingRules,
		validatePromo:      validatePromo,
	}
}

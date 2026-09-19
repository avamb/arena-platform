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
// done directly via hcheckout.ValidatePromoForLines (AB-45c removed the
// legacy callback).
package hfeed

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// pgUniqueViolation is the PostgreSQL error code for unique-constraint violations.
const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hfeed requires. PoolDB
// satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// RateLimiter is the narrow rate-limiting interface the public feed/checkout
// handlers require. The concrete in-memory limiter (publicFeedRateLimiter)
// stays in package httpserver's feed_shims.go because public_feed_152_test.go
// drives its unexported checkFeedToken / checkCheckoutToken / checkIP methods
// directly; it satisfies this interface via the exported CheckFeedToken /
// CheckCheckoutToken / CheckIP adapter methods.
//
// Each Check* method reports (allowed, retryAfterSeconds): retryAfterSeconds
// is the whole seconds remaining until that bucket's window resets, valid
// only when allowed is false, and is surfaced on a 429 as Retry-After.
type RateLimiter interface {
	// CheckFeedToken checks the feed-token bucket. A feed token belongs to a
	// sales channel and is shared by EVERY buyer of that site's widget — this
	// must stay a site-wide limit (PUBLIC_FEED_TOKEN_RATE_LIMIT), never a
	// per-visitor one.
	CheckFeedToken(token string) (allowed bool, retryAfterSeconds int)
	// CheckCheckoutToken checks the checkout-token bucket: one buyer's
	// checkout journey (status polling, recover, ticket PDF).
	CheckCheckoutToken(token string) (allowed bool, retryAfterSeconds int)
	// CheckIP checks the per-client-IP bucket. Callers must key it with
	// Handler.clientIP (httputil.TrustedClientIP), never raw
	// httputil.ExtractClientIP / X-Forwarded-For, which is client-controlled
	// and trivially spoofed.
	CheckIP(ip string) (allowed bool, retryAfterSeconds int)
}

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
	// mediaSigner (feature #535, spec 22 §2.1) turns a media_objects id into
	// an absolute signed URL an external consumer can fetch. Nil keeps the
	// pre-#535 host-relative /v1/media-files/{uuid} projection.
	mediaSigner MediaURLSigner
	// trustedProxies is the reverse-proxy hop count (config.TrustedProxyCount)
	// used to derive the spoof-resistant client IP for rate limiting via
	// httputil.TrustedClientIP. 0 (the default) ignores X-Forwarded-For
	// entirely and uses the TCP peer address — correct only when this
	// process is reachable directly, not behind Traefik/nginx/etc.
	trustedProxies int
	// paymentStarter creates the provider-hosted payment page after the
	// checkout transaction has committed. Nil means this deployment cannot
	// take money for a paid cart: checkout/start then answers
	// checkout.payment_not_configured rather than a dead redirect.
	paymentStarter PaymentStarter
	// returnURLPolicy validates the buyer-supplied return_url and supplies
	// the PUBLIC_TICKETS_BASE_URL fallback.
	returnURLPolicy ReturnURLPolicy
	// paymentWindow / paymentGrace size the hosted session's own expiry and
	// the hold/order/checkout expiry respectively: the hosted session always
	// dies FIRST (window), the seats are released only after the grace.
	paymentWindow time.Duration
	paymentGrace  time.Duration
	// eventQueries resolves the event title used as the hosted page's line
	// item label. Optional: a lookup failure degrades to a generic label
	// rather than failing the sale.
	eventQueries *gen.Queries
}

// WithPayments wires the hosted-payment dependencies. Returns the receiver
// for chaining, matching WithMediaSigner.
func (h *Handler) WithPayments(
	starter PaymentStarter,
	policy ReturnURLPolicy,
	window, grace time.Duration,
	eventQ *gen.Queries,
) *Handler {
	h.paymentStarter = starter
	h.returnURLPolicy = policy
	h.paymentWindow = window
	h.paymentGrace = grace
	h.eventQueries = eventQ
	return h
}

// MediaURLSigner builds a publicly fetchable URL for a media object id,
// returning "" when it cannot sign (storage not configured, object missing) so
// the caller falls back to the host-relative projection instead of emitting a
// broken link. Feature #535, spec 22 §2.1.
type MediaURLSigner func(ctx context.Context, mediaID uuid.UUID) string

// WithMediaSigner wires the absolute signed-media URL builder. Returns the
// receiver for chaining.
func (h *Handler) WithMediaSigner(s MediaURLSigner) *Handler {
	h.mediaSigner = s
	return h
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
	trustedProxies int,
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
		trustedProxies:     trustedProxies,
	}
}

// clientIP derives the request's client IP for rate-limiting purposes using
// the spoof-resistant TrustedClientIP helper — never httputil.ExtractClientIP,
// which trusts the FIRST X-Forwarded-For entry unconditionally and is
// therefore bypassable by any caller that sets that header. With
// trustedProxies == 0 (the default) XFF is ignored entirely and the raw TCP
// peer address is used.
func (h *Handler) clientIP(r *http.Request) string {
	return httputil.TrustedClientIP(r, h.trustedProxies)
}

// enforceRateLimit evaluates the token-scoped bucket (tokenCheck — pass
// h.rl.CheckFeedToken or h.rl.CheckCheckoutToken) AND the per-IP bucket on
// EVERY call, never short-circuited: a naive `!okToken || !okIP` stops
// evaluating at the first false, so a burst that trips the token limit would
// never increment the IP counter (and vice versa), letting that half of the
// defense go uncounted under exactly the load it exists to catch. On block
// it sets Retry-After to the larger of the two blocking windows and writes
// the 429 envelope with errorCode (callers keep their existing
// feed.rate_limited / checkout.rate_limited codes), returning false; the
// caller must return immediately. Returns true when the request may proceed.
func (h *Handler) enforceRateLimit(
	w http.ResponseWriter, r *http.Request, errorCode string,
	tokenCheck func(string) (bool, int), token string,
) bool {
	allowedTok, retryTok := tokenCheck(token)
	allowedIP, retryIP := h.rl.CheckIP(h.clientIP(r))
	if allowedTok && allowedIP {
		return true
	}
	retryAfter := 1
	if !allowedTok && retryTok > retryAfter {
		retryAfter = retryTok
	}
	if !allowedIP && retryIP > retryAfter {
		retryAfter = retryIP
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
		errorCode, "too many requests; please slow down", r,
	))
	return false
}

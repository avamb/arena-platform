// feed_shims.go bridges the *Server god-object to the hfeed sub-package. All
// handler bodies live in hfeed/; these thin delegating methods preserve the
// unexported *Server method surface so mount_catalog.go and the structural
// test files (feed_tokens_test.go, public_feed_152_test.go,
// public_feed_checkout_153_test.go) compile unchanged.
//
// The public feed rate limiter (publicFeedRateLimiter /
// newPublicFeedRateLimiter) is kept live in this file — public_feed_152_test.go
// drives its unexported checkFeedToken / checkCheckoutToken / checkIP methods
// directly and server_struct.go holds the concrete *publicFeedRateLimiter
// field. The type satisfies the narrower hfeed.RateLimiter interface via the
// CheckFeedToken / CheckCheckoutToken / CheckIP wrapper methods below. The
// rateLimiterWindow helper struct also stays here because scanner_shims.go
// shares it.
package httpserver

import (
	"net/http"
	"sync"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
)

// ─── in-memory rate limiter (kept in package httpserver for tests) ────────────

// rateLimiterWindow tracks request count within a rolling 1-minute window.
type rateLimiterWindow struct {
	count   int
	resetAt time.Time
}

// publicFeedRateLimiter is an in-memory rate limiter enforcing THREE
// independent per-minute caps for the public widget API (see the AGENTS.md
// gotcha on public API rate limits):
//
//   - feedTokenLimit — requests/minute for a single feed token. A feed token
//     belongs to a sales channel and is shared by EVERY buyer of that site's
//     widget, so this is a site-wide ceiling, not a per-visitor one
//     (PUBLIC_FEED_TOKEN_RATE_LIMIT defaults to 20000 for exactly that
//     reason — a per-visitor-sized limit collapses under real concurrent
//     traffic).
//   - checkoutTokenLimit — requests/minute for a single checkout token (one
//     buyer's status polling / recover / ticket-pdf calls).
//   - ipLimit — requests/minute per client IP. Callers MUST key this with
//     httputil.TrustedClientIP, never raw ExtractClientIP/X-Forwarded-For,
//     which is client-controlled and trivially spoofed.
//
// A limit <= 0 disables that particular check (always allowed). All three
// maps are opportunistically pruned of expired windows inside check(), at
// most once per minute, so a spoofed/rotating key set cannot grow them
// without bound.
type publicFeedRateLimiter struct {
	mu                 sync.Mutex
	feedTokenLimit     int
	checkoutTokenLimit int
	ipLimit            int
	feedTokens         map[string]*rateLimiterWindow
	checkoutTokens     map[string]*rateLimiterWindow
	ips                map[string]*rateLimiterWindow
	lastSweep          time.Time
}

// newPublicFeedRateLimiter creates a rate limiter with the given per-minute
// limits. A limit <= 0 disables that particular check.
func newPublicFeedRateLimiter(feedTokenLimit, checkoutTokenLimit, ipLimit int) *publicFeedRateLimiter {
	return &publicFeedRateLimiter{
		feedTokenLimit:     feedTokenLimit,
		checkoutTokenLimit: checkoutTokenLimit,
		ipLimit:            ipLimit,
		feedTokens:         make(map[string]*rateLimiterWindow),
		checkoutTokens:     make(map[string]*rateLimiterWindow),
		ips:                make(map[string]*rateLimiterWindow),
	}
}

// sweepExpiredLocked drops every window that has already reset from all
// three maps. Called from check() under rl.mu, gated to at most once per
// minute via lastSweep — an unconditional per-call sweep would just move
// the O(n) cost from unbounded memory growth to CPU burned on every single
// request. Must be called with the lock already held.
func (rl *publicFeedRateLimiter) sweepExpiredLocked(now time.Time) {
	if !rl.lastSweep.IsZero() && now.Sub(rl.lastSweep) < time.Minute {
		return
	}
	rl.lastSweep = now
	sweepExpiredWindows(rl.feedTokens, now)
	sweepExpiredWindows(rl.checkoutTokens, now)
	sweepExpiredWindows(rl.ips, now)
}

// sweepExpiredWindows deletes every map entry whose window has already
// reset as of now.
func sweepExpiredWindows(m map[string]*rateLimiterWindow, now time.Time) {
	for k, w := range m {
		if now.After(w.resetAt) {
			delete(m, k)
		}
	}
}

// check increments the counter for key in the given window map. It reports
// whether the request is allowed and, when blocked, the whole seconds
// remaining until the window resets (at least 1) so callers can set
// Retry-After. limit <= 0 means the check is disabled: always allowed with
// a 0 retry-after.
func (rl *publicFeedRateLimiter) check(m map[string]*rateLimiterWindow, key string, limit int) (allowed bool, retryAfterSeconds int) {
	if limit <= 0 {
		return true, 0
	}
	now := time.Now()
	rl.sweepExpiredLocked(now)
	w, ok := m[key]
	if !ok || now.After(w.resetAt) {
		m[key] = &rateLimiterWindow{count: 1, resetAt: now.Add(time.Minute)}
		return true, 0
	}
	w.count++
	if w.count <= limit {
		return true, 0
	}
	// Ceil the remaining window to whole seconds via integer duration math —
	// NOT time.Duration.Seconds() truncated then +1: on a host with coarse
	// clock resolution (observed on Windows) two back-to-back time.Now()
	// calls can return the identical instant, making the remaining window
	// exactly 60s; a naive int(60.0)+1 then reports 61s, one second past the
	// window's own maximum.
	remaining := w.resetAt.Sub(now)
	if remaining <= 0 {
		return false, 1
	}
	retryAfterSeconds = int(remaining / time.Second)
	if remaining%time.Second != 0 {
		retryAfterSeconds++
	}
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	return false, retryAfterSeconds
}

// checkFeedToken increments the per-feed-token counter.
func (rl *publicFeedRateLimiter) checkFeedToken(token string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.feedTokens, token, rl.feedTokenLimit)
}

// checkCheckoutToken increments the per-checkout-token counter.
func (rl *publicFeedRateLimiter) checkCheckoutToken(token string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.checkoutTokens, token, rl.checkoutTokenLimit)
}

// checkIP increments the per-IP counter.
func (rl *publicFeedRateLimiter) checkIP(ip string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.ips, ip, rl.ipLimit)
}

// CheckFeedToken / CheckCheckoutToken / CheckIP are exported adapter methods
// so *publicFeedRateLimiter satisfies the hfeed.RateLimiter interface. They
// forward to the private implementations above unchanged.
func (rl *publicFeedRateLimiter) CheckFeedToken(token string) (bool, int) {
	return rl.checkFeedToken(token)
}

// CheckCheckoutToken forwards to the unexported checkCheckoutToken helper.
func (rl *publicFeedRateLimiter) CheckCheckoutToken(token string) (bool, int) {
	return rl.checkCheckoutToken(token)
}

// CheckIP forwards to the unexported checkIP helper.
func (rl *publicFeedRateLimiter) CheckIP(ip string) (bool, int) { return rl.checkIP(ip) }

// ─── rate limit config resolution ──────────────────────────────────────────────
// publicFeedTokenRateLimit / publicCheckoutTokenRateLimit / publicAPIIPRateLimit
// read the three PUBLIC_*_RATE_LIMIT settings off cfg, falling back to the
// documented config.go defaults when cfg is nil (test constructions that build
// *config.Config literals by hand, or a Server assembled without one). Wire.go
// calls these once, at Server construction, to size the single package-level
// publicFeedRL limiter — see config.go for the site-wide-vs-per-visitor
// reasoning behind the feed-token default.

const (
	defaultPublicFeedTokenRateLimit     = 20000
	defaultPublicCheckoutTokenRateLimit = 120
	defaultPublicAPIIPRateLimit         = 600
)

func publicFeedTokenRateLimit(cfg *config.Config) int {
	if cfg == nil {
		return defaultPublicFeedTokenRateLimit
	}
	return cfg.PublicFeedTokenRateLimit
}

func publicCheckoutTokenRateLimit(cfg *config.Config) int {
	if cfg == nil {
		return defaultPublicCheckoutTokenRateLimit
	}
	return cfg.PublicCheckoutTokenRateLimit
}

func publicAPIIPRateLimit(cfg *config.Config) int {
	if cfg == nil {
		return defaultPublicAPIIPRateLimit
	}
	return cfg.PublicAPIIPRateLimit
}

// ─── handler construction ─────────────────────────────────────────────────────

// feedHandler constructs an hfeed.Handler from the server's dependencies. A
// fresh handler per request keeps the wiring uniform with hbilling / hgeo /
// hgdpr and avoids stale captures when test code mutates *Server fields
// between calls.
func (s *Server) feedHandler() *hfeed.Handler {
	trustedProxies := 0
	if s.cfg != nil {
		trustedProxies = s.cfg.TrustedProxyCount
	}
	return hfeed.New(
		s.feedTokenQueries,
		s.publicFeedQueries,
		s.sessionQueries,
		s.tierQueries,
		s.checkoutQueries,
		s.reservationQueries,
		s.inventoryQueries,
		s.promoQueries,
		s.ticketQueries,
		s.credentialQueries,
		s.funnelQueries,
		s.pool,
		s.logger,
		s.audit,
		s.publicFeedRL,
		hcheckout.PricingRules(s.pricingRules),
		trustedProxies,
	).WithMediaSigner(s.signedMediaURL)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These keep the original unexported type names live in package httpserver so
// test files (feed_tokens_test.go, public_feed_checkout_153_test.go) compile
// without importing the hfeed sub-package.

type feedTokenResponse = hfeed.FeedTokenResponse
type publicFeedCheckoutStartRequest = hfeed.PublicFeedCheckoutStartRequest
type publicGAItem = hfeed.PublicGAItem

// ─── pure-function forwarders ─────────────────────────────────────────────────
// feed_tokens_test.go calls these unqualified — keep the original lowercase
// names live in package httpserver so callers do not learn about the hfeed
// sub-package.

// feedTokenFromRow forwards to hfeed.FeedTokenFromRow.
func feedTokenFromRow(ft gen.FeedTokenRow) feedTokenResponse {
	return hfeed.FeedTokenFromRow(ft)
}

// generateFeedToken forwards to hfeed.GenerateFeedToken.
func generateFeedToken() (string, error) {
	return hfeed.GenerateFeedToken()
}

// ─── feed token management handler shims ──────────────────────────────────────

func (s *Server) handleCreateFeedToken(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleCreateFeedToken(w, r)
}

func (s *Server) handleListFeedTokens(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleListFeedTokens(w, r)
}

func (s *Server) handleGetFeedToken(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleGetFeedToken(w, r)
}

func (s *Server) handleRevokeFeedToken(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleRevokeFeedToken(w, r)
}

// ─── public feed handler shims ────────────────────────────────────────────────

func (s *Server) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeed(w, r)
}

func (s *Server) handlePublicFeedEvents(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedEvents(w, r)
}

func (s *Server) handlePublicFeedEvent(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedEvent(w, r)
}

// ─── hosted sales page resolver shim ──────────────────────────────────────────

func (s *Server) handlePublicPage(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicPage(w, r)
}

// ─── public feed checkout handler shim ────────────────────────────────────────

func (s *Server) handlePublicFeedCheckoutStart(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedCheckoutStart(w, r)
}

// ─── public checkout status handler shims (feature #319 WID-0b) ──────────────

func (s *Server) handlePublicCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandleGetPublicCheckoutStatus(w, r)
}

func (s *Server) handlePublicTicketPDF(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandleGetPublicTicketPDF(w, r)
}

// ─── hold-expiry recovery handler shim (feature #320 WID-0c) ─────────────────

func (s *Server) handlePublicCheckoutRecover(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutRecover(w, r)
}

// ─── widget funnel telemetry shim (feature #322 WID-0e) ──────────────────────

func (s *Server) handlePublicFeedFunnelEvents(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePostFunnelEvents(w, r)
}

// feed_shims.go bridges the *Server god-object to the hfeed sub-package. All
// handler bodies live in hfeed/; these thin delegating methods preserve the
// unexported *Server method surface so mount_catalog.go and the structural
// test files (feed_tokens_test.go, public_feed_152_test.go,
// public_feed_checkout_153_test.go) compile unchanged.
//
// The public feed rate limiter (publicFeedRateLimiter /
// newPublicFeedRateLimiter) is kept live in this file — public_feed_152_test.go
// drives its unexported checkToken / checkIP methods directly and
// server_struct.go holds the concrete *publicFeedRateLimiter field. The type
// satisfies the narrower hfeed.RateLimiter interface via the CheckToken /
// CheckIP wrapper methods below. The rateLimiterWindow helper struct also
// stays here because scanner_shims.go shares it.
package httpserver

import (
	"net/http"
	"sync"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
)

// ─── in-memory rate limiter (kept in package httpserver for tests) ────────────

// rateLimiterWindow tracks request count within a rolling 1-minute window.
type rateLimiterWindow struct {
	count   int
	resetAt time.Time
}

// publicFeedRateLimiter is a simple in-memory token-bucket rate limiter.
// It tracks per-token and per-IP request counts with 1-minute windows.
// The limiter is safe for concurrent use.
type publicFeedRateLimiter struct {
	mu         sync.Mutex
	tokenLimit int
	ipLimit    int
	tokens     map[string]*rateLimiterWindow
	ips        map[string]*rateLimiterWindow
}

// newPublicFeedRateLimiter creates a rate limiter with the given per-token and
// per-IP limits (requests per minute).
func newPublicFeedRateLimiter(tokenLimit, ipLimit int) *publicFeedRateLimiter {
	return &publicFeedRateLimiter{
		tokenLimit: tokenLimit,
		ipLimit:    ipLimit,
		tokens:     make(map[string]*rateLimiterWindow),
		ips:        make(map[string]*rateLimiterWindow),
	}
}

// check increments the counter for key in the given window map and returns true
// when the request is allowed (counter <= limit) and false when it is blocked.
func (rl *publicFeedRateLimiter) check(m map[string]*rateLimiterWindow, key string, limit int) bool {
	now := time.Now()
	w, ok := m[key]
	if !ok || now.After(w.resetAt) {
		m[key] = &rateLimiterWindow{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	w.count++
	return w.count <= limit
}

// checkToken increments the per-token counter and returns true when allowed.
func (rl *publicFeedRateLimiter) checkToken(token string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.tokens, token, rl.tokenLimit)
}

// checkIP increments the per-IP counter and returns true when allowed.
func (rl *publicFeedRateLimiter) checkIP(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.ips, ip, rl.ipLimit)
}

// CheckToken / CheckIP are exported adapter methods so *publicFeedRateLimiter
// satisfies the hfeed.RateLimiter interface. They forward to the private
// implementations above unchanged.
func (rl *publicFeedRateLimiter) CheckToken(token string) bool { return rl.checkToken(token) }

// CheckIP forwards to the unexported checkIP helper.
func (rl *publicFeedRateLimiter) CheckIP(ip string) bool { return rl.checkIP(ip) }

// ─── handler construction ─────────────────────────────────────────────────────

// feedHandler constructs an hfeed.Handler from the server's dependencies. A
// fresh handler per request keeps the wiring uniform with hbilling / hgeo /
// hgdpr and avoids stale captures when test code mutates *Server fields
// between calls. The promo-code validator is injected as a callback; its
// canonical implementation is hcheckout.ValidatePromoCode (the pricing domain
// moved into hcheckout in phase 1m).
func (s *Server) feedHandler() *hfeed.Handler {
	return hfeed.New(
		s.feedTokenQueries,
		s.publicFeedQueries,
		s.sessionQueries,
		s.tierQueries,
		s.checkoutQueries,
		s.reservationQueries,
		s.inventoryQueries,
		s.promoQueries,
		s.pool,
		s.logger,
		s.audit,
		s.publicFeedRL,
		hcheckout.PricingRules(s.pricingRules),
		hcheckout.ValidatePromoCode,
	)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These keep the original unexported type names live in package httpserver so
// test files (feed_tokens_test.go, public_feed_checkout_153_test.go) compile
// without importing the hfeed sub-package.

type feedTokenResponse = hfeed.FeedTokenResponse
type publicFeedCheckoutStartRequest = hfeed.PublicFeedCheckoutStartRequest

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
	s.feedHandler().HandleCreateFeedToken(w, r)
}

func (s *Server) handleListFeedTokens(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandleListFeedTokens(w, r)
}

func (s *Server) handleGetFeedToken(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandleGetFeedToken(w, r)
}

func (s *Server) handleRevokeFeedToken(w http.ResponseWriter, r *http.Request) {
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

// ─── public feed checkout handler shim ────────────────────────────────────────

func (s *Server) handlePublicFeedCheckoutStart(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedCheckoutStart(w, r)
}

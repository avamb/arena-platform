// public_feed_ratelimit.go holds the in-memory rate limiter of the public feed
// routes (publicFeedRateLimiter) and the resolution of its limits from config.
// public_feed_152_test.go drives the unexported checkFeedToken /
// checkCheckoutToken / checkIP methods directly and server_struct.go holds the
// concrete *publicFeedRateLimiter field. The type satisfies the narrower
// hfeed.RateLimiter interface via the CheckFeedToken / CheckCheckoutToken /
// CheckIP wrapper methods. The rateLimiterWindow helper struct is shared with
// scanner_shims.go.
package httpserver

import (
	"sync"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
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
// The counters live in this process's memory: with several replicas each
// keeps its own, so the effective ceiling is the limit times the replica count.
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

	// Refused promo-code attempts (see config.PublicPromoFailTokenLimit):
	// counted only when a code is refused, checked before a request that
	// names one is evaluated. A limit <= 0 disables that check.
	promoFailTokenLimit int
	promoFailIPLimit    int
	promoFailTokens     map[string]*rateLimiterWindow
	promoFailIPs        map[string]*rateLimiterWindow

	// Page statistics beacon (HP-2a, see config.PublicPageTrackEventLimit):
	// a bucket per event page and one per client IP, both separate from the
	// general buckets above. A limit <= 0 disables that check.
	pageTrackEventLimit int
	pageTrackIPLimit    int
	pageTrackEvents     map[string]*rateLimiterWindow
	pageTrackIPs        map[string]*rateLimiterWindow
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
		promoFailTokens:    make(map[string]*rateLimiterWindow),
		promoFailIPs:       make(map[string]*rateLimiterWindow),
		pageTrackEvents:    make(map[string]*rateLimiterWindow),
		pageTrackIPs:       make(map[string]*rateLimiterWindow),
	}
}

// newPublicFeedRateLimiterFromConfig builds the limiter from every public
// limit on cfg (defaults when cfg is nil), the refused-promo ones included.
func newPublicFeedRateLimiterFromConfig(cfg *config.Config) *publicFeedRateLimiter {
	rl := newPublicFeedRateLimiter(publicFeedTokenRateLimit(cfg), publicCheckoutTokenRateLimit(cfg), publicAPIIPRateLimit(cfg))
	rl.promoFailTokenLimit, rl.promoFailIPLimit = defaultPublicPromoFailTokenLimit, defaultPublicPromoFailIPLimit
	rl.pageTrackEventLimit, rl.pageTrackIPLimit = defaultPublicPageTrackEventLimit, defaultPublicPageTrackIPLimit
	if cfg != nil {
		rl.promoFailTokenLimit, rl.promoFailIPLimit = cfg.PublicPromoFailTokenLimit, cfg.PublicPromoFailIPLimit
		rl.pageTrackEventLimit, rl.pageTrackIPLimit = cfg.PublicPageTrackEventLimit, cfg.PublicPageTrackIPLimit
	}
	return rl
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
	sweepExpiredWindows(rl.promoFailTokens, now)
	sweepExpiredWindows(rl.promoFailIPs, now)
	sweepExpiredWindows(rl.pageTrackEvents, now)
	sweepExpiredWindows(rl.pageTrackIPs, now)
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
	return rl.check(rl.ips, ipRateKey(ip), rl.ipLimit)
}

// ipRateKey is the key a client address is counted under. An IPv4 address is
// its own key. An IPv6 address is keyed by its /64 prefix: one subscriber is
// normally given a whole /64, so counting single addresses would let one client
// step through 2^64 addresses and never meet a per-IP limit. An address that
// does not parse is returned unchanged.
//
// The rule lives in httputil.ClientKey, shared with the short-link scan
// de-duplication (hgo), so the two can never key one client differently.
func ipRateKey(ip string) string {
	return httputil.ClientKey(ip)
}

// windowFull reports whether key's window is already at limit, without
// counting anything: the peek half of the refused-promo limit. limit <= 0
// means disabled (never full).
func (rl *publicFeedRateLimiter) windowFull(m map[string]*rateLimiterWindow, key string, limit int, now time.Time) (bool, int) {
	if limit <= 0 {
		return false, 0
	}
	w, ok := m[key]
	if !ok || now.After(w.resetAt) || w.count < limit {
		return false, 0
	}
	remaining := int(w.resetAt.Sub(now) / time.Second)
	if w.resetAt.Sub(now)%time.Second != 0 || remaining < 1 {
		remaining++
	}
	return true, remaining
}

// takeSlot counts one more attempt in key's window and returns that window, or
// nil when the check is disabled (limit <= 0). The caller has already found
// the budget not full.
func (rl *publicFeedRateLimiter) takeSlot(m map[string]*rateLimiterWindow, key string, limit int, now time.Time) *rateLimiterWindow {
	if limit <= 0 {
		return nil
	}
	if w, ok := m[key]; ok && !now.After(w.resetAt) {
		w.count++
		return w
	}
	w := &rateLimiterWindow{count: 1, resetAt: now.Add(time.Minute)}
	m[key] = w
	return w
}

// ReservePromoAttempt takes one slot of the refused-promo budget of this feed
// token and of this client IP, atomically with the check that they are not
// full. Nothing is taken when either is full. release gives the slot back (an
// accepted code is not a refusal); a refused code simply keeps it.
func (rl *publicFeedRateLimiter) ReservePromoAttempt(token, ip string) (func(), bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rl.sweepExpiredLocked(now)
	key := ipRateKey(ip)
	fullTok, retryTok := rl.windowFull(rl.promoFailTokens, token, rl.promoFailTokenLimit, now)
	fullIP, retryIP := rl.windowFull(rl.promoFailIPs, key, rl.promoFailIPLimit, now)
	if fullTok || fullIP {
		retry := retryTok
		if retryIP > retry {
			retry = retryIP
		}
		return nil, true, retry
	}
	tokWin := rl.takeSlot(rl.promoFailTokens, token, rl.promoFailTokenLimit, now)
	ipWin := rl.takeSlot(rl.promoFailIPs, key, rl.promoFailIPLimit, now)
	return func() {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		for _, w := range []*rateLimiterWindow{tokWin, ipWin} {
			if w != nil && w.count > 0 {
				w.count--
			}
		}
	}, false, 0
}

// CheckPageTrack counts one page-statistics beacon in the bucket of its event
// page and in the bucket of its client IP, BOTH on every call (never
// short-circuited: a burst that trips one bucket must still fill the other).
// eventKey is the lower-cased slug pair of the page, not an id: the check runs
// before any database read, so an unknown page costs no query. It reports the
// larger Retry-After of the buckets that blocked.
func (rl *publicFeedRateLimiter) CheckPageTrack(eventKey, ip string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	okEvent, retryEvent := rl.check(rl.pageTrackEvents, eventKey, rl.pageTrackEventLimit)
	okIP, retryIP := rl.check(rl.pageTrackIPs, ipRateKey(ip), rl.pageTrackIPLimit)
	if okEvent && okIP {
		return true, 0
	}
	retry := retryEvent
	if retryIP > retry {
		retry = retryIP
	}
	return false, retry
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
	defaultPublicPromoFailTokenLimit    = 300
	defaultPublicPromoFailIPLimit       = 10
	defaultPublicPageTrackEventLimit    = 6000
	defaultPublicPageTrackIPLimit       = 120
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

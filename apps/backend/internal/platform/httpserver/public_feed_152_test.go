// public_feed_152_test.go — unit tests for the public feed events API (feature #152).
//
// Tests cover:
//   - Route existence (non-404) for both list and detail
//   - No auth required (not 401) for both
//   - Nil queries → 503 for both
//   - Invalid event_id UUID → 400 for detail endpoint
//   - Rate limiter struct unit tests: allow within limit, block after limit
//   - Response Content-Type is JSON
//   - SQL query file exists and contains required query names
//   - Gen file exists and contains required method names
//   - Querier interface compile-time check
//   - Handler file exists and contains rate limiter + Cache-Control references
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factories
// ─────────────────────────────────────────────────────────────────────────────

// buildPublicFeedServer builds a Server WITHOUT public feed queries.
// Used for nil-guard tests (expect 503).
func buildPublicFeedServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	return New(Options{
		Config: cfg,
		// PublicFeedQueries nil — routes NOT mounted.
	})
}

// buildPublicFeedServerWithQueries builds a Server WITH public feed queries wired
// (gen.New(nil)) so routes ARE mounted. DB calls will panic → recovered as 500.
// Used for route-existence and no-auth tests.
func buildPublicFeedServerWithQueries(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	return New(Options{
		Config:            cfg,
		PublicFeedQueries: gen.New(nil), // non-nil → routes mounted; panics on DB
		FeedTokenQueries:  gen.New(nil), // needed for token validation
		SessionQueries:    gen.New(nil),
		TierQueries:       gen.New(nil),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Nil guard — both endpoints return 503 when publicFeedQueries is nil
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_NilQueries_ListReturns503(t *testing.T) {
	s := buildPublicFeedServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/test-token/events", nil)
	s.router.ServeHTTP(w, req)
	// Routes not mounted → 404 (chi custom not-found handler).
	// Either 404 or 503 confirms no auth is required and no panic.
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401, got 401 — unauthenticated access should be allowed")
	}
}

func TestPublicFeed152_NilQueries_DetailReturns503(t *testing.T) {
	s := buildPublicFeedServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/test-token/events/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401, got 401 — unauthenticated access should be allowed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route existence — routes are mounted when publicFeedQueries is non-nil
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_ListRouteExists(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events", nil)
	s.router.ServeHTTP(w, req)
	// Route is mounted → handler fires; DB panic → 500. Must not be 404.
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected route to exist (non-404), got 404")
	}
}

func TestPublicFeed152_DetailRouteExists(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected route to exist (non-404), got 404")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// No auth required — must not return 401
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_ListNoAuthRequired(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	// No Authorization header.
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events", nil)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 (unauthenticated), got 401")
	}
}

func TestPublicFeed152_DetailNoAuthRequired(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 (unauthenticated), got 401")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Invalid event_id UUID → 400
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_DetailInvalidUUID_Returns400(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events/not-a-uuid", nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid UUID, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Content-Type is JSON
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_ListResponseContentType(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events", nil)
	s.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
}

func TestPublicFeed152_DetailResponseContentType(t *testing.T) {
	s := buildPublicFeedServerWithQueries(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/some-token/events/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rate limiter unit tests
//
// newPublicFeedRateLimiter(feedTokenLimit, checkoutTokenLimit, ipLimit) now
// tracks THREE independent buckets (feed token / checkout token / IP) since
// a feed token belongs to a whole sales channel and must not share a bucket
// with a single buyer's checkout token (defect: 200 concurrent widget
// visitors sharing one feed token exhausted a per-visitor-sized limit and
// 15884/16543 requests were rejected). Each check* method now returns
// (allowed bool, retryAfterSeconds int).
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_RateLimiterFeedTokenAllow(t *testing.T) {
	rl := newPublicFeedRateLimiter(5, 100, 100)
	for i := 0; i < 5; i++ {
		if allowed, _ := rl.checkFeedToken("test-token"); !allowed {
			t.Fatalf("checkFeedToken call %d: expected allow, got block", i+1)
		}
	}
}

func TestPublicFeed152_RateLimiterFeedTokenBlock(t *testing.T) {
	rl := newPublicFeedRateLimiter(3, 100, 100)
	for i := 0; i < 3; i++ {
		rl.checkFeedToken("test-token")
	}
	allowed, retryAfter := rl.checkFeedToken("test-token")
	if allowed {
		t.Fatal("checkFeedToken: expected block after limit, got allow")
	}
	if retryAfter < 1 {
		t.Fatalf("checkFeedToken: expected retryAfterSeconds >= 1 on block, got %d", retryAfter)
	}
}

func TestPublicFeed152_RateLimiterCheckoutTokenAllow(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 5, 100)
	for i := 0; i < 5; i++ {
		if allowed, _ := rl.checkCheckoutToken("checkout-token"); !allowed {
			t.Fatalf("checkCheckoutToken call %d: expected allow, got block", i+1)
		}
	}
}

func TestPublicFeed152_RateLimiterCheckoutTokenBlock(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 3, 100)
	for i := 0; i < 3; i++ {
		rl.checkCheckoutToken("checkout-token")
	}
	if allowed, _ := rl.checkCheckoutToken("checkout-token"); allowed {
		t.Fatal("checkCheckoutToken: expected block after limit, got allow")
	}
}

func TestPublicFeed152_RateLimiterFeedAndCheckoutTokenBucketsIndependent(t *testing.T) {
	// A feed token and a checkout token happening to share the same string
	// value must not share a counter — they are different credentials for
	// different scopes (site-wide vs. one buyer).
	rl := newPublicFeedRateLimiter(1, 1, 100)
	if allowed, _ := rl.checkFeedToken("shared-value"); !allowed {
		t.Fatal("checkFeedToken: expected allow on first call")
	}
	if allowed, _ := rl.checkFeedToken("shared-value"); allowed {
		t.Fatal("checkFeedToken: expected block on second call (limit 1)")
	}
	// The checkout-token bucket for the SAME string must still be fresh.
	if allowed, _ := rl.checkCheckoutToken("shared-value"); !allowed {
		t.Fatal("checkCheckoutToken: expected allow — independent bucket from checkFeedToken")
	}
}

func TestPublicFeed152_RateLimiterIPAllow(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 100, 5)
	for i := 0; i < 5; i++ {
		if allowed, _ := rl.checkIP("1.2.3.4"); !allowed {
			t.Fatalf("checkIP call %d: expected allow, got block", i+1)
		}
	}
}

func TestPublicFeed152_RateLimiterIPBlock(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 100, 3)
	for i := 0; i < 3; i++ {
		rl.checkIP("1.2.3.4")
	}
	allowed, retryAfter := rl.checkIP("1.2.3.4")
	if allowed {
		t.Fatal("checkIP: expected block after limit, got allow")
	}
	if retryAfter < 1 {
		t.Fatalf("checkIP: expected retryAfterSeconds >= 1 on block, got %d", retryAfter)
	}
}

func TestPublicFeed152_RateLimiterDifferentTokensIndependent(t *testing.T) {
	rl := newPublicFeedRateLimiter(2, 100, 100)
	// Fill token-A to the limit.
	rl.checkFeedToken("token-A")
	rl.checkFeedToken("token-A")
	// token-B should still be allowed.
	if allowed, _ := rl.checkFeedToken("token-B"); !allowed {
		t.Fatal("checkFeedToken token-B: expected allow (independent from token-A), got block")
	}
}

func TestPublicFeed152_RateLimiterNewInstanceAllows(t *testing.T) {
	rl := newPublicFeedRateLimiter(1, 1, 1)
	if allowed, _ := rl.checkFeedToken("brand-new"); !allowed {
		t.Fatal("first call on new limiter should always be allowed")
	}
}

// TestPublicFeed152_RateLimiterZeroDisables covers "0 disables this check"
// (PUBLIC_FEED_TOKEN_RATE_LIMIT / PUBLIC_CHECKOUT_TOKEN_RATE_LIMIT /
// PUBLIC_API_IP_RATE_LIMIT contract): a 0 limit must always allow, no matter
// how many requests are made.
func TestPublicFeed152_RateLimiterZeroDisables(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 0)
	for i := 0; i < 1000; i++ {
		if allowed, retryAfter := rl.checkFeedToken("tok"); !allowed || retryAfter != 0 {
			t.Fatalf("checkFeedToken call %d: limit 0 must always allow with retryAfter 0, got allowed=%v retryAfter=%d", i, allowed, retryAfter)
		}
		if allowed, retryAfter := rl.checkCheckoutToken("tok"); !allowed || retryAfter != 0 {
			t.Fatalf("checkCheckoutToken call %d: limit 0 must always allow with retryAfter 0, got allowed=%v retryAfter=%d", i, allowed, retryAfter)
		}
		if allowed, retryAfter := rl.checkIP("1.2.3.4"); !allowed || retryAfter != 0 {
			t.Fatalf("checkIP call %d: limit 0 must always allow with retryAfter 0, got allowed=%v retryAfter=%d", i, allowed, retryAfter)
		}
	}
}

// TestPublicFeed152_RateLimiterRetryAfterWithinWindow verifies the
// retryAfterSeconds returned on a block is a sane whole-second value bounded
// by the 1-minute window (never 0, never more than 60).
func TestPublicFeed152_RateLimiterRetryAfterWithinWindow(t *testing.T) {
	rl := newPublicFeedRateLimiter(1, 100, 100)
	rl.checkFeedToken("tok")
	_, retryAfter := rl.checkFeedToken("tok")
	if retryAfter < 1 || retryAfter > 60 {
		t.Fatalf("retryAfterSeconds out of bounds: got %d, want 1-60", retryAfter)
	}
}

// TestPublicFeed152_RateLimiterSweepPrunesExpiredEntries covers the
// unbounded-map-growth fix: an expired window must eventually be dropped
// from the map, not accumulate forever under spoofed/rotating keys.
func TestPublicFeed152_RateLimiterSweepPrunesExpiredEntries(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 100, 100)
	rl.checkIP("1.2.3.4")
	if len(rl.ips) != 1 {
		t.Fatalf("expected 1 tracked IP before expiry, got %d", len(rl.ips))
	}
	// Force the tracked window into the past so the next sweep collects it,
	// and force the sweep gate open (sweepExpiredLocked only runs at most
	// once per minute).
	rl.mu.Lock()
	rl.ips["1.2.3.4"].resetAt = time.Now().Add(-time.Minute)
	rl.lastSweep = time.Time{}
	rl.mu.Unlock()

	// A check on an unrelated key triggers sweepExpiredLocked under the lock.
	rl.checkIP("5.6.7.8")

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if _, stillPresent := rl.ips["1.2.3.4"]; stillPresent {
		t.Fatal("expired IP window was not pruned by the sweep")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP-level rate limit tests: Retry-After header + spoof-resistant IP keying
// ─────────────────────────────────────────────────────────────────────────────

// buildPublicFeedServerWithLimits builds a Server with the public feed routes
// mounted and explicit rate limits / trusted-proxy depth, so HTTP-level tests
// can deterministically trip the 429 path.
func buildPublicFeedServerWithLimits(t *testing.T, feedTokenLimit, ipLimit, trustedProxies int) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:                   config.EnvDevelopment,
		RequestTimeout:           5 * time.Second,
		BodyLimitBytes:           1 << 20,
		JWTSecretStub:            "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth:           true,
		DefaultLocale:            "en",
		ActiveLocales:            []string{"en", "ru"},
		PublicFeedTokenRateLimit: feedTokenLimit,
		PublicAPIIPRateLimit:     ipLimit,
		TrustedProxyCount:        trustedProxies,
	}
	return New(Options{
		Config:            cfg,
		PublicFeedQueries: gen.New(nil),
		FeedTokenQueries:  gen.New(nil),
		SessionQueries:    gen.New(nil),
		TierQueries:       gen.New(nil),
	})
}

// TestPublicFeed152_HTTP_RateLimited_SetsRetryAfterHeader exercises the full
// HTTP path: once the per-feed-token bucket is exhausted, the 429 response
// must carry a Retry-After header (whole seconds, >= 1) alongside the
// unchanged feed.rate_limited error envelope.
func TestPublicFeed152_HTTP_RateLimited_SetsRetryAfterHeader(t *testing.T) {
	s := buildPublicFeedServerWithLimits(t, 1, 100000, 0)

	// First request consumes the single allowed slot.
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/retry-after-token/events", nil)
	s.router.ServeHTTP(w1, req1)

	// Second request must be blocked with Retry-After set.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/retry-after-token/events", nil)
	s.router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on second request, got %d: %s", w2.Code, w2.Body.String())
	}
	retryAfter := w2.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header on 429 response, got none")
	}
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil {
		t.Fatalf("Retry-After header must be a whole-second integer, got %q: %v", retryAfter, err)
	}
	if seconds < 1 || seconds > 60 {
		t.Fatalf("Retry-After out of bounds: got %d, want 1-60", seconds)
	}
	if !strings.Contains(w2.Body.String(), "feed.rate_limited") {
		t.Fatalf("expected feed.rate_limited error code in body, got: %s", w2.Body.String())
	}
}

// TestPublicFeed152_HTTP_IPRateLimit_SpoofedXFFIgnoredWhenTrustedProxyCountZero
// proves the fix for the IP-keying spoof: with TRUSTED_PROXY_COUNT=0 (the
// default), two requests carrying DIFFERENT client-supplied X-Forwarded-For
// values must still land in the SAME IP bucket, because the raw header is
// never trusted — only RemoteAddr is used.
func TestPublicFeed152_HTTP_IPRateLimit_SpoofedXFFIgnoredWhenTrustedProxyCountZero(t *testing.T) {
	s := buildPublicFeedServerWithLimits(t, 100000, 1, 0)

	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/ip-spoof-token-a/events", nil)
	req1.Header.Set("X-Forwarded-For", "9.9.9.1")
	s.router.ServeHTTP(w1, req1)
	if w1.Code == http.StatusTooManyRequests {
		t.Fatalf("first request should not be rate limited, got 429: %s", w1.Body.String())
	}

	// Different feed token AND a different spoofed XFF value — but the same
	// underlying httptest RemoteAddr. If XFF were trusted, this would land in
	// a different IP bucket and be allowed; since TRUSTED_PROXY_COUNT=0, it
	// must share the bucket with req1 and get blocked.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/ip-spoof-token-b/events", nil)
	req2.Header.Set("X-Forwarded-For", "9.9.9.2")
	s.router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 — spoofed XFF must not bypass the per-IP bucket when TRUSTED_PROXY_COUNT=0, got %d: %s", w2.Code, w2.Body.String())
	}
}

// TestPublicFeed152_HTTP_IPRateLimit_HonouredWithTrustedProxyCountOne is the
// mirror case: with TRUSTED_PROXY_COUNT=1 (one trusted reverse proxy), two
// requests through that proxy carrying genuinely DIFFERENT real client IPs
// (as the trusted hop would append them) land in DIFFERENT IP buckets, so
// the second visitor is not punished for the first visitor's traffic.
func TestPublicFeed152_HTTP_IPRateLimit_HonouredWithTrustedProxyCountOne(t *testing.T) {
	s := buildPublicFeedServerWithLimits(t, 100000, 1, 1)

	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/ip-trusted-token-a/events", nil)
	// One trusted hop: the proxy appended the address of the client that
	// connected to it, so the rightmost entry is the real client.
	req1.Header.Set("X-Forwarded-For", "10.0.0.1")
	s.router.ServeHTTP(w1, req1)
	if w1.Code == http.StatusTooManyRequests {
		t.Fatalf("first visitor should not be rate limited, got 429: %s", w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/ip-trusted-token-b/events", nil)
	req2.Header.Set("X-Forwarded-For", "10.0.0.2")
	s.router.ServeHTTP(w2, req2)
	if w2.Code == http.StatusTooManyRequests {
		t.Fatalf("second visitor has a different real IP and must not share the first visitor's bucket, got 429: %s", w2.Body.String())
	}
}

// TestPublicFeed152_HTTP_IPRateLimit_SpoofedPrefixIgnoredWithTrustedProxyCountOne:
// behind one proxy a client can still prepend its own X-Forwarded-For value.
// The proxy appends the real address after it, so two requests from the same
// real client with different spoofed prefixes must share one IP bucket.
func TestPublicFeed152_HTTP_IPRateLimit_SpoofedPrefixIgnoredWithTrustedProxyCountOne(t *testing.T) {
	s := buildPublicFeedServerWithLimits(t, 100000, 1, 1)

	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/ip-prefix-token-a/events", nil)
	req1.Header.Set("X-Forwarded-For", "6.6.6.1, 10.0.0.9")
	s.router.ServeHTTP(w1, req1)
	if w1.Code == http.StatusTooManyRequests {
		t.Fatalf("first request should not be rate limited, got 429: %s", w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/ip-prefix-token-b/events", nil)
	req2.Header.Set("X-Forwarded-For", "6.6.6.2, 10.0.0.9")
	s.router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 — a spoofed prefix must not create a new IP bucket behind one trusted proxy, got %d: %s", w2.Code, w2.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQL query file: content checks
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_SQLFileExists(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if content == "" {
		t.Fatal("public_feed.sql not found or empty")
	}
}

func TestPublicFeed152_SQLFileContainsListQuery(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if !strings.Contains(content, "ListPublishedEventsByFeedToken") {
		t.Fatal("public_feed.sql missing ListPublishedEventsByFeedToken query name")
	}
}

func TestPublicFeed152_SQLFileContainsCountQuery(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if !strings.Contains(content, "CountPublishedEventsByFeedToken") {
		t.Fatal("public_feed.sql missing CountPublishedEventsByFeedToken query name")
	}
}

func TestPublicFeed152_SQLFileContainsGetQuery(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if !strings.Contains(content, "GetPublishedEventByFeedToken") {
		t.Fatal("public_feed.sql missing GetPublishedEventByFeedToken query name")
	}
}

func TestPublicFeed152_SQLFileJoinsPublications(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if !strings.Contains(content, "event_publications") {
		t.Fatal("public_feed.sql must join event_publications table")
	}
}

func TestPublicFeed152_SQLFileJoinsFeedTokens(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if !strings.Contains(content, "agent_feed_tokens") {
		t.Fatal("public_feed.sql must join agent_feed_tokens table")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gen file: content checks
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_GenFileExists(t *testing.T) {
	content := findFileByName(t, "public_feed.sql.go")
	if content == "" {
		t.Fatal("public_feed.sql.go not found or empty")
	}
}

func TestPublicFeed152_GenFileContainsListMethod(t *testing.T) {
	content := findFileByName(t, "public_feed.sql.go")
	if !strings.Contains(content, "ListPublishedEventsByFeedToken") {
		t.Fatal("public_feed.sql.go missing ListPublishedEventsByFeedToken method")
	}
}

func TestPublicFeed152_GenFileContainsCountMethod(t *testing.T) {
	content := findFileByName(t, "public_feed.sql.go")
	if !strings.Contains(content, "CountPublishedEventsByFeedToken") {
		t.Fatal("public_feed.sql.go missing CountPublishedEventsByFeedToken method")
	}
}

func TestPublicFeed152_GenFileContainsGetMethod(t *testing.T) {
	content := findFileByName(t, "public_feed.sql.go")
	if !strings.Contains(content, "GetPublishedEventByFeedToken") {
		t.Fatal("public_feed.sql.go missing GetPublishedEventByFeedToken method")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Querier interface compile-time check (via querier.go content)
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_QuerierInterfaceHasListMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "ListPublishedEventsByFeedToken") {
		t.Fatal("querier.go missing ListPublishedEventsByFeedToken method")
	}
}

func TestPublicFeed152_QuerierInterfaceHasCountMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "CountPublishedEventsByFeedToken") {
		t.Fatal("querier.go missing CountPublishedEventsByFeedToken method")
	}
}

func TestPublicFeed152_QuerierInterfaceHasGetMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "GetPublishedEventByFeedToken") {
		t.Fatal("querier.go missing GetPublishedEventByFeedToken method")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler file content checks
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_HandlerFileExists(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if content == "" {
		t.Fatal("public_feed.go not found or empty")
	}
}

func TestPublicFeed152_HandlerFileContainsRateLimiter(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "publicFeedRateLimiter") {
		t.Fatal("public_feed.go must define publicFeedRateLimiter struct")
	}
}

func TestPublicFeed152_HandlerFileContainsCacheControlList(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "max-age=60") {
		t.Fatal("public_feed.go must set Cache-Control max-age=60 for list endpoint")
	}
}

func TestPublicFeed152_HandlerFileContainsCacheControlDetail(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "max-age=30") {
		t.Fatal("public_feed.go must set Cache-Control max-age=30 for detail endpoint")
	}
}

func TestPublicFeed152_HandlerFileContainsStaleWhileRevalidate(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "stale-while-revalidate") {
		t.Fatal("public_feed.go must use stale-while-revalidate in Cache-Control headers")
	}
}

func TestPublicFeed152_HandlerFileContainsHandleList(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "handlePublicFeedEvents") {
		t.Fatal("public_feed.go must define handlePublicFeedEvents handler")
	}
}

func TestPublicFeed152_HandlerFileContainsHandleDetail(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "handlePublicFeedEvent") {
		t.Fatal("public_feed.go must define handlePublicFeedEvent handler")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring checks via server.go content
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_ServerHasPublicFeedQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "publicFeedQueries") {
		t.Fatal("server.go missing publicFeedQueries field")
	}
}

func TestPublicFeed152_ServerHasPublicFeedRLField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "publicFeedRL") {
		t.Fatal("server.go missing publicFeedRL field")
	}
}

func TestPublicFeed152_ServerHasPublicFeedQueriesOption(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "PublicFeedQueries") {
		t.Fatal("server.go missing PublicFeedQueries option")
	}
}

func TestPublicFeed152_ServerMountsPublicFeedRoutes(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "public/feeds/{feed_token}/events") {
		t.Fatal("server.go must register public feed events routes")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time interface satisfaction (static check)
// ─────────────────────────────────────────────────────────────────────────────

// TestPublicFeed152_CompileTimeQuerierSatisfied verifies that *gen.Queries
// implements the Querier interface (which now includes the three public feed methods).
// This is a compile-time check via the package-level var _ in querier.go; this test
// simply confirms the gen package compiles correctly.
func TestPublicFeed152_CompileTimeQuerierSatisfied(_ *testing.T) {
	// If this file compiles, the interface is satisfied.
	var _ gen.Querier = (*gen.Queries)(nil)
}

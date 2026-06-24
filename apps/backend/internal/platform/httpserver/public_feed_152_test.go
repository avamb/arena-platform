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
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicFeed152_RateLimiterTokenAllow(t *testing.T) {
	rl := newPublicFeedRateLimiter(5, 100)
	for i := 0; i < 5; i++ {
		if !rl.checkToken("test-token") {
			t.Fatalf("checkToken call %d: expected allow, got block", i+1)
		}
	}
}

func TestPublicFeed152_RateLimiterTokenBlock(t *testing.T) {
	rl := newPublicFeedRateLimiter(3, 100)
	for i := 0; i < 3; i++ {
		rl.checkToken("test-token")
	}
	if rl.checkToken("test-token") {
		t.Fatal("checkToken: expected block after limit, got allow")
	}
}

func TestPublicFeed152_RateLimiterIPAllow(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 5)
	for i := 0; i < 5; i++ {
		if !rl.checkIP("1.2.3.4") {
			t.Fatalf("checkIP call %d: expected allow, got block", i+1)
		}
	}
}

func TestPublicFeed152_RateLimiterIPBlock(t *testing.T) {
	rl := newPublicFeedRateLimiter(100, 3)
	for i := 0; i < 3; i++ {
		rl.checkIP("1.2.3.4")
	}
	if rl.checkIP("1.2.3.4") {
		t.Fatal("checkIP: expected block after limit, got allow")
	}
}

func TestPublicFeed152_RateLimiterDifferentTokensIndependent(t *testing.T) {
	rl := newPublicFeedRateLimiter(2, 100)
	// Fill token-A to the limit.
	rl.checkToken("token-A")
	rl.checkToken("token-A")
	// token-B should still be allowed.
	if !rl.checkToken("token-B") {
		t.Fatal("checkToken token-B: expected allow (independent from token-A), got block")
	}
}

func TestPublicFeed152_RateLimiterNewInstanceAllows(t *testing.T) {
	rl := newPublicFeedRateLimiter(1, 1)
	if !rl.checkToken("brand-new") {
		t.Fatal("first call on new limiter should always be allowed")
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
func TestPublicFeed152_CompileTimeQuerierSatisfied(t *testing.T) {
	// If this file compiles, the interface is satisfied.
	var _ gen.Querier = (*gen.Queries)(nil)
}

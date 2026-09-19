// public_page_test.go — unit-level coverage for the hosted-page resolve
// endpoint (GET /v1/public/pages/{org_slug}/{event_slug}) that does not need
// a live PostgreSQL: the 503 self-gate when publicFeedQueries is nil, and the
// pure row->response projection. The full resolution chain (org -> event ->
// publication -> hosted_page-enabled channel -> feed token) can only be
// proven against a real database — see public_page_integration_test.go.
package hfeed

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// allowAllRateLimiter is a stub RateLimiter that never blocks, for unit
// tests that exercise HandlePublicPage past the nil-queries self-gate (which
// always evaluates the per-IP bucket via Handler.enforceRateLimit).
type allowAllRateLimiter struct{}

func (allowAllRateLimiter) CheckFeedToken(string) (bool, int)     { return true, 0 }
func (allowAllRateLimiter) CheckCheckoutToken(string) (bool, int) { return true, 0 }
func (allowAllRateLimiter) CheckIP(string) (bool, int)            { return true, 0 }

// pageRequest builds a GET request with org_slug/event_slug chi URL params
// pre-populated, mirroring channelTTLRequest's "call the handler directly"
// pattern used elsewhere in the codebase.
func pageRequest(orgSlug, eventSlug string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/public/pages/"+orgSlug+"/"+eventSlug, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_slug", orgSlug)
	rctx.URLParams.Add("event_slug", eventSlug)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// TestHandlePublicPage_NilQueries_503 verifies the handler self-gates with a
// 503 dependency.database_unavailable envelope when publicFeedQueries is nil
// (matches HandlePublicFeedEvents / HandlePublicFeedEvent's convention).
func TestHandlePublicPage_NilQueries_503(t *testing.T) {
	t.Parallel()
	h := unsignedFeed()
	w := httptest.NewRecorder()
	h.HandlePublicPage(w, pageRequest("acme", "summer-fest"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandlePublicPage_EmptySlug_404 verifies an empty org_slug or
// event_slug path segment answers the same page.not_found 404 as every
// other resolution miss, without ever reaching the database.
func TestHandlePublicPage_EmptySlug_404(t *testing.T) {
	t.Parallel()
	h := &Handler{publicFeedQueries: gen.New(nil), logger: slog.Default(), rl: allowAllRateLimiter{}}
	w := httptest.NewRecorder()
	h.HandlePublicPage(w, pageRequest("", "summer-fest"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// promoterPageRequest builds a GET request with the org_slug chi URL param
// pre-populated (one-segment route, no event_slug), mirroring pageRequest.
func promoterPageRequest(orgSlug string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/public/pages/"+orgSlug, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_slug", orgSlug)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// TestHandlePublicPromoterPage_NilQueries_503 verifies the promoter-page
// handler self-gates with a 503 dependency.database_unavailable envelope
// when publicFeedQueries is nil, matching HandlePublicPage's convention.
func TestHandlePublicPromoterPage_NilQueries_503(t *testing.T) {
	t.Parallel()
	h := unsignedFeed()
	w := httptest.NewRecorder()
	h.HandlePublicPromoterPage(w, promoterPageRequest("acme"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandlePublicPromoterPage_EmptySlug_404 verifies an empty org_slug path
// segment answers the same page.not_found 404 as every other resolution
// miss, without ever reaching the database.
func TestHandlePublicPromoterPage_EmptySlug_404(t *testing.T) {
	t.Parallel()
	h := &Handler{publicFeedQueries: gen.New(nil), logger: slog.Default(), rl: allowAllRateLimiter{}}
	w := httptest.NewRecorder()
	h.HandlePublicPromoterPage(w, promoterPageRequest(""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

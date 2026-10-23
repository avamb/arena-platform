package hfeed

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// trackLimiter blocks the beacon after `allow` calls and records the keys it
// was asked about.
type trackLimiter struct {
	allowAllRateLimiter
	allow int
	calls int
	keys  []string
	ips   []string
}

func (l *trackLimiter) CheckPageTrack(key, ip string) (bool, int) {
	l.calls++
	l.keys = append(l.keys, key)
	l.ips = append(l.ips, ip)
	if l.calls > l.allow {
		return false, 7
	}
	return true, 0
}

func trackRequest(org, event, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/public/pages/"+org+"/"+event+"/track", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_slug", org)
	rctx.URLParams.Add("event_slug", event)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestPageTrack_NilQueries_503(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	unsignedFeed().HandlePublicPageTrack(w, trackRequest("acme", "show", `{"kind":"page_view"}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// The limits run before the body is read and before any database read: an
// over-limit request is 429 track.rate_limited with Retry-After, and the
// queries (which here would panic on use) are never touched.
func TestPageTrack_OverTheLimitIs429BeforeAnyReads(t *testing.T) {
	t.Parallel()
	metrics, err := observability.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	lim := &trackLimiter{allow: 0}
	h := (&Handler{publicFeedQueries: gen.New(nil), logger: slog.Default(), rl: lim}).WithMetrics(metrics)

	w := httptest.NewRecorder()
	h.HandlePublicPageTrack(w, trackRequest("Acme", "Show", `{"kind":"page_view"}`))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "track.rate_limited") {
		t.Errorf("body = %s", w.Body.String())
	}
	if w.Header().Get("Retry-After") != "7" {
		t.Errorf("Retry-After = %q, want 7", w.Header().Get("Retry-After"))
	}
	// The bucket is the lower-cased slug pair, never an id or an address.
	if len(lim.keys) != 1 || lim.keys[0] != "acme/show" {
		t.Errorf("limiter key = %v", lim.keys)
	}
	if got := testutil.ToFloat64(metrics.PageTrackTotal.WithLabelValues("other", observability.PageTrackRateLimited)); got != 1 {
		t.Errorf("rate_limited metric = %v, want 1", got)
	}
}

func TestPageTrack_BadBodiesAreRefusedBeforeTheDatabase(t *testing.T) {
	t.Parallel()
	metrics, _ := observability.New(prometheus.NewRegistry())
	h := (&Handler{publicFeedQueries: gen.New(nil), logger: slog.Default(), rl: allowAllRateLimiter{}}).WithMetrics(metrics)
	cases := []struct{ name, body, code string }{
		{"empty", ``, "track.invalid_body"},
		{"not json", `page_view`, "track.invalid_body"},
		{"array", `["page_view"]`, "track.invalid_body"},
		{"trailing data", `{"kind":"page_view"} {}`, "track.invalid_body"},
		{"no kind", `{}`, "track.invalid_kind"},
		{"unknown kind", `{"kind":"purchase"}`, "track.invalid_kind"},
		{"case matters", `{"kind":"PAGE_VIEW"}`, "track.invalid_kind"},
		{"oversize", `{"kind":"page_view","utm_source":"` + strings.Repeat("a", 3000) + `"}`, "track.invalid_body"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.HandlePublicPageTrack(w, trackRequest("acme", "show", c.body))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.code) {
			t.Errorf("%s: %d %s, want 400 %s", c.name, w.Code, w.Body.String(), c.code)
		}
	}
}

package hanalytics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type brokenDB struct{}

func (brokenDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("db down")
}
func (brokenDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func adminRouter(h *AdminHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/v1/admin/analytics/flows", h.HandleFlows)
	r.Get("/v1/admin/analytics/errors", h.HandleErrors)
	r.Get("/v1/admin/analytics/people/{person_id}/timeline", h.HandleTimeline)
	return r
}

func adminGet(h *AdminHandler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	adminRouter(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestAdmin_NoDatabaseAnswers503(t *testing.T) {
	h := NewAdmin(AdminOptions{})
	for _, p := range []string{
		"/v1/admin/analytics/flows",
		"/v1/admin/analytics/errors",
		"/v1/admin/analytics/people/6f1c2c34-9b7d-4a76-8c52-5b5a3c3b2a10/timeline",
	} {
		if w := adminGet(h, p); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
}

func TestAdmin_RefusesBadParameters(t *testing.T) {
	h := NewAdmin(AdminOptions{DB: brokenDB{}, Now: func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) }})
	cases := []struct{ path, code string }{
		{"/v1/admin/analytics/flows?surface=widget", "analytics.invalid_surface"},
		{"/v1/admin/analytics/flows?from=2026-11-05&to=2026-11-01", "analytics.invalid_period"},
		{"/v1/admin/analytics/errors?from=yesterday", "analytics.invalid_period"},
		{"/v1/admin/analytics/errors?surface=x", "analytics.invalid_surface"},
		{"/v1/admin/analytics/people/not-a-uuid/timeline", "analytics.invalid_person_id"},
		{"/v1/admin/analytics/people/6f1c2c34-9b7d-4a76-8c52-5b5a3c3b2a10/timeline?limit=0", "analytics.invalid_limit"},
	}
	for _, c := range cases {
		w := adminGet(h, c.path)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.code) {
			t.Errorf("%s = %d %s, want 400 %s", c.path, w.Code, w.Body.String(), c.code)
		}
	}
	// Valid parameters reach the database; a broken one answers 503, not 500.
	if w := adminGet(h, "/v1/admin/analytics/flows?surface=bot&from=2026-10-26&to=2026-11-01"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("broken database = %d", w.Code)
	}
}

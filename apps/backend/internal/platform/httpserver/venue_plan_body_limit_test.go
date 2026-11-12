package httpserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hvenueplan"
)

// The hall plan upload routes get past the global body limit (1 MiB by
// default); every other route keeps it.
func TestVenuePlanUploadBodyLimit_OnlyTheUploadRoutes(t *testing.T) {
	cases := []struct {
		method, path string
		want         int64
	}{
		{http.MethodPost, "/v1/organizations/o/venues/v/plan-intakes", hvenueplan.CreateBodyLimit},
		{http.MethodPost, "/v1/admin/plan-intakes/i/drafts", hvenueplan.DraftBodyLimit},
		{http.MethodPost, "/v1/organizations/o/plan-intakes/i/answers", 0},
		{http.MethodPost, "/v1/media", 0},
		{http.MethodPatch, "/v1/organizations/o/venues/v/plan-intakes", 0},
		{http.MethodPost, "/v1/organizations/o/venues/v/plan-intakes/x", 0},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := venuePlanUploadBodyLimit(r); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, got, c.want)
		}
	}

	read := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mw := httpadapter.JSONBodyLimitWithOverride(1<<20, venuePlanUploadBodyLimit)(read)
	body := bytes.Repeat([]byte("x"), 2<<20)
	for path, want := range map[string]int{
		"/v1/organizations/o/venues/v/plan-intakes": http.StatusOK,
		"/v1/organizations/o/venues":                http.StatusRequestEntityTooLarge,
	} {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		if rec.Code != want {
			t.Errorf("POST %s with 2 MiB: %d, want %d", path, rec.Code, want)
		}
	}
}

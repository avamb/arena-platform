package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
)

// Every API response carries X-Content-Type-Options: nosniff exactly once —
// a normal answer, a 404, a panic's 500, and a handler that sets the header
// itself.
func TestNewRouter_NoSniffOnEveryResponse(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Deps{AppEnv: "production"})
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/own", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/panic", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})

	for _, path := range []string{"/ok", "/own", "/panic", "/missing"} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		got := rr.Header().Values("X-Content-Type-Options")
		if len(got) != 1 || got[0] != "nosniff" {
			t.Errorf("%s (status %d): X-Content-Type-Options = %q, want one nosniff", path, rr.Code, got)
		}
	}
}

package honboarding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestRequireSuperadmin(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := RequireSuperadmin(next)

	t.Run("without the superadmin marker", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/onboarding/applications", nil)
		req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{ID: "00000000-0000-0000-0000-000000000001"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if reached {
			t.Fatal("the handler was reached without the superadmin marker")
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if body.Error.Code != "superadmin.required" {
			t.Fatalf("code = %q, want superadmin.required (%s)", body.Error.Code, rec.Body.String())
		}
	})

	t.Run("with the superadmin marker", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/onboarding/applications", nil)
		req = req.WithContext(auth.WithSuperadminOrgAccess(req.Context()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !reached {
			t.Fatalf("status = %d reached = %v, want 200 and reached", rec.Code, reached)
		}
	})
}

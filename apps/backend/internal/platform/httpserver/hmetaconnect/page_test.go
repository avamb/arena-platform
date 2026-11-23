package hmetaconnect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// TestHandlePage_Guards: an API key is refused before anything else and a
// missing database answers 503.
func TestHandlePage_Guards(t *testing.T) {
	call := func(h *Handler, actor auth.Actor) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/page", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("org_id", uuid.NewString())
		ctx := auth.WithActor(context.WithValue(req.Context(), chi.RouteCtxKey, rc), actor)
		w := httptest.NewRecorder()
		h.HandlePage(w, req.WithContext(ctx))
		return w
	}
	h := unitHandler(t, "")
	if w := call(h, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService}); w.Code != 403 || !strings.Contains(w.Body.String(), "meta.api_key_forbidden") {
		t.Fatalf("api key: %d %s", w.Code, w.Body.String())
	}
	if w := call(h, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser}); w.Code != 503 || !strings.Contains(w.Body.String(), "dependency.database_unavailable") {
		t.Fatalf("no database: %d %s", w.Code, w.Body.String())
	}
}

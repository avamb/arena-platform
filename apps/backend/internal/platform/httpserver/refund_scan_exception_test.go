package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// Without the refund engine the exception route answers 503 and records
// nothing: a grant is never accepted on a guess.
func TestGrantScanException_NoEngineIsUnavailable(t *testing.T) {
	s := &Server{}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", uuid.NewString())
	rctx.URLParams.Add("ticket_id", uuid.NewString())
	ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
	ctx = auth.WithSuperadminOrgAccess(ctx)
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"hours":2}`)).WithContext(ctx)
	r.Header.Set("X-Admin-Reason", "show stopped")
	w := httptest.NewRecorder()
	s.handleGrantScanException(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), refunds.CodeEngineUnavailable) {
		t.Fatalf("grant without the refund engine: %d %s; want 503 %s", w.Code, w.Body.String(), refunds.CodeEngineUnavailable)
	}
}

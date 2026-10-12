package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// TestPathOrganization_NoRouteContext: a request that never went through a
// chi router has no route context; the lookup must answer "no organization"
// instead of panicking.
func TestPathOrganization_NoRouteContext(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	if id, ok := pathOrganization(r); ok || id != uuid.Nil {
		t.Fatalf("pathOrganization without route context = %v, %v; want nil, false", id, ok)
	}
}

func TestPathOrganization_ReadsOrgIDAndOrganizationsID(t *testing.T) {
	want := uuid.New()

	rc := chi.NewRouteContext()
	rc.URLParams.Add("org_id", want.String())
	r := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rc))
	if id, ok := pathOrganization(r); !ok || id != want {
		t.Fatalf("org_id: got %v, %v", id, ok)
	}

	rc = chi.NewRouteContext()
	rc.RoutePatterns = []string{"/v1/organizations/{id}"}
	rc.URLParams.Add("id", want.String())
	r = httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rc))
	if id, ok := pathOrganization(r); !ok || id != want {
		t.Fatalf("/organizations/{id}: got %v, %v", id, ok)
	}
}

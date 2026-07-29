//go:build integration

package hiam

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/tests/pgtest"
)

func TestAdminOrganizationLegalPatchGetRoundTrip(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()
	q := gen.New(pool)
	created, err := q.InsertOrganization(context.Background(), "PATCH legal", "patch-legal", "DE", "en", 1200)
	if err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	h := New(q, q, q, pool, nil, slog.Default(), nil, nil)
	body := []byte(`{"legal_name":"TEST_392 Arena GmbH","tax_id":"DE123456789","tax_id_scheme":"eu_vat","registration_number":"HRB 392","legal_address_line1":"Test Strasse 392","legal_address_line2":"Suite 392","legal_address_postal_code":"10115","legal_address_city":"Berlin","legal_address_country":"de","contact_email":"legal392@example.test","contact_phone":"+4930123456","website_url":"https://example.test","kyb_status":"pending"}`)
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/organizations/"+created.ID.String(), bytes.NewReader(body))
	req.Header.Set("X-Admin-Reason", "verify legal entity persistence")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", created.ID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	patchRec := httptest.NewRecorder()
	h.HandleAdminUpdateOrg(patchRec, req)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", patchRec.Code, patchRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/admin/organizations", nil)
	getReq.Header.Set("X-Admin-Reason", "verify legal entity persistence")
	getRec := httptest.NewRecorder()
	h.HandleSuperadminListOrganizations(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", getRec.Code, getRec.Body.String())
	}
	var response struct {
		Organizations []map[string]any `json:"organizations"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if len(response.Organizations) != 1 || response.Organizations[0]["legal_name"] != "TEST_392 Arena GmbH" || response.Organizations[0]["kyb_status"] != "pending" {
		t.Fatalf("GET legal-field roundtrip failed: %s", getRec.Body.String())
	}
}

func TestSuperadminOrganizationSearchAndPagination(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()
	q := gen.New(pool)
	for _, org := range []struct{ name, slug string }{
		{"TEST_401 Arena North", "test-401-north"},
		{"TEST_401 Arena South", "test-401-south"},
		{"Unrelated organization", "unrelated-401"},
	} {
		if _, err := q.InsertOrganization(context.Background(), org.name, org.slug, "DE", "en", 1200); err != nil {
			t.Fatalf("insert %q: %v", org.slug, err)
		}
	}
	h := New(q, q, q, pool, nil, slog.Default(), nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/organizations?q=TEST_401&limit=1&offset=1", nil)
	req.Header.Set("X-Admin-Reason", "verify TEST_401 organization search and pagination")
	rec := httptest.NewRecorder()
	h.HandleSuperadminListOrganizations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Organizations []struct {
			Slug string `json:"slug"`
		} `json:"organizations"`
		Total  int64 `json:"total"`
		Limit  int32 `json:"limit"`
		Offset int32 `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 2 || response.Limit != 1 || response.Offset != 1 || len(response.Organizations) != 1 || response.Organizations[0].Slug != "test-401-south" {
		t.Fatalf("unexpected paginated response: %s", rec.Body.String())
	}
}

// network_orgs_210_test.go -- unit tests for feature #210
// (network organizer/agent assignment endpoints under
// /v1/admin/networks/{id}/organizers and .../agents).
//
// Covers:
//   - Route mounting and auth gating for all six endpoints (3 per kind).
//   - Handler input validation: nil-queries 503 short-circuit, empty body /
//     invalid JSON / missing organization_id / malformed organization_id
//     400s, invalid path-UUID 400s.
//   - Mount-file content check: the new organizers/agents patterns plus
//     the network.manage_organizers and network.manage_agents permission
//     strings are present so the binding pattern in
//     0044_network_permissions.sql remains the effective gate.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Route mounting & auth gating
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkOrgs210_RoutesRequireAuth(t *testing.T) {
	netID := uuid.New().String()
	orgID := uuid.New().String()
	cases := []struct {
		method, path, body string
	}{
		// organizers
		{http.MethodGet, "/v1/admin/networks/" + netID + "/organizers", ""},
		{http.MethodPost, "/v1/admin/networks/" + netID + "/organizers",
			`{"organization_id":"` + orgID + `"}`},
		{http.MethodDelete, "/v1/admin/networks/" + netID + "/organizers/" + orgID, ""},
		// agents
		{http.MethodGet, "/v1/admin/networks/" + netID + "/agents", ""},
		{http.MethodPost, "/v1/admin/networks/" + netID + "/agents",
			`{"organization_id":"` + orgID + `"}`},
		{http.MethodDelete, "/v1/admin/networks/" + netID + "/agents/" + orgID, ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			s := buildNetworkUserServer209(t) // reuse the #209 builder
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s returned 404 — route not mounted", tc.method, tc.path)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: expected 401 without JWT, got %d (body=%s)",
					tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler validation — call handlers directly to bypass middleware. Each
// scenario is run once per assignment_kind to confirm both closures share
// the same input-validation contract.
// ─────────────────────────────────────────────────────────────────────────────

func eachKind(t *testing.T, fn func(t *testing.T, kind string)) {
	t.Helper()
	for _, kind := range []string{
		networkAssignmentKindOrganizer,
		networkAssignmentKindAgent,
	} {
		kind := kind
		t.Run(kind, func(t *testing.T) { fn(t, kind) })
	}
}

func TestNetworkOrgs210_Attach_NilQueriesReturns503(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
		h := s.handleAttachNetworkOrganization(kind)
		req := httptest.NewRequest(http.MethodPost,
			"/v1/admin/networks/"+uuid.New().String()+"/"+kind+"s",
			strings.NewReader(`{"organization_id":"`+uuid.New().String()+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})
}

func TestNetworkOrgs210_Attach_EmptyBodyReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
			pool:           &dbDownPool{},
		}
		h := s.handleAttachNetworkOrganization(kind)
		req := chiPathRequest(http.MethodPost,
			"/v1/admin/networks/x/"+kind+"s", http.NoBody,
			map[string]string{"id": uuid.New().String()})
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, orgRespJSON(t, rec)); code != "network_org.empty_body" {
			t.Errorf("error.code = %q want network_org.empty_body", code)
		}
	})
}

func TestNetworkOrgs210_Attach_InvalidJSONReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
			pool:           &dbDownPool{},
		}
		h := s.handleAttachNetworkOrganization(kind)
		req := chiPathRequest(http.MethodPost,
			"/v1/admin/networks/x/"+kind+"s",
			strings.NewReader("not-json"),
			map[string]string{"id": uuid.New().String()})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
		if code := errorCode(t, orgRespJSON(t, rec)); code != "network_org.invalid_json" {
			t.Errorf("error.code = %q want network_org.invalid_json", code)
		}
	})
}

func TestNetworkOrgs210_Attach_MissingOrgIDReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
			pool:           &dbDownPool{},
		}
		h := s.handleAttachNetworkOrganization(kind)
		req := chiPathRequest(http.MethodPost,
			"/v1/admin/networks/x/"+kind+"s",
			strings.NewReader(`{"organization_id":""}`),
			map[string]string{"id": uuid.New().String()})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
		if code := errorCode(t, orgRespJSON(t, rec)); code != "network_org.invalid_organization_id" {
			t.Errorf("error.code = %q want network_org.invalid_organization_id", code)
		}
	})
}

func TestNetworkOrgs210_Attach_MalformedOrgIDReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
			pool:           &dbDownPool{},
		}
		h := s.handleAttachNetworkOrganization(kind)
		req := chiPathRequest(http.MethodPost,
			"/v1/admin/networks/x/"+kind+"s",
			strings.NewReader(`{"organization_id":"not-a-uuid"}`),
			map[string]string{"id": uuid.New().String()})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
		if code := errorCode(t, orgRespJSON(t, rec)); code != "network_org.invalid_organization_id" {
			t.Errorf("error.code = %q want network_org.invalid_organization_id", code)
		}
	})
}

func TestNetworkOrgs210_Attach_InvalidNetworkIDReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
			pool:           &dbDownPool{},
		}
		h := s.handleAttachNetworkOrganization(kind)
		req := chiPathRequest(http.MethodPost,
			"/v1/admin/networks/x/"+kind+"s",
			strings.NewReader(`{"organization_id":"`+uuid.New().String()+`"}`),
			map[string]string{"id": "not-a-uuid"})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid path UUID, got %d", rec.Code)
		}
	})
}

func TestNetworkOrgs210_Detach_NilQueriesReturns503(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
		h := s.handleDetachNetworkOrganization(kind)
		req := httptest.NewRequest(http.MethodDelete,
			"/v1/admin/networks/"+uuid.New().String()+"/"+kind+"s/"+uuid.New().String(), nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})
}

func TestNetworkOrgs210_Detach_InvalidOrgIDReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
			pool:           &dbDownPool{},
		}
		h := s.handleDetachNetworkOrganization(kind)
		req := chiPathRequest(http.MethodDelete,
			"/v1/admin/networks/x/"+kind+"s/y", nil,
			map[string]string{"id": uuid.New().String(), "orgId": "not-a-uuid"})
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid orgId, got %d", rec.Code)
		}
	})
}

func TestNetworkOrgs210_List_NilQueriesReturns503(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
		h := s.handleListNetworkOrganizations(kind)
		req := httptest.NewRequest(http.MethodGet,
			"/v1/admin/networks/"+uuid.New().String()+"/"+kind+"s", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})
}

func TestNetworkOrgs210_List_InvalidNetworkIDReturns400(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		s := &Server{
			cfg:            &config.Config{DefaultLocale: "en"},
			networkQueries: gen.New(nil),
		}
		h := s.handleListNetworkOrganizations(kind)
		req := chiPathRequest(http.MethodGet,
			"/v1/admin/networks/x/"+kind+"s", nil,
			map[string]string{"id": "not-a-uuid"})
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid path UUID, got %d", rec.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Mount-file content check — guards permissions and route patterns against
// silent drift from the bindings in 0044_network_permissions.sql.
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkOrgs210_MountFileUsesCorrectPermissions(t *testing.T) {
	body := readSourceFile(t, "mount_networks.go")
	for _, want := range []string{
		`"network.manage_organizers"`,
		`"network.manage_agents"`,
		`"/admin/networks/{id}/organizers"`,
		`"/admin/networks/{id}/organizers/{orgId}"`,
		`"/admin/networks/{id}/agents"`,
		`"/admin/networks/{id}/agents/{orgId}"`,
		`handleAttachNetworkOrganization`,
		`handleDetachNetworkOrganization`,
		`handleListNetworkOrganizations`,
		`networkAssignmentKindOrganizer`,
		`networkAssignmentKindAgent`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mount_networks.go missing %q", want)
		}
	}
}

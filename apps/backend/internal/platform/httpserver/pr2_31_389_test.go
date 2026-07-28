// pr2_31_389_test.go verifies the org-membership enforcement added by feature
// #389 (PR2-31): closing the two org-scoped surfaces PR2-26 left unguarded —
// external allocations and complimentary issuances.
//
// Before this fix, a permission-holder in Org A could read/write Org B's
// external allocations and complimentary tickets by supplying B's UUID in the
// URL path: the routes were gated only by RBAC (allocation.*, complimentary.*)
// with no membership check against the {org_id} path parameter.
//
// Test coverage mirrors pr2_26_382_test.go:
//
//   - Denial tests (emptyMembershipDBTX): every guarded surface returns 403
//     org.access_denied for a non-member.
//   - Admission tests (orgMemberAdmitFromCtxDBTX): the same surfaces proceed
//     past the membership check for a member (any status except 401/403).
//   - Revoke: POST /v1/complimentary/{id}/revoke resolves the owning org from
//     the issuance row (not the URL); a non-member of that org is denied.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const pr231ActorID = "00000000-0000-0000-0000-000000000389"

// pr231IssuanceOrgID is the org that owns the fake issuance served by
// issuanceRowDBTX in the revoke denial test. The actor is not a member.
var pr231IssuanceOrgID = uuid.MustParse("00000000-0000-0000-0000-00000000b0b1")

// ─────────────────────────────────────────────────────────────────────────────
// issuanceRowDBTX — fake DBTX whose QueryRow serves a single complimentary
// issuance row. Only ID and OrgID are populated; the PR2-31 revoke shim reads
// nothing else before the membership check.
// ─────────────────────────────────────────────────────────────────────────────

type issuanceRow struct {
	id    uuid.UUID
	orgID uuid.UUID
}

func (r issuanceRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		if p, ok := dest[0].(*uuid.UUID); ok {
			*p = r.id
		}
	}
	if len(dest) > 1 {
		if p, ok := dest[1].(*uuid.UUID); ok {
			*p = r.orgID
		}
	}
	if len(dest) > 7 {
		if p, ok := dest[7].(*string); ok {
			*p = "issued"
		}
	}
	return nil
}

type issuanceRowDBTX struct {
	orgID uuid.UUID
}

func (d *issuanceRowDBTX) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (d *issuanceRowDBTX) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return emptyRows{}, nil
}

func (d *issuanceRowDBTX) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	id := uuid.Nil
	if len(args) > 0 {
		if v, ok := args[0].(uuid.UUID); ok {
			id = v
		}
	}
	orgID := d.orgID
	if orgID == uuid.Nil {
		// Self-owned mode: the issuance's org equals the issuance ID itself,
		// which lets orgMemberAdmitFromCtxDBTX (keyed on the {id} URL param)
		// admit the actor in the revoke admission test.
		orgID = id
	}
	return issuanceRow{id: id, orgID: orgID}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server factories
// ─────────────────────────────────────────────────────────────────────────────

func pr231Config() *config.Config {
	return &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
}

// buildPR231Server builds a Server with allocation + complimentary routes
// mounted. memberDBTX decides membership; issuanceDBTX serves the issuance row
// for the revoke path.
func buildPR231Server(t *testing.T, memberDBTX gen.DBTX, issuanceDBTX gen.DBTX) *Server {
	t.Helper()
	cfg := pr231Config()
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildPR231Server: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		MembershipQueries:    gen.New(memberDBTX),
		AllocationQueries:    gen.New(nil),
		InventoryQueries:     gen.New(nil),
		ComplimentaryQueries: gen.New(issuanceDBTX),
		BarcodeQueries:       gen.New(nil),
		CredentialQueries:    gen.New(nil),
		Audit:                &captureAuditWriter{},
	})
}

func doPR231Request(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+mintJWT(t, s.stub, pr231ActorID))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

// pr231GuardedSurfaces enumerates every org-scoped surface guarded by PR2-31.
// Paths embed a fresh org UUID at call time via the org placeholder.
func pr231GuardedSurfaces(orgID, resourceID uuid.UUID) []struct {
	name   string
	method string
	path   string
	body   string
} {
	org := orgID.String()
	res := resourceID.String()
	return []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"AllocationCreate", http.MethodPost, "/v1/organizations/" + org + "/external-allocations",
			`{"session_id":"` + res + `","operator_name":"X","qty":1}`},
		{"AllocationList", http.MethodGet, "/v1/organizations/" + org + "/external-allocations", ""},
		{"AllocationGet", http.MethodGet, "/v1/organizations/" + org + "/external-allocations/" + res, ""},
		{"AllocationPatch", http.MethodPatch, "/v1/organizations/" + org + "/external-allocations/" + res,
			`{"status":"active"}`},
		{"ComplimentaryCreate", http.MethodPost, "/v1/organizations/" + org + "/complimentary",
			`{"session_id":"` + res + `","qty":1,"recipients":[{"name":"X"}],"batch_id":"b1","issued_by":"t"}`},
		{"ComplimentaryList", http.MethodGet, "/v1/organizations/" + org + "/complimentary", ""},
		{"ComplimentaryGet", http.MethodGet, "/v1/organizations/" + org + "/complimentary/" + res, ""},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Denial tests — non-member gets 403 org.access_denied on every surface
// ─────────────────────────────────────────────────────────────────────────────

func TestPR231_OrgScopedSurfacesDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR231Server(t, &emptyMembershipDBTX{}, &issuanceRowDBTX{orgID: pr231IssuanceOrgID})
	for _, tc := range pr231GuardedSurfaces(uuid.New(), uuid.New()) {
		t.Run(tc.name, func(t *testing.T) {
			w := doPR231Request(t, s, tc.method, tc.path, tc.body)
			assertOrgAccessDenied(t, w)
		})
	}
}

// TestPR231_RevokeDeniedForNonMemberOfIssuanceOrg proves the revoke route —
// which has no {org_id} URL parameter — still enforces membership against the
// org resolved from the issuance row.
func TestPR231_RevokeDeniedForNonMemberOfIssuanceOrg(t *testing.T) {
	t.Parallel()
	s := buildPR231Server(t, &emptyMembershipDBTX{}, &issuanceRowDBTX{orgID: pr231IssuanceOrgID})
	w := doPR231Request(t, s, http.MethodPost,
		"/v1/complimentary/"+uuid.New().String()+"/revoke", "")
	assertOrgAccessDenied(t, w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Admission tests — member proceeds past the guard (never 401/403)
// ─────────────────────────────────────────────────────────────────────────────

func TestPR231_OrgScopedSurfacesAdmitMember(t *testing.T) {
	t.Parallel()
	s := buildPR231Server(t, &orgMemberAdmitFromCtxDBTX{}, &issuanceRowDBTX{orgID: pr231IssuanceOrgID})
	for _, tc := range pr231GuardedSurfaces(uuid.New(), uuid.New()) {
		t.Run(tc.name, func(t *testing.T) {
			w := doPR231Request(t, s, tc.method, tc.path, tc.body)
			if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
				t.Errorf("member should pass the org guard; got %d (body: %s)", w.Code, w.Body.String())
			}
		})
	}
}

// TestPR231_RevokeAdmitsMemberOfIssuanceOrg uses the self-owned issuance mode
// (issuance.org_id == issuance.id) so orgMemberAdmitFromCtxDBTX — which admits
// the org matching the {id} URL param — admits the actor for the resolved org.
func TestPR231_RevokeAdmitsMemberOfIssuanceOrg(t *testing.T) {
	t.Parallel()
	s := buildPR231Server(t, &orgMemberAdmitFromCtxDBTX{}, &issuanceRowDBTX{})
	w := doPR231Request(t, s, http.MethodPost,
		"/v1/complimentary/"+uuid.New().String()+"/revoke", "")
	if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
		t.Errorf("member should pass the org guard on revoke; got %d (body: %s)", w.Code, w.Body.String())
	}
}

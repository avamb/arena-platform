// pr2_01_org_isolation_test.go verifies that every org-scoped handler enforces
// membership isolation (PR2-01: cross-tenant authorization bypass fix).
//
// The tests exercise the security invariant that an actor who is a member of
// Org A cannot read or mutate Org B's resources through the org-scoped API
// surfaces affected by PR2-01:
//
//   - GET/POST/PATCH/DELETE /v1/organizations/{org_id}/bank-accounts[/{id}]
//   - GET/POST/PATCH/DELETE /v1/organizations/{org_id}/payment-configs[/{id}]
//   - GET/POST/PATCH/DELETE /v1/organizations/{org_id}/channels[/{id}]
//   - GET/POST/PATCH/DELETE /v1/organizations/{org_id}/venues (org-scoped only)
//   - PATCH /v1/organizations/{id}
//
// Each test mounts a Server with MembershipQueries backed by a fake DBTX that
// returns an EMPTY membership list (actor has no memberships in any org). The
// handler under test is called with orgID=orgB in the path. The expected
// response is HTTP 403 with error code "org.access_denied".
//
// All tests are pure unit tests — no live PostgreSQL required.
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

// ─────────────────────────────────────────────────────────────────────────────
// Fake DBTX that returns empty rows for every Query call.
// This simulates an actor who has no memberships in any org.
// ─────────────────────────────────────────────────────────────────────────────

// emptyRows is a pgx.Rows implementation that is already closed with no data.
type emptyRows struct{}

func (emptyRows) Close()                                       {}
func (emptyRows) Err() error                                   { return nil }
func (emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyRows) Next() bool                                   { return false }
func (emptyRows) Scan(_ ...any) error                          { return nil }
func (emptyRows) Values() ([]any, error)                       { return nil, nil }
func (emptyRows) RawValues() [][]byte                          { return nil }
func (emptyRows) Conn() *pgx.Conn                              { return nil }

// emptyMembershipDBTX implements gen.DBTX and returns empty rows for all
// queries. Used to simulate a member lookup that returns no memberships.
type emptyMembershipDBTX struct{}

func (e *emptyMembershipDBTX) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (e *emptyMembershipDBTX) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return emptyRows{}, nil
}

func (e *emptyMembershipDBTX) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for PR2-01 isolation tests
// ─────────────────────────────────────────────────────────────────────────────

const pr2OrgIsolationActorID = "00000000-0000-0000-0000-000000000357"

// buildPR201Server builds a Server with:
//   - Stub JWT auth (actor pr2OrgIsolationActorID — member of OrgA only)
//   - MembershipQueries backed by emptyMembershipDBTX (returns no memberships)
//   - All affected domain query fields set to gen.New(nil) (routes mount, but DB fails)
//   - Pool set to dbDownPool (no real DB ops proceed past membership check)
func buildPR201Server(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildPR201Server: NewStubProvider: %v", err)
	}
	// MembershipQueries backed by emptyMembershipDBTX returns no memberships,
	// so the actor appears as a non-member of every org.
	membershipQ := gen.New(&emptyMembershipDBTX{})

	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		MembershipQueries:    membershipQ,
		OrgQueries:           gen.New(nil),
		BankAccountQueries:   gen.New(nil),
		PaymentConfigQueries: gen.New(nil),
		ChannelQueries:       gen.New(nil),
		VenueQueries:         gen.New(nil),
		Audit:                &captureAuditWriter{},
	})
}

func pr201Token(t *testing.T, s *Server) string {
	t.Helper()
	return mintJWT(t, s.stub, pr2OrgIsolationActorID)
}

// doOrgIsolationRequest fires an authenticated request and returns the recorder.
func doOrgIsolationRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+pr201Token(t, s))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

func assertOrgAccessDenied(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d (body: %s)", w.Code, w.Body.String())
		return
	}
	body := w.Body.String()
	if !strings.Contains(body, "org.access_denied") {
		t.Errorf("expected error code org.access_denied in body, got: %s", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Bank account isolation tests (PR2-01)
// ─────────────────────────────────────────────────────────────────────────────

func TestPR201_BankAccount_ListDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodGet,
		"/v1/organizations/"+orgB.String()+"/bank-accounts", "")
	assertOrgAccessDenied(t, w)
}

func TestPR201_BankAccount_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPost,
		"/v1/organizations/"+orgB.String()+"/bank-accounts",
		`{"holder_name":"X","currency":"USD","country":"US","iban":"US12345"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_BankAccount_UpdateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPatch,
		"/v1/organizations/"+orgB.String()+"/bank-accounts/"+itemID.String(),
		`{"holder_name":"Y"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_BankAccount_DeleteDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodDelete,
		"/v1/organizations/"+orgB.String()+"/bank-accounts/"+itemID.String(), "")
	assertOrgAccessDenied(t, w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Payment config isolation tests (PR2-01)
// ─────────────────────────────────────────────────────────────────────────────

func TestPR201_PaymentConfig_ListDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodGet,
		"/v1/organizations/"+orgB.String()+"/payment-configs", "")
	assertOrgAccessDenied(t, w)
}

func TestPR201_PaymentConfig_GetDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodGet,
		"/v1/organizations/"+orgB.String()+"/payment-configs/"+itemID.String(), "")
	assertOrgAccessDenied(t, w)
}

func TestPR201_PaymentConfig_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPost,
		"/v1/organizations/"+orgB.String()+"/payment-configs",
		`{"provider":"stripe","mode":"test"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_PaymentConfig_UpdateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPatch,
		"/v1/organizations/"+orgB.String()+"/payment-configs/"+itemID.String(),
		`{"is_active":false}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_PaymentConfig_DeleteDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodDelete,
		"/v1/organizations/"+orgB.String()+"/payment-configs/"+itemID.String(), "")
	assertOrgAccessDenied(t, w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Channel isolation tests (PR2-01)
// ─────────────────────────────────────────────────────────────────────────────

func TestPR201_Channel_ListDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodGet,
		"/v1/organizations/"+orgB.String()+"/channels", "")
	assertOrgAccessDenied(t, w)
}

func TestPR201_Channel_GetDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodGet,
		"/v1/organizations/"+orgB.String()+"/channels/"+itemID.String(), "")
	assertOrgAccessDenied(t, w)
}

func TestPR201_Channel_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPost,
		"/v1/organizations/"+orgB.String()+"/channels",
		`{"name":"test","payment_mode":"direct_merchant","provider":"stripe","provider_account_id":"acct_123"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_Channel_UpdateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPatch,
		"/v1/organizations/"+orgB.String()+"/channels/"+itemID.String(),
		`{"name":"new-name"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_Channel_DeleteDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodDelete,
		"/v1/organizations/"+orgB.String()+"/channels/"+itemID.String(), "")
	assertOrgAccessDenied(t, w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Venue isolation tests (PR2-01) — org-scoped endpoints only
// ─────────────────────────────────────────────────────────────────────────────

func TestPR201_Venue_ListByOrgDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodGet,
		"/v1/organizations/"+orgB.String()+"/venues", "")
	assertOrgAccessDenied(t, w)
}

func TestPR201_Venue_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPost,
		"/v1/organizations/"+orgB.String()+"/venues",
		`{"name":"Arena B"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_Venue_UpdateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPatch,
		"/v1/organizations/"+orgB.String()+"/venues/"+itemID.String(),
		`{"name":"Arena B v2"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR201_Venue_DeleteDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	itemID := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodDelete,
		"/v1/organizations/"+orgB.String()+"/venues/"+itemID.String(), "")
	assertOrgAccessDenied(t, w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Org settings isolation tests (PR2-01)
// ─────────────────────────────────────────────────────────────────────────────

func TestPR201_Org_UpdateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR201Server(t)
	orgB := uuid.New()
	w := doOrgIsolationRequest(t, s, http.MethodPatch,
		"/v1/organizations/"+orgB.String(),
		`{"name":"Hijacked Org"}`)
	assertOrgAccessDenied(t, w)
}

// pr2_26_382_test.go verifies the org-membership enforcement added by feature
// #382 (PR2-26): extending the fail-closed org-membership guard to all
// remaining org-scoped routes and fixing the fail-open nil-guard.
//
// Test coverage:
//
//   - orgMemberAdmitFromCtxDBTX: a fake DBTX that admits the test actor as a
//     member of whatever org appears in the chi URL context (org_id or id param).
//     Used to update the existing test factories so that authenticated requests
//     through the router continue to succeed now that membership checks are
//     fail-closed.
//
//   - Denial tests: confirm that 403 is returned for each newly-guarded surface
//     when the actor is NOT a member (backed by emptyMembershipDBTX from
//     pr2_01_org_isolation_test.go).
//
//   - Admission tests: confirm that 200/400/5xx (but not 401/403) are returned
//     for the same surfaces when the actor IS a member (backed by
//     orgMemberAdmitFromCtxDBTX).
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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// orgMemberAdmitFromCtxDBTX — fake DBTX that admits the actor for the
// org found in the chi route context (org_id or id URL param).
//
// When ListMembershipsByUser is called, it returns a single membership row
// whose OrgID matches the org_id (or id) URL param from the chi context.
// This means the membership check in actorIsMemberOfOrg will find a match
// and admit the actor, regardless of which org is in the URL.
// ─────────────────────────────────────────────────────────────────────────────

// singleMemberRows is a pgx.Rows implementation that yields exactly one
// membership row and then closes. The row is pre-scanned for the six fields
// that scanMembershipRow expects: id, user_id, org_id, role, status, joined_at.
type singleMemberRows struct {
	delivered bool
	id        uuid.UUID
	userID    uuid.UUID
	orgID     uuid.UUID
	role      string
	status    string
	joinedAt  time.Time
}

func (r *singleMemberRows) Close()                                       {}
func (r *singleMemberRows) Err() error                                   { return nil }
func (r *singleMemberRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *singleMemberRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *singleMemberRows) Values() ([]any, error)                       { return nil, nil }
func (r *singleMemberRows) RawValues() [][]byte                          { return nil }
func (r *singleMemberRows) Conn() *pgx.Conn                              { return nil }

func (r *singleMemberRows) Next() bool {
	if r.delivered {
		return false
	}
	r.delivered = true
	return true
}

// Scan delivers the membership fields in the order expected by scanMembershipRow:
// id, user_id, org_id, role, status, joined_at.
func (r *singleMemberRows) Scan(dest ...any) error {
	if len(dest) < 6 {
		return nil
	}
	if p, ok := dest[0].(*uuid.UUID); ok {
		*p = r.id
	}
	if p, ok := dest[1].(*uuid.UUID); ok {
		*p = r.userID
	}
	if p, ok := dest[2].(*uuid.UUID); ok {
		*p = r.orgID
	}
	if p, ok := dest[3].(*string); ok {
		*p = r.role
	}
	if p, ok := dest[4].(*string); ok {
		*p = r.status
	}
	if p, ok := dest[5].(*time.Time); ok {
		*p = r.joinedAt
	}
	return nil
}

// orgMemberAdmitFromCtxDBTX implements gen.DBTX. When Query is called
// (ListMembershipsByUser), it reads the org_id or id URL parameter from the
// chi route context embedded in ctx and returns a single membership row that
// matches that org UUID. This admits the test actor as a member of any org
// that appears in the request URL.
type orgMemberAdmitFromCtxDBTX struct{}

func (o *orgMemberAdmitFromCtxDBTX) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (o *orgMemberAdmitFromCtxDBTX) Query(ctx context.Context, _ string, args ...any) (pgx.Rows, error) {
	// Extract the org UUID from the chi route context.
	// Handlers use "org_id" for nested resources; "id" for top-level org routes.
	var orgID uuid.UUID
	rctx := chi.RouteContext(ctx)
	if rctx != nil {
		for _, param := range []string{"org_id", "id"} {
			val := rctx.URLParam(param)
			if val == "" {
				continue
			}
			parsed, err := uuid.Parse(val)
			if err == nil {
				orgID = parsed
				break
			}
		}
	}

	// Determine the user UUID from the first Query arg (userID passed to
	// ListMembershipsByUser). Fall back to a fixed test UUID.
	var userID uuid.UUID
	if len(args) > 0 {
		if uid, ok := args[0].(uuid.UUID); ok {
			userID = uid
		}
	}
	if userID == uuid.Nil {
		userID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	}

	return &singleMemberRows{
		id:       uuid.MustParse("00000000-0000-0000-0000-000000000099"),
		userID:   userID,
		orgID:    orgID,
		role:     "org_admin",
		status:   "active",
		joinedAt: time.Now(),
	}, nil
}

func (o *orgMemberAdmitFromCtxDBTX) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared test helpers for PR2-26 tests
// ─────────────────────────────────────────────────────────────────────────────

const pr226ActorID = "00000000-0000-0000-0000-000000000382"

// buildPR226DenialServer builds a Server where the actor is a non-member of
// every org (backed by emptyMembershipDBTX). Requests to org-scoped routes
// should return 403.
func buildPR226DenialServer(t *testing.T) *Server {
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
		t.Fatalf("buildPR226DenialServer: %v", err)
	}
	// emptyMembershipDBTX returns no memberships → actor is non-member.
	memberQ := gen.New(&emptyMembershipDBTX{})
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		MembershipQueries: memberQ,
		OrgQueries:        gen.New(nil),
		EventQueries:      gen.New(nil),
		SessionQueries:    gen.New(nil),
		TierQueries:       gen.New(nil),
		FeedTokenQueries:  gen.New(nil),
		Audit:             &captureAuditWriter{},
	})
}

// buildPR226AdmitServer builds a Server where the actor is admitted as a
// member of any org that appears in the request URL (backed by
// orgMemberAdmitFromCtxDBTX). Requests to org-scoped routes should proceed
// past the membership check and fail at the DB layer (503 from dbDownPool).
func buildPR226AdmitServer(t *testing.T) *Server {
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
		t.Fatalf("buildPR226AdmitServer: %v", err)
	}
	// orgMemberAdmitFromCtxDBTX returns a matching membership → actor admitted.
	memberQ := gen.New(&orgMemberAdmitFromCtxDBTX{})
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		MembershipQueries: memberQ,
		OrgQueries:        gen.New(nil),
		EventQueries:      gen.New(nil),
		SessionQueries:    gen.New(nil),
		TierQueries:       gen.New(nil),
		FeedTokenQueries:  gen.New(nil),
		Audit:             &captureAuditWriter{},
	})
}

func pr226Token(t *testing.T, s *Server) string {
	t.Helper()
	return mintJWT(t, s.stub, pr226ActorID)
}

func doPR226Request(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+pr226Token(t, s))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// Denial tests — new surfaces guarded by PR2-26
// ─────────────────────────────────────────────────────────────────────────────

func TestPR226_Org_GetDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	w := doPR226Request(t, s, http.MethodGet, "/v1/organizations/"+orgID.String(), "")
	assertOrgAccessDenied(t, w)
}

func TestPR226_Org_DeleteDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	w := doPR226Request(t, s, http.MethodDelete, "/v1/organizations/"+orgID.String(), "")
	assertOrgAccessDenied(t, w)
}

func TestPR226_Event_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	body := `{"name":"Hijacked","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`
	w := doPR226Request(t, s, http.MethodPost, "/v1/organizations/"+orgID.String()+"/events", body)
	assertOrgAccessDenied(t, w)
}

func TestPR226_Event_ListByOrgDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	w := doPR226Request(t, s, http.MethodGet, "/v1/organizations/"+orgID.String()+"/events", "")
	assertOrgAccessDenied(t, w)
}

func TestPR226_Event_UpdateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	eventID := uuid.New()
	w := doPR226Request(t, s, http.MethodPatch,
		"/v1/organizations/"+orgID.String()+"/events/"+eventID.String(),
		`{"name":"Hijacked"}`)
	assertOrgAccessDenied(t, w)
}

func TestPR226_Event_DeleteDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	eventID := uuid.New()
	w := doPR226Request(t, s, http.MethodDelete,
		"/v1/organizations/"+orgID.String()+"/events/"+eventID.String(), "")
	assertOrgAccessDenied(t, w)
}

func TestPR226_Session_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	eventID := uuid.New()
	body := `{"start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z","capacity_total":100}`
	w := doPR226Request(t, s, http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/sessions", body)
	assertOrgAccessDenied(t, w)
}

func TestPR226_Session_ListDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	eventID := uuid.New()
	w := doPR226Request(t, s, http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/sessions", "")
	assertOrgAccessDenied(t, w)
}

func TestPR226_FeedToken_CreateDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	chanID := uuid.New()
	w := doPR226Request(t, s, http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/channels/"+chanID.String()+"/feed-tokens",
		`{}`)
	assertOrgAccessDenied(t, w)
}

func TestPR226_FeedToken_ListDeniedForNonMember(t *testing.T) {
	t.Parallel()
	s := buildPR226DenialServer(t)
	orgID := uuid.New()
	chanID := uuid.New()
	w := doPR226Request(t, s, http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/channels/"+chanID.String()+"/feed-tokens", "")
	assertOrgAccessDenied(t, w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Admission tests — actor admitted via orgMemberAdmitFromCtxDBTX
// ─────────────────────────────────────────────────────────────────────────────

// TestPR226_ActiveMember_EventCreate_IsAdmitted verifies that a request by an
// admitted org member is NOT rejected with 403. It will fail at the DB layer
// (503 from dbDownPool) but that proves the membership gate was passed.
func TestPR226_ActiveMember_EventCreate_IsAdmitted(t *testing.T) {
	t.Parallel()
	s := buildPR226AdmitServer(t)
	orgID := uuid.New()
	body := `{"name":"Admitted Event","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`
	w := doPR226Request(t, s, http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/events", body)
	if w.Code == http.StatusForbidden {
		t.Errorf("admitted org member got 403 on event create: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("admitted org member got 401 on event create: %s", w.Body.String())
	}
}

// TestPR226_ActiveMember_OrgGet_IsAdmitted verifies GET /v1/organizations/{id}
// passes the membership check for an admitted actor.
func TestPR226_ActiveMember_OrgGet_IsAdmitted(t *testing.T) {
	t.Parallel()
	s := buildPR226AdmitServer(t)
	orgID := uuid.New()
	w := doPR226Request(t, s, http.MethodGet, "/v1/organizations/"+orgID.String(), "")
	if w.Code == http.StatusForbidden {
		t.Errorf("admitted org member got 403 on org get: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("admitted org member got 401 on org get: %s", w.Body.String())
	}
}

// TestPR226_FailClosed_NilMembershipQueries verifies that when membershipQueries
// is nil (unintentionally un-wired), requests to org-scoped routes are denied
// (fail-closed) rather than silently admitted (fail-open).
func TestPR226_FailClosed_NilMembershipQueries(t *testing.T) {
	t.Parallel()
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
		t.Fatalf("NewStubProvider: %v", err)
	}
	// MembershipQueries intentionally NOT set (nil).
	s := New(Options{
		Config:       cfg,
		Auth:         stub,
		Pool:         &dbDownPool{},
		OrgQueries:   gen.New(nil),
		EventQueries: gen.New(nil),
	})

	orgID := uuid.New()
	w := doPR226Request(t, s, http.MethodGet, "/v1/organizations/"+orgID.String()+"/events", "")
	// With nil membershipQueries the guard must fail-closed → 403.
	if w.Code != http.StatusForbidden {
		t.Errorf("nil membershipQueries: expected 403 (fail-closed), got %d: %s", w.Code, w.Body.String())
	}
}

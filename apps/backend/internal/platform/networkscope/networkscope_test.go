// Package networkscope unit tests — feature #207.
//
// Covers:
//   - positive: actor assigned to network N can address N (and orgs / resources
//     reaching N via the network->org chain);
//   - negative: actor NOT assigned to network N is denied with *OutOfScopeError;
//   - bypass: platform_superadmin and admin always pass without touching the DB;
//   - infra failure: Querier errors propagate as plain errors (not 403);
//   - unauthenticated: no actor on context -> ErrUnauthenticated;
//   - HTTP middleware: 200/401/403/500 wiring through chi URL params.
package networkscope_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/networkscope"
)

// -----------------------------------------------------------------------------
// fakeQuerier — in-memory stand-in for *gen.Queries
// -----------------------------------------------------------------------------

// fakeQuerier records the args of the most recent call and returns canned
// data. Set errOnUser / errOnOrg to simulate DB failures.
type fakeQuerier struct {
	userNetworks map[uuid.UUID][]uuid.UUID
	orgNetworks  map[uuid.UUID][]uuid.UUID

	lastUserID   uuid.UUID
	lastOrgID    uuid.UUID
	lastKind     *string
	userCalls    int
	orgCalls     int
	errOnUser    error
	errOnOrg     error
	kindFilterFn func(*string, []uuid.UUID) []uuid.UUID // optional org-side kind filter
}

func (f *fakeQuerier) ListNetworkIDsByUser(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	f.userCalls++
	f.lastUserID = userID
	if f.errOnUser != nil {
		return nil, f.errOnUser
	}
	out := append([]uuid.UUID(nil), f.userNetworks[userID]...)
	return out, nil
}

func (f *fakeQuerier) ListNetworkIDsByOrganization(_ context.Context, orgID uuid.UUID, kind *string) ([]uuid.UUID, error) {
	f.orgCalls++
	f.lastOrgID = orgID
	f.lastKind = kind
	if f.errOnOrg != nil {
		return nil, f.errOnOrg
	}
	rows := append([]uuid.UUID(nil), f.orgNetworks[orgID]...)
	if f.kindFilterFn != nil {
		rows = f.kindFilterFn(kind, rows)
	}
	return rows, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func mustParse(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("uuid.Parse(%q): %v", s, err)
	}
	return id
}

func ctxWithRoles(userID uuid.UUID, roles ...string) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{
		ID:    userID.String(),
		Type:  auth.ActorTypeUser,
		Roles: roles,
	})
}

func ctxWithAnonActor() context.Context {
	return auth.WithActor(context.Background(), auth.Actor{Type: auth.ActorTypeAnon})
}

// stringPtr returns a pointer to the supplied literal.
func stringPtr(s string) *string { return &s }

// -----------------------------------------------------------------------------
// Constructor / contract
// -----------------------------------------------------------------------------

func TestNewScoper_NilQuerier_Panics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewScoper(nil) did not panic — silently allowing requests is unacceptable")
		}
	}()
	_ = networkscope.NewScoper(nil)
}

func TestIsBypassRole(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"platform_superadmin", true},
		{"admin", true},
		{"platform_operator", false}, // explicitly NOT a bypass — feature #206
		{"network_operator", false},
		{"organizer", false},
		{"", false},
	} {
		if got := networkscope.IsBypassRole(tc.role); got != tc.want {
			t.Errorf("IsBypassRole(%q) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

func TestBypassRoles_ListContents(t *testing.T) {
	t.Parallel()
	// Defensive: the list must contain exactly the two documented bypass
	// roles. Anyone adding "platform_operator" here is breaking #206.
	want := map[string]bool{"platform_superadmin": true, "admin": true}
	if len(networkscope.BypassRoles) != len(want) {
		t.Fatalf("BypassRoles length = %d, want %d (%v)", len(networkscope.BypassRoles), len(want), networkscope.BypassRoles)
	}
	for _, r := range networkscope.BypassRoles {
		if !want[r] {
			t.Errorf("BypassRoles contains unexpected role %q", r)
		}
	}
}

// -----------------------------------------------------------------------------
// LoadAssignedNetworks
// -----------------------------------------------------------------------------

func TestLoadAssignedNetworks_ReturnsUserRows(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	netB := mustParse(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	q := &fakeQuerier{userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA, netB}}}
	s := networkscope.NewScoper(q)

	got, err := s.LoadAssignedNetworks(ctxWithRoles(uid, "network_operator"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != netA || got[1] != netB {
		t.Fatalf("got %v, want [%s %s]", got, netA, netB)
	}
}

func TestLoadAssignedNetworks_BypassReturnsNil(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	q := &fakeQuerier{}
	s := networkscope.NewScoper(q)

	for _, role := range []string{"platform_superadmin", "admin"} {
		got, err := s.LoadAssignedNetworks(ctxWithRoles(uid, role))
		if err != nil {
			t.Errorf("bypass role %q: unexpected error %v", role, err)
		}
		if got != nil {
			t.Errorf("bypass role %q: expected nil slice (= 'all networks'), got %v", role, got)
		}
		if q.userCalls != 0 {
			t.Errorf("bypass role %q: Querier should not have been called, got %d calls", role, q.userCalls)
		}
	}
}

func TestLoadAssignedNetworks_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := networkscope.NewScoper(&fakeQuerier{})

	if _, err := s.LoadAssignedNetworks(context.Background()); !errors.Is(err, networkscope.ErrUnauthenticated) {
		t.Fatalf("no actor: want ErrUnauthenticated, got %v", err)
	}
	if _, err := s.LoadAssignedNetworks(ctxWithAnonActor()); !errors.Is(err, networkscope.ErrUnauthenticated) {
		t.Fatalf("anon actor: want ErrUnauthenticated, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// AssertNetworkInScope
// -----------------------------------------------------------------------------

func TestAssertNetworkInScope_PositiveAndNegative(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	netB := mustParse(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	netC := mustParse(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")

	q := &fakeQuerier{userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA, netB}}}
	s := networkscope.NewScoper(q)

	// Positive — netA in {A,B}.
	if err := s.AssertNetworkInScope(ctxWithRoles(uid, "network_operator"), netA); err != nil {
		t.Fatalf("positive: unexpected error %v", err)
	}

	// Negative — netC not assigned.
	err := s.AssertNetworkInScope(ctxWithRoles(uid, "network_operator"), netC)
	if !errors.Is(err, networkscope.ErrOutOfScope) {
		t.Fatalf("negative: want ErrOutOfScope, got %v", err)
	}
	var scopeErr *networkscope.OutOfScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("negative: want *OutOfScopeError, got %T", err)
	}
	if scopeErr.Resource != "network" || scopeErr.ResourceID != netC.String() {
		t.Errorf("OutOfScopeError fields: got %+v, want {network %s}", scopeErr, netC)
	}
	if scopeErr.HTTPStatus() != http.StatusForbidden {
		t.Errorf("HTTPStatus() = %d, want 403", scopeErr.HTTPStatus())
	}
}

func TestAssertNetworkInScope_BypassSkipsQuerier(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netZ := mustParse(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	q := &fakeQuerier{} // empty — would deny everything if consulted
	s := networkscope.NewScoper(q)

	for _, role := range []string{"platform_superadmin", "admin"} {
		if err := s.AssertNetworkInScope(ctxWithRoles(uid, role), netZ); err != nil {
			t.Errorf("bypass role %q: want nil, got %v", role, err)
		}
	}
	if q.userCalls != 0 {
		t.Errorf("bypass: Querier should not have been called, got %d", q.userCalls)
	}
}

func TestAssertNetworkInScope_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := networkscope.NewScoper(&fakeQuerier{})
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	if err := s.AssertNetworkInScope(context.Background(), netA); !errors.Is(err, networkscope.ErrUnauthenticated) {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
}

func TestAssertNetworkInScope_QuerierError(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	dbErr := errors.New("connection refused")
	q := &fakeQuerier{errOnUser: dbErr}
	s := networkscope.NewScoper(q)

	err := s.AssertNetworkInScope(ctxWithRoles(uid, "network_operator"), netA)
	if err == nil || !errors.Is(err, dbErr) {
		t.Fatalf("want wrapped DB error, got %v", err)
	}
	if errors.Is(err, networkscope.ErrOutOfScope) {
		t.Errorf("DB failure should not present as out-of-scope (403); it must surface as a 5xx")
	}
}

// -----------------------------------------------------------------------------
// AssertOrganizationInScope
// -----------------------------------------------------------------------------

func TestAssertOrganizationInScope_Positive(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	netB := mustParse(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	q := &fakeQuerier{
		userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA}},
		orgNetworks:  map[uuid.UUID][]uuid.UUID{orgA: {netB, netA}}, // overlap on A
	}
	s := networkscope.NewScoper(q)

	if err := s.AssertOrganizationInScope(ctxWithRoles(uid, "network_operator"), orgA, nil); err != nil {
		t.Fatalf("positive any-kind: %v", err)
	}
	if err := s.AssertOrganizationInScope(ctxWithRoles(uid, "network_operator"), orgA, stringPtr("organizer")); err != nil {
		t.Fatalf("positive organizer-kind: %v", err)
	}
	if q.lastKind == nil || *q.lastKind != "organizer" {
		t.Errorf("kind filter not propagated to Querier, got %v", q.lastKind)
	}
}

func TestAssertOrganizationInScope_NegativeNoOverlap(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	netB := mustParse(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	netC := mustParse(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")

	q := &fakeQuerier{
		userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA}},
		orgNetworks:  map[uuid.UUID][]uuid.UUID{orgA: {netB, netC}},
	}
	s := networkscope.NewScoper(q)

	err := s.AssertOrganizationInScope(ctxWithRoles(uid, "network_operator"), orgA, nil)
	if !errors.Is(err, networkscope.ErrOutOfScope) {
		t.Fatalf("want ErrOutOfScope, got %v", err)
	}
	var scopeErr *networkscope.OutOfScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Resource != "organization" {
		t.Errorf("OutOfScopeError fields: got %+v, want resource=organization", scopeErr)
	}
}

func TestAssertOrganizationInScope_NegativeUserHasNoAssignments(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	// User has no network assignments at all -> must deny WITHOUT bothering
	// to consult the orgNetworks side.
	q := &fakeQuerier{}
	s := networkscope.NewScoper(q)

	if err := s.AssertOrganizationInScope(ctxWithRoles(uid, "network_operator"), orgA, nil); !errors.Is(err, networkscope.ErrOutOfScope) {
		t.Fatalf("want ErrOutOfScope, got %v", err)
	}
	if q.orgCalls != 0 {
		t.Errorf("orgCalls = %d, want 0 (org lookup must be skipped when user has no assignments)", q.orgCalls)
	}
}

func TestAssertOrganizationInScope_BypassSkipsQuerier(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	q := &fakeQuerier{}
	s := networkscope.NewScoper(q)

	if err := s.AssertOrganizationInScope(ctxWithRoles(uid, "platform_superadmin"), orgA, nil); err != nil {
		t.Errorf("superadmin: want nil, got %v", err)
	}
	if q.userCalls+q.orgCalls != 0 {
		t.Errorf("bypass: Querier should not have been called, got user=%d org=%d", q.userCalls, q.orgCalls)
	}
}

// -----------------------------------------------------------------------------
// AssertResourceInScope — re-labels OutOfScopeError to the caller's resource.
// -----------------------------------------------------------------------------

func TestAssertResourceInScope_RelabelsOnDeny(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	q := &fakeQuerier{
		userNetworks: map[uuid.UUID][]uuid.UUID{uid: {mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}},
		orgNetworks:  map[uuid.UUID][]uuid.UUID{orgA: {mustParse(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")}},
	}
	s := networkscope.NewScoper(q)

	err := s.AssertResourceInScope(ctxWithRoles(uid, "network_operator"), "order", "order-123", orgA)
	var scopeErr *networkscope.OutOfScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("want *OutOfScopeError, got %v", err)
	}
	if scopeErr.Resource != "order" || scopeErr.ResourceID != "order-123" {
		t.Errorf("relabel failed: got %+v, want {order order-123}", scopeErr)
	}
}

func TestAssertResourceInScope_AllowsPositive(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	q := &fakeQuerier{
		userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA}},
		orgNetworks:  map[uuid.UUID][]uuid.UUID{orgA: {netA}},
	}
	s := networkscope.NewScoper(q)

	if err := s.AssertResourceInScope(ctxWithRoles(uid, "network_operator"), "ticket", "T-1", orgA); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// IsBypass
// -----------------------------------------------------------------------------

func TestIsBypass(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	s := networkscope.NewScoper(&fakeQuerier{})

	if !s.IsBypass(ctxWithRoles(uid, "platform_superadmin")) {
		t.Error("superadmin should be bypass")
	}
	if !s.IsBypass(ctxWithRoles(uid, "admin")) {
		t.Error("admin should be bypass")
	}
	if s.IsBypass(ctxWithRoles(uid, "network_operator")) {
		t.Error("network_operator must NOT be bypass")
	}
	if s.IsBypass(ctxWithRoles(uid, "platform_operator")) {
		t.Error("platform_operator must NOT be bypass (feature #206 invariant)")
	}
	if s.IsBypass(context.Background()) {
		t.Error("no actor must not bypass")
	}
}

// -----------------------------------------------------------------------------
// HTTP middleware
// -----------------------------------------------------------------------------

func decodeErrorEnvelope(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var v struct {
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if v.Error == nil {
		t.Fatal("envelope missing 'error' key")
	}
	return v.Error
}

func newRequestWithActor(method, path string, actor *auth.Actor) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if actor != nil {
		r = r.WithContext(auth.WithActor(r.Context(), *actor))
	}
	return r
}

func TestRequireNetworkScope_AllowsInScope(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	q := &fakeQuerier{userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA}}}
	s := networkscope.NewScoper(q)

	r := chi.NewRouter()
	r.With(networkscope.RequireNetworkScope(s, "network_id")).
		Get("/v1/networks/{network_id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newRequestWithActor(http.MethodGet, "/v1/networks/"+netA.String(),
		&auth.Actor{ID: uid.String(), Type: auth.ActorTypeUser, Roles: []string{"network_operator"}}))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s), want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("body = %q, want 'ok'", w.Body.String())
	}
}

func TestRequireNetworkScope_DeniesOutOfScope(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	netZ := mustParse(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	q := &fakeQuerier{userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA}}}
	s := networkscope.NewScoper(q)

	r := chi.NewRouter()
	r.With(networkscope.RequireNetworkScope(s, "network_id")).
		Get("/v1/networks/{network_id}", func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("handler must not run on out-of-scope")
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newRequestWithActor(http.MethodGet, "/v1/networks/"+netZ.String(),
		&auth.Actor{ID: uid.String(), Type: auth.ActorTypeUser, Roles: []string{"network_operator"}}))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	env := decodeErrorEnvelope(t, w.Body)
	if env["code"] != "networkscope.out_of_scope" {
		t.Errorf("code = %v, want networkscope.out_of_scope", env["code"])
	}
	if env["resource"] != "network" || env["resource_id"] != netZ.String() {
		t.Errorf("envelope resource fields = %v / %v", env["resource"], env["resource_id"])
	}
}

func TestRequireNetworkScope_Unauthenticated_Returns401(t *testing.T) {
	t.Parallel()
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	s := networkscope.NewScoper(&fakeQuerier{})

	r := chi.NewRouter()
	r.With(networkscope.RequireNetworkScope(s, "network_id")).
		Get("/v1/networks/{network_id}", func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("handler must not run when unauthenticated")
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/networks/"+netA.String(), nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	env := decodeErrorEnvelope(t, w.Body)
	if env["code"] != "networkscope.unauthenticated" {
		t.Errorf("code = %v, want networkscope.unauthenticated", env["code"])
	}
}

func TestRequireNetworkScope_BypassRoleAllows(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netZ := mustParse(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	q := &fakeQuerier{} // no assignments
	s := networkscope.NewScoper(q)

	r := chi.NewRouter()
	r.With(networkscope.RequireNetworkScope(s, "network_id")).
		Get("/v1/networks/{network_id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newRequestWithActor(http.MethodGet, "/v1/networks/"+netZ.String(),
		&auth.Actor{ID: uid.String(), Type: auth.ActorTypeUser, Roles: []string{"platform_superadmin"}}))

	if w.Code != http.StatusOK {
		t.Fatalf("superadmin status = %d, want 200", w.Code)
	}
	if q.userCalls != 0 {
		t.Errorf("Querier called %d times for bypass actor, want 0", q.userCalls)
	}
}

func TestRequireNetworkScope_InvalidUUID_Returns400(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	s := networkscope.NewScoper(&fakeQuerier{})

	r := chi.NewRouter()
	r.With(networkscope.RequireNetworkScope(s, "network_id")).
		Get("/v1/networks/{network_id}", func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("handler must not run on malformed UUID")
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newRequestWithActor(http.MethodGet, "/v1/networks/not-a-uuid",
		&auth.Actor{ID: uid.String(), Type: auth.ActorTypeUser, Roles: []string{"network_operator"}}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestRequireNetworkScope_QuerierError_Returns500(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	q := &fakeQuerier{errOnUser: errors.New("connection refused")}
	s := networkscope.NewScoper(q)

	r := chi.NewRouter()
	r.With(networkscope.RequireNetworkScope(s, "network_id")).
		Get("/v1/networks/{network_id}", func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("handler must not run on infra failure")
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newRequestWithActor(http.MethodGet, "/v1/networks/"+netA.String(),
		&auth.Actor{ID: uid.String(), Type: auth.ActorTypeUser, Roles: []string{"network_operator"}}))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	env := decodeErrorEnvelope(t, w.Body)
	if env["code"] != "networkscope.internal_error" {
		t.Errorf("code = %v, want networkscope.internal_error", env["code"])
	}
}

func TestRequireOrganizationScope_DeniesOutOfScope(t *testing.T) {
	t.Parallel()
	uid := mustParse(t, "11111111-1111-1111-1111-111111111111")
	orgA := mustParse(t, "00000000-0000-0000-0000-0000000000aa")
	netA := mustParse(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	netZ := mustParse(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	q := &fakeQuerier{
		userNetworks: map[uuid.UUID][]uuid.UUID{uid: {netA}},
		orgNetworks:  map[uuid.UUID][]uuid.UUID{orgA: {netZ}},
	}
	s := networkscope.NewScoper(q)

	r := chi.NewRouter()
	r.With(networkscope.RequireOrganizationScope(s, "org_id", nil)).
		Get("/v1/orgs/{org_id}", func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("handler must not run")
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newRequestWithActor(http.MethodGet, "/v1/orgs/"+orgA.String(),
		&auth.Actor{ID: uid.String(), Type: auth.ActorTypeUser, Roles: []string{"network_operator"}}))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	env := decodeErrorEnvelope(t, w.Body)
	if env["resource"] != "organization" {
		t.Errorf("resource = %v, want organization", env["resource"])
	}
}

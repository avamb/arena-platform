package hiam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/orgperm"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

type fakeOrgMeQuerier struct {
	memberships []gen.MembershipRow
	orgs        map[uuid.UUID]gen.OrganizationRow
	err         error
}

func (f fakeOrgMeQuerier) ListMembershipsByUser(context.Context, uuid.UUID) ([]gen.MembershipRow, error) {
	return f.memberships, f.err
}

func (f fakeOrgMeQuerier) GetOrganizationByID(_ context.Context, id uuid.UUID) (gen.OrganizationRow, error) {
	org, ok := f.orgs[id]
	if !ok {
		return gen.OrganizationRow{}, pgx.ErrNoRows
	}
	return org, nil
}

// fakeResolver answers per organization, like the real checker.
type fakeResolver map[uuid.UUID]permissions.Effective

func (f fakeResolver) EffectiveInOrg(_ context.Context, actor auth.Actor, org uuid.UUID) (permissions.Effective, error) {
	if actor.IsService() {
		return permissions.Effective{Roles: []string{}, Permissions: actor.Permissions}, nil
	}
	if eff, ok := f[org]; ok {
		return eff, nil
	}
	return permissions.Effective{Roles: []string{}, Permissions: []string{}}, nil
}

type orgMeFixture struct {
	user, orgA, orgB, orgC uuid.UUID
	q                      fakeOrgMeQuerier
	res                    fakeResolver
}

// newOrgMeFixture: a user who is an agent of A and a manager (organizer) of
// B; C exists but the user is not a member of it.
func newOrgMeFixture() orgMeFixture {
	f := orgMeFixture{user: uuid.New(), orgA: uuid.New(), orgB: uuid.New(), orgC: uuid.New()}
	f.q = fakeOrgMeQuerier{
		memberships: []gen.MembershipRow{
			{OrgID: f.orgA, Role: "agent", Status: "active"},
			{OrgID: f.orgB, Role: "organizer", Status: "active"},
		},
		orgs: map[uuid.UUID]gen.OrganizationRow{
			f.orgA: {ID: f.orgA, Name: "Org A", DisplayNumber: 11},
			f.orgB: {ID: f.orgB, Name: "Org B", DisplayNumber: 12},
			f.orgC: {ID: f.orgC, Name: "Org C", DisplayNumber: 13},
		},
	}
	f.res = fakeResolver{
		f.orgA: {Roles: []string{"agent"}, Permissions: []string{"ticket.read"}},
		f.orgB: {Roles: []string{"organizer"}, Permissions: []string{"event.read", "order.read"}},
		f.orgC: {Roles: []string{"platform_superadmin"}, Permissions: []string{"event.read", "superadmin.read"}},
	}
	return f
}

// memberOf is the test stand-in for MembershipCheck. It decides with the
// shared rule of every guard, orgperm.HasMembershipRow.
func memberOf(q OrgMeQuerier) MemberCheck {
	if q == nil {
		return nil
	}
	return func(ctx context.Context, org uuid.UUID) (bool, error) {
		rows, err := q.ListMembershipsByUser(ctx, uuid.Nil)
		if err != nil {
			return false, err
		}
		return orgperm.HasMembershipRow(rows, org), nil
	}
}

func callOrgMe(t *testing.T, q OrgMeQuerier, res permissions.OrgEffectiveResolver, ctx context.Context, org string) (int, orgMeResponse, string) {
	t.Helper()
	r := chi.NewRouter()
	var check MemberCheck
	if q != nil {
		check = memberOf(q)
	}
	r.Get("/organizations/{org_id}/me", HandleOrgMe(q, check, res, nil))
	req := httptest.NewRequest(http.MethodGet, "/organizations/"+org+"/me", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body orgMeResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, body, env.Error.Code
}

func userCtx(id uuid.UUID) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{ID: id.String(), Type: auth.ActorTypeUser})
}

func TestOrgMe_AgentInOneOrganizationManagerInAnother(t *testing.T) {
	f := newOrgMeFixture()
	code, a, _ := callOrgMe(t, f.q, f.res, userCtx(f.user), f.orgA.String())
	if code != http.StatusOK {
		t.Fatalf("A: status %d", code)
	}
	if a.Role != "agent" || a.CallerType != OrgMeCallerUser || a.OrgName != "Org A" || a.OrgDisplayNumber != 11 {
		t.Errorf("A = %+v", a)
	}
	if !reflect.DeepEqual(a.Permissions, []string{"ticket.read"}) || !reflect.DeepEqual(a.MembershipRoles, []string{"agent"}) {
		t.Errorf("A lists %v / %v; want only the agent's", a.MembershipRoles, a.Permissions)
	}
	code, b, _ := callOrgMe(t, f.q, f.res, userCtx(f.user), f.orgB.String())
	if code != http.StatusOK || b.Role != "organizer" || !reflect.DeepEqual(b.Permissions, []string{"event.read", "order.read"}) {
		t.Errorf("B: %d %+v", code, b)
	}
	if b.PlatformSuperadmin {
		t.Error("an ordinary member is reported as superadmin")
	}
}

func TestOrgMe_NonMemberIsRefused(t *testing.T) {
	f := newOrgMeFixture()
	code, _, errCode := callOrgMe(t, f.q, f.res, userCtx(f.user), f.orgC.String())
	if code != http.StatusForbidden || errCode != "org.access_denied" {
		t.Fatalf("C: %d %s; want 403 org.access_denied", code, errCode)
	}
	// An inactive membership row does not count either.
	f.q.memberships = append(f.q.memberships, gen.MembershipRow{OrgID: f.orgC, Role: "org_admin", Status: "suspended"})
	if code, _, _ := callOrgMe(t, f.q, f.res, userCtx(f.user), f.orgC.String()); code != http.StatusForbidden {
		t.Fatalf("suspended membership: %d; want 403", code)
	}
}

func TestOrgMe_SuperadminNeedsNoMembershipAndNoReason(t *testing.T) {
	f := newOrgMeFixture()
	ctx := auth.WithSuperadminOrgAccess(userCtx(f.user))
	code, c, _ := callOrgMe(t, f.q, f.res, ctx, f.orgC.String())
	if code != http.StatusOK || !c.PlatformSuperadmin || c.Role != "" || len(c.MembershipRoles) != 0 {
		t.Fatalf("superadmin in C: %d %+v", code, c)
	}
	if !reflect.DeepEqual(c.Roles, []string{"platform_superadmin"}) {
		t.Errorf("roles = %v", c.Roles)
	}
}

func TestOrgMe_APIKeyOnlyInItsOwnOrganization(t *testing.T) {
	f := newOrgMeFixture()
	key := auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService, OrgID: f.orgB.String(), Permissions: []string{"order.read"}}
	ctx := auth.WithActor(context.Background(), key)
	code, b, _ := callOrgMe(t, f.q, f.res, ctx, f.orgB.String())
	if code != http.StatusOK || b.CallerType != OrgMeCallerAPIKey || b.Role != "" || !reflect.DeepEqual(b.Permissions, []string{"order.read"}) {
		t.Fatalf("key in its org: %d %+v", code, b)
	}
	if len(b.Roles) != 0 || b.Roles == nil || b.MembershipRoles == nil {
		t.Errorf("key lists must be empty, not null: %+v", b)
	}
	if code, _, errCode := callOrgMe(t, f.q, f.res, ctx, f.orgA.String()); code != http.StatusForbidden || errCode != "org.access_denied" {
		t.Fatalf("key in another org: %d %s", code, errCode)
	}
}

func TestOrgMe_EdgeCases(t *testing.T) {
	f := newOrgMeFixture()
	if code, _, _ := callOrgMe(t, f.q, f.res, userCtx(f.user), "not-a-uuid"); code != http.StatusBadRequest {
		t.Errorf("bad uuid: %d; want 400", code)
	}
	if code, _, _ := callOrgMe(t, f.q, f.res, context.Background(), f.orgA.String()); code != http.StatusUnauthorized {
		t.Errorf("no actor: %d; want 401", code)
	}
	if code, _, errCode := callOrgMe(t, nil, nil, userCtx(f.user), f.orgA.String()); code != http.StatusServiceUnavailable {
		t.Errorf("no wiring: %d %s; want 503", code, errCode)
	}
	deleted := f.q
	deleted.orgs = map[uuid.UUID]gen.OrganizationRow{}
	if code, _, errCode := callOrgMe(t, deleted, f.res, userCtx(f.user), f.orgA.String()); code != http.StatusNotFound || errCode != "org.not_found" {
		t.Errorf("deleted org: %d %s; want 404 org.not_found", code, errCode)
	}
	broken := f.q
	broken.err = errors.New("db down")
	if code, _, _ := callOrgMe(t, broken, f.res, userCtx(f.user), f.orgA.String()); code != http.StatusInternalServerError {
		t.Errorf("db error: %d; want 500", code)
	}
}

// A platform role in a membership row grants nothing (the checker reads
// platform roles from user_roles only), so it is neither a membership nor
// listed in membership_roles.
func TestOrgMe_PlatformRoleRowIsNotAMembership(t *testing.T) {
	f := newOrgMeFixture()
	f.q.memberships = append(f.q.memberships, gen.MembershipRow{OrgID: f.orgC, Role: "platform_superadmin", Status: "active"})
	if code, _, errCode := callOrgMe(t, f.q, f.res, userCtx(f.user), f.orgC.String()); code != http.StatusForbidden || errCode != "org.access_denied" {
		t.Fatalf("platform row only: %d %s; want 403", code, errCode)
	}
	f.q.memberships = append(f.q.memberships, gen.MembershipRow{OrgID: f.orgA, Role: "platform_operator", Status: "active"})
	_, a, _ := callOrgMe(t, f.q, f.res, userCtx(f.user), f.orgA.String())
	if !reflect.DeepEqual(a.MembershipRoles, []string{"agent"}) {
		t.Errorf("membership_roles = %v; want the platform role left out", a.MembershipRoles)
	}
}

func TestMainMembershipRole(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"agent"}, "agent"},
		{[]string{"agent", "organizer"}, "organizer"},
		{[]string{"organizer", "org_admin", "agent"}, "org_admin"},
		{[]string{"network_operator", "external_ticketing_operator"}, "external_ticketing_operator"},
	}
	for _, c := range cases {
		if got := MainMembershipRole(c.in); got != c.want {
			t.Errorf("MainMembershipRole(%v) = %q; want %q", c.in, got, c.want)
		}
	}
}

//go:build integration

// memberships_cross_org_integration_test.go — SEC-1 (org-isolation audit
// 2026-10-10, hole H1): the membership routes must stay inside the caller's
// authority.
//
//	POST/GET /v1/organizations/{org_id}/members
//	DELETE   /v1/organizations/{org_id}/members/{user_id}
//	GET/POST /v1/admin/organizations/{org_id}/members
//	PATCH/DELETE /v1/admin/organizations/{org_id}/members/{membership_id}
//
// Before the fix these checked only that the caller held
// membership.read/grant/revoke SOMEWHERE (the permission checker unions the
// roles of every membership), never that the caller belonged to the PATH
// organization, and accepted every role of memberships_role_check from any
// caller. The owner of any organization could grant themselves
// platform_superadmin and became the platform superadmin on the next request.
//
// Every request goes through the REAL router with a JWT whose only authority
// is a membership row (an empty roles claim, exactly what /v1/auth/login and
// the Telegram bot mint), or with an organization API key.
//
// Run against a scratch database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -count=1 -v \
//	    ./apps/backend/internal/platform/httpserver/ -run TestSEC1_
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type sec1Env struct {
	srv    *Server
	ts     *httptest.Server
	q      *gen.Queries
	secret string
	orgs   []uuid.UUID
	users  []uuid.UUID
}

func newSec1Env(t *testing.T) *sec1Env {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	e := &sec1Env{srv: srv, ts: ts, q: gen.New(srv.pgxPool), secret: secret}
	t.Cleanup(func() {
		ts.Close()
		ctx := context.Background()
		for _, c := range []struct {
			stmt string
			arg  []uuid.UUID
		}{
			{`DELETE FROM memberships WHERE user_id = ANY($1)`, e.users},
			{`DELETE FROM memberships WHERE org_id = ANY($1)`, e.orgs},
			{`DELETE FROM user_roles WHERE user_id = ANY($1)`, e.users},
			{`DELETE FROM api_keys WHERE org_id = ANY($1)`, e.orgs},
			{`DELETE FROM organizations WHERE id = ANY($1)`, e.orgs},
			{`DELETE FROM users WHERE id = ANY($1)`, e.users},
		} {
			if _, err := srv.pgxPool.Exec(ctx, c.stmt, c.arg); err != nil {
				t.Logf("cleanup %q: %v", c.stmt, err)
			}
		}
	})
	return e
}

func (e *sec1Env) org(t *testing.T, label string) uuid.UUID {
	t.Helper()
	suffix := uuid.NewString()[:8]
	o, err := e.q.InsertOrganization(context.Background(), "SEC1 "+label+" "+suffix,
		"sec1-"+strings.ToLower(label)+"-"+suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	e.orgs = append(e.orgs, o.ID)
	return o.ID
}

func (e *sec1Env) user(t *testing.T, label string) uuid.UUID {
	t.Helper()
	u, err := e.q.InsertUser(context.Background(), "sec1-"+label+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	e.users = append(e.users, u.ID)
	return u.ID
}

// member inserts an active membership straight into the table, the way a
// past grant (or a past exploit) left it, and returns its id.
func (e *sec1Env) member(t *testing.T, user, org uuid.UUID, role string) uuid.UUID {
	t.Helper()
	m, err := e.q.InsertMembership(context.Background(), user, org, role)
	if err != nil {
		t.Fatalf("InsertMembership(%s): %v", role, err)
	}
	return m.ID
}

// token mints a JWT whose only authority is the user's database rows.
func (e *sec1Env) token(t *testing.T, user uuid.UUID) string {
	t.Helper()
	tok, _, err := auth.IssueJWT(e.secret, user, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

// superadmin is the operator's real shape: a NULL-org_id user_roles row of
// platform_superadmin and no membership anywhere.
func (e *sec1Env) superadmin(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	id := e.user(t, "super")
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`INSERT INTO user_roles (user_id, role_id, org_id)
		 SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, id); err != nil {
		t.Fatalf("grant global platform_superadmin: %v", err)
	}
	return id, e.token(t, id)
}

func (e *sec1Env) key(t *testing.T, org uuid.UUID, scopes ...string) string {
	t.Helper()
	creator := e.user(t, "keyowner")
	_, raw, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(e.q), apikeys.IssueInput{
		OrgID: org, Name: "sec1-" + uuid.NewString()[:6], Scopes: scopes, CreatedBy: creator,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue(%v): %v", scopes, err)
	}
	return raw
}

// do sends one request; reason "" omits X-Admin-Reason.
func (e *sec1Env) do(t *testing.T, method, path, bearer, reason, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode, integReadBody(t, resp)
}

func (e *sec1Env) hasActive(t *testing.T, user, org uuid.UUID, role string) bool {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2 AND role = $3 AND status = 'active'`,
		user, org, role).Scan(&n); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	return n > 0
}

func sec1Code(body string) string {
	var out map[string]any
	if json.Unmarshal([]byte(body), &out) != nil {
		return ""
	}
	return prom0113Code(out)
}

// expect fails unless the request answered want.
func (e *sec1Env) expect(t *testing.T, want int, method, path, bearer, reason, body string) string {
	t.Helper()
	st, out := e.do(t, method, path, bearer, reason, body)
	if st != want {
		t.Errorf("%s %s: got %d %s, want %d", method, path, st, out, want)
	}
	return out
}

func grantBody(user uuid.UUID, role string) string {
	return `{"user_id":"` + user.String() + `","role":"` + role + `"}`
}

var sec1PlatformRoles = []string{"platform_superadmin", "platform_operator", "network_operator", "external_ticketing_operator"}

// (a) The escalation: the owner of org B grants themselves a platform role in
// B or in a foreign org A, then calls a superadmin-only route.
func TestSEC1_OwnerCannotGrantPlatformRoles(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	tok := e.token(t, owner)

	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/admin/orders", tok, "baseline", "")

	for _, org := range []uuid.UUID{orgB, orgA} {
		for _, role := range sec1PlatformRoles {
			e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/organizations/"+org.String()+"/members", tok, "", grantBody(owner, role))
			e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/admin/organizations/"+org.String()+"/members", tok, "sec1", grantBody(owner, role))
		}
	}
	for _, org := range []uuid.UUID{orgB, orgA} {
		for _, role := range sec1PlatformRoles {
			if e.hasActive(t, owner, org, role) {
				t.Errorf("owner now holds %s in %s", role, org)
			}
		}
	}

	// The proof of escalation: a superadmin-only route and the cross-tenant
	// bypass on a foreign organization.
	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/admin/orders", tok, "sec1 escalation probe", "")
	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+orgA.String()+"/channels", tok, "sec1 escalation probe", "")
}

// (b) + (d) The owner of B reads, grants and revokes in a foreign org A.
func TestSEC1_OwnerCannotManageForeignOrgMembers(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	tok := e.token(t, owner)
	victim, victimMgr := e.user(t, "victim"), e.user(t, "victimmgr")
	victimMID := e.member(t, victim, orgA, "org_admin")
	mgrMID := e.member(t, victimMgr, orgA, "organizer")

	a := "/v1/organizations/" + orgA.String() + "/members"
	adminA := "/v1/admin/organizations/" + orgA.String() + "/members"
	adminB := "/v1/admin/organizations/" + orgB.String() + "/members"

	e.expect(t, http.StatusForbidden, http.MethodGet, a, tok, "", "")
	if out := e.expect(t, http.StatusForbidden, http.MethodGet, adminA, tok, "sec1", ""); strings.Contains(out, "sec1-victim") {
		t.Errorf("foreign member e-mails leaked: %s", out)
	}
	e.expect(t, http.StatusForbidden, http.MethodPost, a, tok, "", grantBody(owner, "org_admin"))
	e.expect(t, http.StatusForbidden, http.MethodPost, adminA, tok, "sec1", grantBody(owner, "org_admin"))
	e.expect(t, http.StatusForbidden, http.MethodDelete, a+"/"+victim.String(), tok, "", `{"role":"org_admin"}`)
	e.expect(t, http.StatusForbidden, http.MethodPatch, adminA+"/"+mgrMID.String(), tok, "sec1", `{"role":"org_admin"}`)
	e.expect(t, http.StatusForbidden, http.MethodDelete, adminA+"/"+victimMID.String(), tok, "sec1", "")

	// (d) A's membership id under the owner's own org path is not found.
	e.expect(t, http.StatusNotFound, http.MethodPatch, adminB+"/"+mgrMID.String(), tok, "sec1", `{"role":"agent"}`)
	e.expect(t, http.StatusNotFound, http.MethodDelete, adminB+"/"+victimMID.String(), tok, "sec1", "")

	if !e.hasActive(t, victim, orgA, "org_admin") || !e.hasActive(t, victimMgr, orgA, "organizer") {
		t.Error("a foreign owner changed org A's memberships")
	}
	if e.hasActive(t, owner, orgA, "org_admin") {
		t.Error("the foreign owner made themselves an owner of org A")
	}
}

// Authority is per organization: an agent of A who owns B still cannot grant
// or revoke in A.
func TestSEC1_AuthorityIsCheckedInThePathOrganization(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	caller := e.user(t, "agentowner")
	e.member(t, caller, orgA, "agent")
	e.member(t, caller, orgB, "org_admin")
	tok := e.token(t, caller)
	other := e.user(t, "other")
	e.member(t, other, orgA, "organizer")

	a := "/v1/organizations/" + orgA.String() + "/members"
	e.expect(t, http.StatusForbidden, http.MethodPost, a, tok, "", grantBody(caller, "org_admin"))
	e.expect(t, http.StatusForbidden, http.MethodDelete, a+"/"+other.String(), tok, "", `{"role":"organizer"}`)
	if e.hasActive(t, caller, orgA, "org_admin") || !e.hasActive(t, other, orgA, "organizer") {
		t.Error("an agent of A changed A's memberships with authority held in B")
	}
	// Reading the member list is a member's right (membership.read, 0011).
	e.expect(t, http.StatusOK, http.MethodGet, a, tok, "", "")
}

// An organization API key with membership scopes stays in its organization
// and cannot mint platform roles either.
func TestSEC1_APIKeyCannotEscalate(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	key := e.key(t, orgB, "membership.read", "membership.grant", "membership.revoke")
	target := e.user(t, "target")

	e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/organizations/"+orgB.String()+"/members", key, "", grantBody(target, "platform_superadmin"))
	e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/organizations/"+orgA.String()+"/members", key, "", grantBody(target, "organizer"))
	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+orgA.String()+"/members", key, "", "")
	if e.hasActive(t, target, orgB, "platform_superadmin") || e.hasActive(t, target, orgA, "organizer") {
		t.Error("an API key granted outside its authority")
	}
	// Its own organization, an organization role: allowed.
	e.expect(t, http.StatusCreated, http.MethodPost, "/v1/organizations/"+orgB.String()+"/members", key, "", grantBody(target, "organizer"))
}

// The owner's legitimate team flows (admin console members tab, the bot's
// "Team" screen that revokes with the role in the body) keep working.
func TestSEC1_OwnerKeepsOwnTeamFlows(t *testing.T) {
	e := newSec1Env(t)
	orgB := e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	tok := e.token(t, owner)
	mate := e.user(t, "mate")
	b := "/v1/organizations/" + orgB.String() + "/members"
	adminB := "/v1/admin/organizations/" + orgB.String() + "/members"

	e.expect(t, http.StatusOK, http.MethodGet, b, tok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, adminB, tok, "sec1", "")
	e.expect(t, http.StatusCreated, http.MethodPost, b, tok, "", grantBody(mate, "organizer"))
	e.expect(t, http.StatusCreated, http.MethodPost, b, tok, "", grantBody(mate, "agent"))
	e.expect(t, http.StatusOK, http.MethodDelete, b+"/"+mate.String(), tok, "", `{"role":"agent"}`)
	out := e.expect(t, http.StatusCreated, http.MethodPost, adminB, tok, "sec1", grantBody(mate, "org_admin"))
	var created struct {
		Membership struct {
			ID string `json:"id"`
		} `json:"membership"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	e.expect(t, http.StatusForbidden, http.MethodPatch, adminB+"/"+created.Membership.ID, tok, "sec1", `{"role":"platform_superadmin"}`)
	e.expect(t, http.StatusOK, http.MethodPatch, adminB+"/"+created.Membership.ID, tok, "sec1", `{"role":"agent"}`)
	e.expect(t, http.StatusOK, http.MethodDelete, adminB+"/"+created.Membership.ID, tok, "sec1", "")

	// A platform-level row in the owner's organization was put there by the
	// platform; the owner may not remove it.
	op := e.user(t, "netop")
	opMID := e.member(t, op, orgB, "network_operator")
	e.expect(t, http.StatusForbidden, http.MethodDelete, b+"/"+op.String(), tok, "", `{"role":"network_operator"}`)
	e.expect(t, http.StatusForbidden, http.MethodDelete, adminB+"/"+opMID.String(), tok, "sec1", "")
	if !e.hasActive(t, op, orgB, "network_operator") {
		t.Error("the owner removed a platform-granted membership")
	}

	// A manager has membership.read only: reading is fine, granting is not.
	mgr := e.user(t, "mgr")
	e.member(t, mgr, orgB, "organizer")
	mtok := e.token(t, mgr)
	e.expect(t, http.StatusOK, http.MethodGet, b, mtok, "", "")
	e.expect(t, http.StatusForbidden, http.MethodPost, b, mtok, "", grantBody(mgr, "org_admin"))
}

// The operator's real superadmin (global user_roles, no membership) keeps
// full access to every organization's members with X-Admin-Reason.
func TestSEC1_SuperadminKeepsFullMembershipAccess(t *testing.T) {
	e := newSec1Env(t)
	orgA := e.org(t, "A")
	_, tok := e.superadmin(t)
	u := e.user(t, "u")
	a := "/v1/organizations/" + orgA.String() + "/members"
	adminA := "/v1/admin/organizations/" + orgA.String() + "/members"

	e.expect(t, http.StatusOK, http.MethodGet, a, tok, "sec1", "")
	e.expect(t, http.StatusOK, http.MethodGet, adminA, tok, "sec1", "")
	e.expect(t, http.StatusBadRequest, http.MethodGet, a, tok, "", "")
	e.expect(t, http.StatusCreated, http.MethodPost, a, tok, "sec1", grantBody(u, "org_admin"))
	e.expect(t, http.StatusCreated, http.MethodPost, a, tok, "sec1", grantBody(u, "network_operator"))
	out := e.expect(t, http.StatusCreated, http.MethodPost, adminA, tok, "sec1", grantBody(u, "external_ticketing_operator"))
	var created struct {
		Membership struct {
			ID string `json:"id"`
		} `json:"membership"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	e.expect(t, http.StatusOK, http.MethodPatch, adminA+"/"+created.Membership.ID, tok, "sec1", `{"role":"agent"}`)
	e.expect(t, http.StatusOK, http.MethodDelete, adminA+"/"+created.Membership.ID, tok, "sec1", "")
	e.expect(t, http.StatusOK, http.MethodDelete, a+"/"+u.String(), tok, "sec1", `{"role":"network_operator"}`)
	e.expect(t, http.StatusOK, http.MethodGet, "/v1/admin/orders", tok, "sec1", "")

	// A platform role is global (user_roles), never a membership: even the
	// superadmin gets a clear answer instead of a row that grants nothing.
	if st, out := e.do(t, http.MethodPost, a, tok, "sec1", grantBody(u, "platform_superadmin")); st != http.StatusUnprocessableEntity || sec1Code(out) != "membership.platform_role_is_global" {
		t.Errorf("superadmin platform_superadmin membership: %d %s", st, out)
	}
}

// Defence in depth: a platform_superadmin MEMBERSHIP row (what the exploit
// left behind) grants nothing — no superadmin permission, no cross-tenant
// bypass. The operator's superadmin is a global user_roles row.
func TestSEC1_PlatformRoleMembershipGrantsNothing(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	esc := e.user(t, "escalated")
	e.member(t, esc, orgB, "org_admin")
	e.member(t, esc, orgB, "platform_superadmin")
	tok := e.token(t, esc)

	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/admin/orders", tok, "sec1", "")
	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+orgA.String()+"/members", tok, "sec1", "")
	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+orgA.String()+"/channels", tok, "sec1", "")
	// Their own organization still works as an owner.
	e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+orgB.String()+"/members", tok, "", "")
}

//go:build integration

// perm_org_rows_integration_test.go — PERM-ORG: a route whose `{id}` names
// the organization itself decides the permission inside THAT organization.
//
// The caller in every case holds a membership in organization A that carries
// no permission at all (agent) and the owner role of organization B. Owning B
// lets them past every permission gate that decides from the union of their
// memberships; being a member of A lets them past every membership guard. Only
// the per-organization decision stops them.
package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type permOrgRows struct {
	e      *sweepEnv
	orgA   uuid.UUID
	w      *sweepWorld
	mixed  string // agent of A, owner of B
	ownerA string // owner of A
}

func newPermOrgRows(t *testing.T) *permOrgRows {
	t.Helper()
	e := newSweepEnv(t)
	p := &permOrgRows{e: e, orgA: e.newOrg(t, "A")}
	p.w = e.newWorld(t, p.orgA, e.orgC)
	p.mixed = p.userToken(t, map[uuid.UUID]string{p.orgA: "agent", e.orgB: "org_admin"})
	p.ownerA = p.userToken(t, map[uuid.UUID]string{p.orgA: "org_admin"})
	return p
}

func (p *permOrgRows) userToken(t *testing.T, roles map[uuid.UUID]string) string {
	t.Helper()
	ctx := context.Background()
	u, err := p.e.q.InsertUser(ctx, "permorg-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	for org, role := range roles {
		if _, err := p.e.q.InsertMembership(ctx, u.ID, org, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = p.e.pool.Exec(c, `DELETE FROM memberships WHERE user_id = $1`, u.ID)
		_, _ = p.e.pool.Exec(c, `DELETE FROM users WHERE id = $1`, u.ID)
	})
	tok, _, err := auth.IssueJWT(p.e.secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

// refused fails unless the request answered want with the error code code.
func (p *permOrgRows) refused(t *testing.T, want int, code, method, path, body string) {
	t.Helper()
	st, out := p.e.do(t, method, path, p.mixed, body)
	if st != want || prom0113Code(out) != code {
		t.Errorf("%s %s as an agent of A: got %d %v, want %d %s", method, path, st, out, want, code)
	}
}

// allowed fails unless the owner of A got exactly want.
func (p *permOrgRows) allowed(t *testing.T, want int, method, path, body string) {
	t.Helper()
	if st, out := p.e.do(t, method, path, p.ownerA, body); st != want {
		t.Errorf("%s %s as the owner of A: got %d %v, want %d", method, path, st, out, want)
	}
}

func (p *permOrgRows) scalar(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := p.e.pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// P1: the organization routes name their organization `{id}`, not `{org_id}`.
func TestPermOrg_OrganizationRoutesDecideInThatOrganization(t *testing.T) {
	p := newPermOrgRows(t)
	org := "/v1/organizations/" + p.orgA.String()
	name := p.scalar(t, `SELECT name FROM organizations WHERE id = $1`, p.orgA)

	p.refused(t, http.StatusForbidden, "permissions.denied", http.MethodPatch, org, `{"name":"taken over"}`)
	p.refused(t, http.StatusForbidden, "permissions.denied", http.MethodDelete, org, "")
	if got := p.scalar(t, `SELECT name || ':' || (deleted_at IS NULL)::text FROM organizations WHERE id = $1`, p.orgA); got != name+":true" {
		t.Errorf("organization A changed by an agent of A: %s", got)
	}

	p.allowed(t, http.StatusOK, http.MethodPatch, org, `{"name":"`+name+` renamed"}`)
	// The owner deletes an organization of their own (a fresh one, A is still used).
	orgD := p.e.newOrg(t, "D")
	ownerD := p.userToken(t, map[uuid.UUID]string{orgD: "org_admin"})
	if st, out := p.e.do(t, http.MethodDelete, "/v1/organizations/"+orgD.String(), ownerD, ""); st != http.StatusOK && st != http.StatusNoContent {
		t.Errorf("owner deletes their organization: %d %v", st, out)
	}
	t.Cleanup(func() { _, _ = p.e.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgD) })
}

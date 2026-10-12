//go:build integration

// perm_org_rows_integration_test.go — PERM-ORG: a route that learns its
// organization from the row it loads, or from an `{id}` that names the
// organization itself, decides the permission inside THAT organization.
//
// The caller in every case holds a membership in organization A that carries
// no permission at all (agent) and the owner role of organization B. Owning B
// lets them past every permission gate that decides from the union of their
// memberships; being a member of A lets them past every membership guard. Only
// the per-organization decision stops them.
package httpserver

import (
	"context"
	"fmt"
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

// P2: the allocation routes are not scoped at the gate; each side of an
// allocation is checked in its own organization.
func TestPermOrg_AllocationSidesDecideInTheirOrganization(t *testing.T) {
	e := newSweepEnv(t)
	orgA := e.newOrg(t, "A")
	w := e.newWorld(t, orgA, e.orgB) // A organizes, B is the partner
	p := &permOrgRows{e: e, orgA: orgA, w: w}
	// agent of the organizer A, owner of the partner B
	mixed := p.userToken(t, map[uuid.UUID]string{orgA: "agent", e.orgB: "org_admin"})
	ownerA := p.userToken(t, map[uuid.UUID]string{orgA: "org_admin"})
	base := "/v1/organizations/" + e.orgB.String() + "/external-allocations"
	one := base + "/" + w.alloc.String()

	create := fmt.Sprintf(`{"session_id":%q,"quota_qty":2,"status":"pending"}`, w.s1)
	if st, out := e.do(t, http.MethodPost, base, mixed, create); st != http.StatusNotFound {
		t.Errorf("an agent of the organizer creates an allocation on its session: %d %v, want 404", st, out)
	}
	if st, out := e.do(t, http.MethodPatch, one, mixed, `{"status":"active"}`); st != http.StatusForbidden || prom0113Code(out) != "allocation.organizer_only" {
		t.Errorf("an agent of the organizer activates the allocation: %d %v, want 403 allocation.organizer_only", st, out)
	}
	if got := p.scalar(t, `SELECT status FROM external_allocations WHERE id = $1`, w.alloc); got != "pending" {
		t.Errorf("allocation moved by an agent of the organizer: %s", got)
	}
	// The partner side still works: the owner of B reads its allocation.
	if st, out := e.do(t, http.MethodGet, one, mixed, ""); st != http.StatusOK {
		t.Errorf("owner of the partner reads its allocation: %d %v", st, out)
	}
	// The organizer's own owner still runs it.
	if st, out := e.do(t, http.MethodPatch, one, ownerA, `{"status":"active"}`); st != http.StatusOK {
		t.Errorf("owner of the organizer activates the allocation: %d %v", st, out)
	}

	// An agent of the PARTNER with the owner role of the organizer: the
	// partner side is decided in B, where they hold nothing.
	agentOfPartner := p.userToken(t, map[uuid.UUID]string{e.orgB: "agent"})
	if st, out := e.do(t, http.MethodGet, one, agentOfPartner, ""); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Errorf("an agent of the partner reads the allocation: %d %v, want a refusal", st, out)
	}
}

// P3: flat routes that load a row of A, one case per guard family.
func TestPermOrg_FlatRowRoutesDecideInTheRowOrganization(t *testing.T) {
	p := newPermOrgRows(t)
	w := p.w

	t.Run("complimentary revoke", func(t *testing.T) {
		path := "/v1/complimentary/" + w.issuance.String() + "/revoke"
		p.refused(t, http.StatusForbidden, "permissions.denied", http.MethodPost, path, `{"reason":"permorg"}`)
		if got := p.scalar(t, `SELECT status FROM complimentary_issuances WHERE id = $1`, w.issuance); got != "issued" {
			t.Errorf("issuance revoked by an agent of A: %s", got)
		}
	})

	t.Run("seat category and admin seat list", func(t *testing.T) {
		p.refused(t, http.StatusForbidden, "permissions.denied", http.MethodPatch,
			"/v1/event-sessions/"+w.s3.String()+"/seats/category",
			fmt.Sprintf(`{"seat_keys":["S|A|1"],"tier_id":%q}`, w.tier))
		p.refused(t, http.StatusForbidden, "permissions.denied", http.MethodGet,
			"/v1/event-sessions/"+w.s1.String()+"/seats/admin", "")
		p.allowed(t, http.StatusOK, http.MethodGet, "/v1/event-sessions/"+w.s1.String()+"/seats/admin", "")
	})

	t.Run("session media", func(t *testing.T) {
		p.refused(t, http.StatusForbidden, "org.access_denied", http.MethodPut,
			"/v1/sessions/"+w.s1.String()+"/media", `{"items":[]}`)
		p.allowed(t, http.StatusOK, http.MethodPut, "/v1/sessions/"+w.s1.String()+"/media", `{"items":[]}`)
	})

	t.Run("publications and report", func(t *testing.T) {
		pubs := "/v1/events/" + w.event.String() + "/publications"
		p.refused(t, http.StatusNotFound, "publication.event_not_found", http.MethodPost, pubs, fmt.Sprintf(`{"feed_token_id":%q}`, w.feedToken))
		p.refused(t, http.StatusNotFound, "publication.event_not_found", http.MethodDelete, pubs+"/"+w.feedToken.String(), "")
		if got := p.scalar(t, `SELECT count(*)::text FROM event_publications WHERE event_id = $1`, w.event); got != "0" {
			t.Errorf("event of A published by an agent of A: %s rows", got)
		}
		p.refused(t, http.StatusNotFound, "event.not_found", http.MethodPost, "/v1/events/"+w.event.String()+"/report", "")
	})

	t.Run("seating plans", func(t *testing.T) {
		plan := "/v1/seating-plans/" + w.planID.String()
		p.refused(t, http.StatusForbidden, "seating_plan.owner_org_forbidden", http.MethodPost,
			"/v1/venues/"+w.venue.String()+"/seating-plans",
			fmt.Sprintf(`{"owner_org_id":%q,"name":"permorg","plan_type":"general_admission"}`, p.orgA))
		p.refused(t, http.StatusNotFound, "seating_plan.not_found", http.MethodPatch, plan, `{"name":"permorg"}`)
		p.refused(t, http.StatusForbidden, "seating_plan.owner_org_forbidden", http.MethodPost, plan+"/fork",
			fmt.Sprintf(`{"owner_org_id":%q,"venue_id":%q,"name":"permorg fork"}`, p.orgA, w.venue))
		p.refused(t, http.StatusNotFound, "seating_plan.not_found", http.MethodPost, plan+"/versions",
			`{"geometry":{"sectors":[]}}`)
		if got := p.scalar(t, `SELECT name FROM seating_plans WHERE id = $1`, w.planID); got == "permorg" {
			t.Error("plan of A renamed by an agent of A")
		}
	})

	t.Run("reconciliation report", func(t *testing.T) {
		p.e.exec(t, `UPDATE external_allocations SET status = 'active' WHERE id = $1`, w.alloc)
		body := fmt.Sprintf(`{"allocation_id":%q,"lines":[{"external_ref":"PERMORG-1","line_type":"sale","qty":1}]}`, w.alloc)
		t.Cleanup(func() {
			c := context.Background()
			_, _ = p.e.pool.Exec(c, `DELETE FROM reconciliation_lines WHERE report_id IN (SELECT id FROM reconciliation_reports WHERE allocation_id = $1)`, w.alloc)
			_, _ = p.e.pool.Exec(c, `DELETE FROM reconciliation_reports WHERE allocation_id = $1`, w.alloc)
		})
		p.refused(t, http.StatusNotFound, "reconciliation.allocation_not_found", http.MethodPost, "/v1/reconciliation/reports", body)
		if got := p.scalar(t, `SELECT count(*)::text FROM reconciliation_reports WHERE allocation_id = $1`, w.alloc); got != "0" {
			t.Errorf("report filed on A's allocation by an agent of A: %s", got)
		}
		p.allowed(t, http.StatusCreated, http.MethodPost, "/v1/reconciliation/reports", body)
	})
}

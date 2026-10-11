//go:build integration

package httpserver

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// A reconciliation report is readable by its partner organization and by the
// organization that owns the allocated session, nobody else.
func TestOrgIsolationReports_ReconciliationReportStaysWithItsOrganizations(t *testing.T) {
	e := newSweepEnv(t)
	orgA := e.newOrg(t, "A")
	wA := e.newWorld(t, orgA, e.orgC)

	report := uuid.New()
	e.exec(t, `INSERT INTO reconciliation_reports (id, allocation_id, partner_org_id) VALUES ($1, $2, $3)`, report, wA.alloc, e.orgC)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM reconciliation_reports WHERE id = $1`, report)
	})

	creator, err := e.q.InsertUser(context.Background(), "recon-key-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, creator.ID) })
	_, rawKey, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(e.q), apikeys.IssueInput{
		OrgID: e.orgB, Name: "recon-" + uuid.NewString()[:6], Scopes: []string{"reconciliation.read"}, CreatedBy: creator.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}

	path := "/v1/reconciliation/reports/" + report.String()
	for name, tok := range map[string]string{"b-owner": e.owner, "b-api-key": rawKey} {
		if status, out := e.do(t, "GET", path, tok, ""); status != 404 {
			t.Errorf("%s reading another organization's report: got %d %v, want 404", name, status, out)
		}
	}

	// The organization that owns the allocated session reads it.
	u, err := e.q.InsertUser(context.Background(), "recon-a-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM memberships WHERE user_id = $1`, u.ID)
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID)
	})
	if _, err := e.q.InsertMembership(context.Background(), u.ID, orgA, "org_admin"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	tok, _, err := auth.IssueJWT(e.secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	if status, out := e.do(t, "GET", path, tok, ""); status != 200 {
		t.Errorf("session organization reading its report: got %d %v, want 200", status, out)
	}
}

// The organization list holds only the organizations the caller may read.
func TestOrgIsolationReports_OrganizationListIsScopedToTheCaller(t *testing.T) {
	e := newSweepEnv(t)
	orgA := e.newOrg(t, "A")

	status, out := e.do(t, "GET", "/v1/organizations", e.owner, "")
	if status != 200 {
		t.Fatalf("list organizations: %d %v", status, out)
	}
	ids := map[string]bool{}
	if list, ok := out["organizations"].([]any); ok {
		for _, o := range list {
			if m, ok := o.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					ids[id] = true
				}
			}
		}
	}
	if ids[orgA.String()] {
		t.Errorf("organization A is listed for an owner of organization B")
	}
	if !ids[e.orgB.String()] {
		t.Errorf("the caller's own organization is missing from the list")
	}
}

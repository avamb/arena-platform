//go:build integration

// seating_plan_read_access_integration_test.go — who reads a seating plan on
// the four GET routes (list by venue, read one, versions list, one version).
// A private plan is read by its owner organization (a member or its API key)
// and by a platform superadmin; a plan that is not private is read by every
// caller with seating_plan.read. Every request goes through the real router.
//
//	DATABASE_URL=... go test -tags integration -count=1 \
//	    ./apps/backend/internal/platform/httpserver/ -run SeatingPlanReadAccess
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// listedPlans returns the ids of the plans GET .../seating-plans lists.
func (e *spvEnv) listedPlans(t *testing.T, token string) map[string]bool {
	t.Helper()
	out := e.call(t, http.StatusOK, "", "GET", "/v1/venues/"+e.venueA.String()+"/seating-plans", token, "", "")
	ids := map[string]bool{}
	for _, it := range out["seating_plans"].([]any) {
		ids[it.(map[string]any)["id"].(string)] = true
	}
	return ids
}

// readsPlan asserts the caller reads plan on all four routes.
func (e *spvEnv) readsPlan(t *testing.T, plan uuid.UUID, token string) {
	t.Helper()
	if !e.listedPlans(t, token)[plan.String()] {
		t.Fatalf("plan %s missing from the venue list", plan)
	}
	p := "/v1/seating-plans/" + plan.String()
	e.call(t, http.StatusOK, "", "GET", p, token, "", "")
	e.call(t, http.StatusOK, "", "GET", p+"/versions", token, "", "")
	e.call(t, http.StatusOK, "", "GET", p+"/versions/1", token, "", "")
}

// cannotReadPlan asserts the caller sees plan on none of the four routes.
func (e *spvEnv) cannotReadPlan(t *testing.T, plan uuid.UUID, token string) {
	t.Helper()
	if e.listedPlans(t, token)[plan.String()] {
		t.Fatalf("plan %s is in the venue list", plan)
	}
	p := "/v1/seating-plans/" + plan.String()
	e.call(t, http.StatusNotFound, "seating_plan.not_found", "GET", p, token, "", "")
	e.call(t, http.StatusNotFound, "seating_plan.not_found", "GET", p+"/versions", token, "", "")
	e.call(t, http.StatusNotFound, "seating_plan.version_not_found", "GET", p+"/versions/1", token, "", "")
}

func TestSeatingPlanReadAccess_PrivatePlan(t *testing.T) {
	e := newSPVEnv(t)
	private := e.plan(t, e.orgA, e.adminA, "Read access private")

	t.Run("another organization does not see it", func(t *testing.T) {
		e.cannotReadPlan(t, private, e.adminB)
	})
	t.Run("an API key of another organization does not see it", func(t *testing.T) {
		e.cannotReadPlan(t, private, e.key(t, e.orgB, "seating_plan.read"))
	})
	t.Run("the owner organization reads it", func(t *testing.T) {
		e.readsPlan(t, private, e.adminA)
	})
	t.Run("an API key of the owner organization reads it", func(t *testing.T) {
		e.readsPlan(t, private, e.key(t, e.orgA, "seating_plan.read"))
	})
	t.Run("a platform superadmin reads it without a reason", func(t *testing.T) {
		e.readsPlan(t, private, e.super)
	})
}

func TestSeatingPlanReadAccess_NonPrivatePlan(t *testing.T) {
	e := newSPVEnv(t)
	plan := e.plan(t, e.orgA, e.adminA, "Read access shared")
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`UPDATE seating_plans SET visibility = 'shared_read' WHERE id = $1`, plan); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
	e.readsPlan(t, plan, e.adminB)
	e.readsPlan(t, plan, e.adminA)
}

// TestSeatingPlanVersion_GeometryJSONDecorIsDrawingOnly: a version sent as
// geometry JSON stores its decor through the decor writer, like an SVG.
func TestSeatingPlanVersion_GeometryJSONDecorIsDrawingOnly(t *testing.T) {
	e := newSPVEnv(t)
	plan := e.plan(t, e.orgA, e.adminA, "Geometry JSON decor")
	p := "/v1/seating-plans/" + plan.String()
	v1 := e.call(t, http.StatusOK, "", "GET", p+"/versions/1", e.adminA, "", "")
	geom := v1["seating_plan_version"].(map[string]any)["geometry"].(map[string]any)
	geom["decor_svg"] = `<rect width="4" onclick="x()"/><script>x()</script><img src="x"/>`
	body, err := json.Marshal(map[string]any{"geometry": geom})
	if err != nil {
		t.Fatal(err)
	}
	e.call(t, http.StatusCreated, "", "POST", p+"/versions", e.adminA, "", string(body))
	var stored string
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT geometry->>'decor_svg' FROM seating_plan_versions WHERE seating_plan_id = $1 AND version_number = 2`, plan).
		Scan(&stored); err != nil {
		t.Fatalf("read stored decor: %v", err)
	}
	if want := `<rect width="4"/>`; stored != want {
		t.Fatalf("stored decor_svg = %q, want %q", stored, want)
	}
}

func TestSeatingPlanReadAccess_ListKeepsOwnAndNonPrivate(t *testing.T) {
	e := newSPVEnv(t)
	privateA := e.plan(t, e.orgA, e.adminA, "Read access list A")
	rowB, err := e.q.InsertSeatingPlan(context.Background(), e.venueA, e.orgB, "Read access list B", "assigned_seats", "private", "active", nil)
	if err != nil {
		t.Fatalf("plan of organization B: %v", err)
	}
	privateB := rowB.ID
	shared := e.plan(t, e.orgA, e.adminA, "Read access list shared")
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`UPDATE seating_plans SET visibility = 'public_template' WHERE id = $1`, shared); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
	ids := e.listedPlans(t, e.adminB)
	if ids[privateA.String()] || !ids[privateB.String()] || !ids[shared.String()] {
		t.Fatalf("organization B lists %v, want its own plan and the public one only", ids)
	}
	ids = e.listedPlans(t, e.super)
	if !ids[privateA.String()] || !ids[privateB.String()] || !ids[shared.String()] {
		t.Fatalf("superadmin lists %v, want every plan", ids)
	}
}

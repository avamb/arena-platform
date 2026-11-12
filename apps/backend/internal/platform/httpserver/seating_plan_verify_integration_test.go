//go:build integration

// seating_plan_verify_integration_test.go — spec 08_architecture/64 section
// 3.4: a platform superadmin verifies a seating plan, the plan becomes the
// template of its venue, another organization copies it and the copy never
// changes the template. Every request goes through the real router.
//
//	DATABASE_URL=... go test -tags integration -count=1 \
//	    ./apps/backend/internal/platform/httpserver/ -run SeatingPlanVerify
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// spvSVG4 is a hall with one category and FOUR seats (planUpSVG has three).
const spvSVG4 = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 200 100">` +
	`<g id="PriceCategory"><circle inkscape:label="#Standard" cx="1" cy="1" r="1" style="fill:#ff0000"/></g>` +
	`<g inkscape:label="#Parter"><title>1</title>` +
	`<circle cx="10" cy="10" r="3" style="fill:#ff0000"><title>1</title></circle>` +
	`<circle cx="20" cy="10" r="3" style="fill:#ff0000"><title>2</title></circle>` +
	`<circle cx="30" cy="10" r="3" style="fill:#ff0000"><title>3</title></circle>` +
	`<circle cx="40" cy="10" r="3" style="fill:#ff0000"><title>4</title></circle>` +
	`</g></svg>`

type spvEnv struct {
	*sec1Env
	orgA, orgB     uuid.UUID
	adminA, adminB string
	superID        uuid.UUID
	super          string
	venueA         uuid.UUID
	plans          []uuid.UUID
}

func newSPVEnv(t *testing.T) *spvEnv {
	t.Helper()
	e := &spvEnv{sec1Env: newSec1Env(t)}
	e.orgA, e.orgB = e.org(t, "SPV-A"), e.org(t, "SPV-B")
	ua, ub := e.user(t, "spv-a"), e.user(t, "spv-b")
	e.member(t, ua, e.orgA, "org_admin")
	e.member(t, ub, e.orgB, "org_admin")
	e.adminA, e.adminB = e.token(t, ua), e.token(t, ub)
	e.superID, e.super = e.superadmin(t)
	e.venueA = uuid.New()
	ctx := context.Background()
	if _, err := e.srv.pgxPool.Exec(ctx,
		`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`,
		e.venueA, e.orgA, "SPV Hall "+e.venueA.String()[:8]); err != nil {
		t.Fatalf("venue fixture: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		all := `(SELECT id FROM seating_plans WHERE owner_org_id = ANY($1))`
		orgs := []uuid.UUID{e.orgA, e.orgB}
		for _, stmt := range []string{
			`DELETE FROM audit_events WHERE resource_type = 'seating_plan' AND resource_id IN (SELECT id::text FROM seating_plans WHERE owner_org_id = ANY($1))`,
			`UPDATE seating_plans SET current_version_id = NULL WHERE id IN ` + all,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN ` + all,
			`DELETE FROM seating_plans WHERE source_seating_plan_id IN ` + all,
			`DELETE FROM seating_plans WHERE owner_org_id = ANY($1)`,
			`DELETE FROM venues WHERE org_id = ANY($1)`,
		} {
			if _, err := e.srv.pgxPool.Exec(ctx, stmt, orgs); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	return e
}

func spvJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// call sends a request and fails unless it answered want (and code, when set).
func (e *spvEnv) call(t *testing.T, want int, code, method, path, bearer, reason, body string) map[string]any {
	t.Helper()
	st, out := e.do(t, method, path, bearer, reason, body)
	if st != want {
		t.Fatalf("%s %s: got %d %s, want %d", method, path, st, out, want)
	}
	if code != "" && sec1Code(out) != code {
		t.Fatalf("%s %s: code %q (%s), want %q", method, path, sec1Code(out), out, code)
	}
	if out == "" {
		return nil
	}
	return spvJSON(t, out)
}

// plan creates an active assigned_seats plan of org with one version.
func (e *spvEnv) plan(t *testing.T, org uuid.UUID, token, name string) uuid.UUID {
	t.Helper()
	out := e.call(t, http.StatusCreated, "", "POST", "/v1/venues/"+e.venueA.String()+"/seating-plans", token, "",
		fmt.Sprintf(`{"owner_org_id":%q,"name":%q,"plan_type":"assigned_seats","status":"active"}`, org, name))
	id := uuid.MustParse(out["seating_plan"].(map[string]any)["id"].(string))
	svg, _ := json.Marshal(planUpSVG)
	e.call(t, http.StatusCreated, "", "POST", "/v1/seating-plans/"+id.String()+"/versions", token, "", `{"svg":`+string(svg)+`}`)
	return id
}

// templateState is everything of a plan a copy must never change.
func (e *spvEnv) templateState(t *testing.T, plan uuid.UUID) string {
	t.Helper()
	var s string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `
		SELECT p.owner_org_id || '|' || p.name || '|' || p.status || '|' || p.visibility || '|' ||
		       COALESCE(p.current_version_id::text, '-') || '|' || p.category_name_overrides::text || '|' ||
		       p.updated_at || '|' ||
		       (SELECT count(*) FROM seating_plan_versions v WHERE v.seating_plan_id = p.id) || '|' ||
		       (SELECT string_agg(v.version_number || ':' || v.geometry_checksum || ':' || md5(v.geometry::text), ',' ORDER BY v.version_number)
		        FROM seating_plan_versions v WHERE v.seating_plan_id = p.id)
		FROM seating_plans p WHERE p.id = $1`, plan).Scan(&s); err != nil {
		t.Fatalf("template state: %v", err)
	}
	return s
}

func (e *spvEnv) planRow(t *testing.T, plan uuid.UUID) (state, visibility, status string, note *string, verifiedBy *uuid.UUID) {
	t.Helper()
	var st *string
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT verification_state, visibility, status, verification_note, verified_by FROM seating_plans WHERE id = $1`, plan).
		Scan(&st, &visibility, &status, &note, &verifiedBy); err != nil {
		t.Fatalf("plan row: %v", err)
	}
	if st != nil {
		state = *st
	}
	return state, visibility, status, note, verifiedBy
}

func (e *spvEnv) templates(t *testing.T, org uuid.UUID, token string) []map[string]any {
	t.Helper()
	out := e.call(t, http.StatusOK, "", "GET",
		"/v1/organizations/"+org.String()+"/venues/"+e.venueA.String()+"/plan-templates", token, "", "")
	var items []map[string]any
	for _, it := range out["plan_templates"].([]any) {
		items = append(items, it.(map[string]any))
	}
	return items
}

func TestSeatingPlanVerify_TemplateFlow(t *testing.T) {
	e := newSPVEnv(t)
	template := e.plan(t, e.orgA, e.adminA, "SPV template")
	verify := "/v1/admin/seating-plans/" + template.String() + "/verify"
	needsFix := "/v1/admin/seating-plans/" + template.String() + "/needs-fix"

	// (a) seating_plan.verify, a platform superadmin and X-Admin-Reason.
	e.call(t, http.StatusForbidden, "", "POST", verify, e.adminA, "check", "")
	e.call(t, http.StatusForbidden, "", "GET", "/v1/admin/seating-plans", e.adminB, "", "")
	e.call(t, http.StatusBadRequest, "superadmin.missing_reason", "POST", verify, e.super, "", "")
	key := e.key(t, e.orgA, "seating_plan.read")
	e.call(t, http.StatusForbidden, "", "POST", verify, key, "check", "")
	if st, _, _, _, _ := e.planRow(t, template); st != "" {
		t.Fatalf("refused calls changed the state to %q", st)
	}

	// The venue is not verified yet: org B sees no templates there.
	e.call(t, http.StatusNotFound, "venue.not_found", "GET",
		"/v1/organizations/"+e.orgB.String()+"/venues/"+e.venueA.String()+"/plan-templates", e.adminB, "", "")

	// (b) The superadmin verifies org A's plan (state NULL until now).
	out := e.call(t, http.StatusOK, "", "POST", verify, e.super, "hall checked", "")
	if out["verification_state"] != "verified" || out["visibility"] != "operator_verified" ||
		out["venue_review_status"] != "verified" || out["changed"] != true || out["verified_at"] == nil {
		t.Fatalf("verify answer: %v", out)
	}
	st, vis, _, _, by := e.planRow(t, template)
	if st != "verified" || vis != "operator_verified" || by == nil || *by != e.superID {
		t.Fatalf("plan after verify: %s %s %v", st, vis, by)
	}
	var review string
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT review_status FROM venues WHERE id = $1 AND verified_at IS NOT NULL`, e.venueA).Scan(&review); err != nil || review != "verified" {
		t.Fatalf("venue after verify: %q %v", review, err)
	}
	var audits int
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'v1.seating_plan.verify' AND resource_id = $1 AND metadata->>'reason' = 'hall checked'`,
		template.String()).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("verify audit rows: %d %v", audits, err)
	}
	if out := e.call(t, http.StatusOK, "", "POST", verify, e.super, "again", ""); out["changed"] != false {
		t.Fatalf("repeated verify: %v", out)
	}

	// (c) Org B lists the venue's templates and copies the template.
	items := e.templates(t, e.orgB, e.adminB)
	if len(items) != 1 || items[0]["plan_id"] != template.String() || items[0]["own"] != false || items[0]["capacity_seated"] != float64(3) {
		t.Fatalf("org B templates: %v", items)
	}
	if own := e.templates(t, e.orgA, e.adminA); len(own) != 1 || own[0]["own"] != true {
		t.Fatalf("org A templates: %v", own)
	}
	before := e.templateState(t, template)
	fork := e.call(t, http.StatusCreated, "", "POST", "/v1/seating-plans/"+template.String()+"/fork", e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q}`, e.orgB))["seating_plan"].(map[string]any)
	forkID := uuid.MustParse(fork["id"].(string))
	if fork["owner_org_id"] != e.orgB.String() || fork["status"] != "active" || fork["visibility"] != "private" ||
		fork["source_seating_plan_id"] != template.String() || fork["current_version_id"] == nil {
		t.Fatalf("fork: %v", fork)
	}
	if st, _, _, _, _ := e.planRow(t, forkID); st != "" {
		t.Fatalf("fork verification state %q, want NULL", st)
	}
	svg4, _ := json.Marshal(spvSVG4)
	v := e.call(t, http.StatusCreated, "", "POST", "/v1/seating-plans/"+forkID.String()+"/versions", e.adminB, "", `{"svg":`+string(svg4)+`}`)
	if v["seating_plan_version"].(map[string]any)["capacity_seated"] != float64(4) {
		t.Fatalf("fork version: %v", v)
	}
	e.call(t, http.StatusOK, "", "PATCH", "/v1/seating-plans/"+forkID.String(), e.adminB, "",
		`{"name":"SPV copy of B","category_name_overrides":{"1":"Gold"}}`)
	if after := e.templateState(t, template); after != before {
		t.Fatalf("the copy changed the template:\n before %s\n after  %s", before, after)
	}

	// (d) A private plan of another organization is not copied; nobody sets
	// operator_verified by hand.
	private := e.plan(t, e.orgA, e.adminA, "SPV private")
	e.call(t, http.StatusNotFound, "seating_plan.not_found", "POST", "/v1/seating-plans/"+private.String()+"/fork", e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q}`, e.orgB))
	e.call(t, http.StatusForbidden, "seating_plan.verify_required", "PATCH", "/v1/seating-plans/"+forkID.String(), e.adminB, "",
		`{"visibility":"operator_verified"}`)
	e.call(t, http.StatusForbidden, "seating_plan.verify_required", "POST", "/v1/venues/"+e.venueA.String()+"/seating-plans", e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q,"name":"SPV self","plan_type":"assigned_seats","visibility":"operator_verified"}`, e.orgB))

	// (f) The queue: an unverified plan with its organization and venue names.
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`UPDATE seating_plans SET verification_state = 'unverified' WHERE id = $1`, private); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range e.call(t, http.StatusOK, "", "GET", "/v1/admin/seating-plans", e.super, "", "")["seating_plans"].([]any) {
		row := it.(map[string]any)
		if row["plan_id"] == private.String() {
			found = true
			if row["owner_org_id"] != e.orgA.String() || row["owner_org_name"] == "" || row["venue_name"] == "" ||
				row["verification_state"] != "unverified" || row["capacity_seated"] != float64(3) || row["version_number"] != float64(1) {
				t.Errorf("queue row: %v", row)
			}
		}
		if row["plan_id"] == template.String() {
			t.Errorf("the verified template is in the unverified queue")
		}
	}
	if !found {
		t.Error("the unverified plan is not in the queue")
	}

	// (e) Needs fix: the template is private again and leaves the list; the
	// copy keeps working; the venue stays verified.
	e.call(t, http.StatusBadRequest, "seating_plan.invalid_note", "POST", needsFix, e.super, "fix", `{"note":""}`)
	out = e.call(t, http.StatusOK, "", "POST", needsFix, e.super, "row 7 wrong", `{"note":"Row 7 has 18 seats, the plan shows 17"}`)
	if out["verification_state"] != "needs_fix" || out["visibility"] != "private" || out["venue_review_status"] != "verified" {
		t.Fatalf("needs-fix answer: %v", out)
	}
	st, vis, _, note, _ := e.planRow(t, template)
	if st != "needs_fix" || vis != "private" || note == nil || *note != "Row 7 has 18 seats, the plan shows 17" {
		t.Fatalf("plan after needs-fix: %s %s %v", st, vis, note)
	}
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'v1.seating_plan.needs_fix' AND resource_id = $1`, template.String()).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("needs-fix audit rows: %d %v", audits, err)
	}
	if items := e.templates(t, e.orgB, e.adminB); len(items) != 0 {
		t.Fatalf("org B still sees templates after needs-fix: %v", items)
	}
	e.call(t, http.StatusNotFound, "seating_plan.not_found", "POST", "/v1/seating-plans/"+template.String()+"/fork", e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q,"name":"SPV second copy"}`, e.orgB))
	e.call(t, http.StatusOK, "", "GET", "/v1/seating-plans/"+forkID.String(), e.adminB, "", "")
	e.call(t, http.StatusCreated, "", "POST", "/v1/seating-plans/"+forkID.String()+"/versions", e.adminB, "", `{"svg":`+string(svg4)+`}`)
	found = false
	for _, it := range e.call(t, http.StatusOK, "", "GET", "/v1/admin/seating-plans?verification_state=needs_fix", e.super, "", "")["seating_plans"].([]any) {
		if it.(map[string]any)["plan_id"] == template.String() {
			found = true
		}
	}
	if !found {
		t.Error("the plan is not in the needs_fix list")
	}

	// The owner of a verified template may only take it back to private,
	// which sends it back to the queue.
	e.call(t, http.StatusOK, "", "POST", verify, e.super, "fixed", "")
	e.call(t, http.StatusConflict, "seating_plan.template_visibility_locked", "PATCH", "/v1/seating-plans/"+template.String(), e.adminA, "",
		`{"visibility":"public_template"}`)
	e.call(t, http.StatusOK, "", "PATCH", "/v1/seating-plans/"+template.String(), e.adminA, "", `{"visibility":"private"}`)
	if st, vis, _, _, by := e.planRow(t, template); st != "unverified" || vis != "private" || by != nil {
		t.Fatalf("template taken back by its owner: %s %s %v", st, vis, by)
	}
}

// A template never changes without a new verification: a new version posted by
// the OWNER sends the plan back to private and unverified in the same
// transaction; a platform superadmin's version (X-Admin-Reason) keeps it
// verified (spec 64 section 3.4).
func TestSeatingPlanVerify_OwnerVersionResetsTheTemplate(t *testing.T) {
	e := newSPVEnv(t)
	plan := e.plan(t, e.orgA, e.adminA, "SPV reset")
	verify := "/v1/admin/seating-plans/" + plan.String() + "/verify"
	versions := "/v1/seating-plans/" + plan.String() + "/versions"
	svg4, _ := json.Marshal(spvSVG4)
	body := `{"svg":` + string(svg4) + `}`

	e.call(t, http.StatusOK, "", "POST", verify, e.super, "hall checked", "")
	if st, vis, _, _, _ := e.planRow(t, plan); st != "verified" || vis != "operator_verified" {
		t.Fatalf("before: %s %s", st, vis)
	}

	// The superadmin's fix is a new version and the template stays verified.
	e.call(t, http.StatusBadRequest, "superadmin.missing_reason", "POST", versions, e.super, "", body)
	out := e.call(t, http.StatusCreated, "", "POST", versions, e.super, "row fix", body)
	if v := out["seating_plan_version"].(map[string]any); v["version_number"] != float64(2) {
		t.Fatalf("superadmin version: %v", v)
	}
	if st, vis, _, _, by := e.planRow(t, plan); st != "verified" || vis != "operator_verified" || by == nil {
		t.Fatalf("after the superadmin's version: %s %s %v", st, vis, by)
	}
	if got := out["seating_plan"].(map[string]any)["visibility"]; got != "operator_verified" {
		t.Errorf("answer visibility %v", got)
	}

	// The owner's version resets it, in the answer too.
	out = e.call(t, http.StatusCreated, "", "POST", versions, e.adminA, "", body)
	if v := out["seating_plan_version"].(map[string]any); v["version_number"] != float64(3) {
		t.Fatalf("owner version: %v", v)
	}
	if got := out["seating_plan"].(map[string]any)["visibility"]; got != "private" {
		t.Errorf("answer visibility %v, want private", got)
	}
	st, vis, _, _, by := e.planRow(t, plan)
	if st != "unverified" || vis != "private" || by != nil {
		t.Fatalf("after the owner's version: %s %s %v", st, vis, by)
	}
	// No other organization sees a template any more, and the plan is in the queue again.
	if items := e.templates(t, e.orgB, e.adminB); len(items) != 0 {
		t.Fatalf("org B still sees the template: %v", items)
	}
	// A private plan stays private and unverified when its owner posts again.
	e.call(t, http.StatusCreated, "", "POST", versions, e.adminA, "", body)
	if st, vis, _, _, _ := e.planRow(t, plan); st != "unverified" || vis != "private" {
		t.Fatalf("second owner version: %s %s", st, vis)
	}
}

// The superadmin exemption needs a non-empty X-Admin-Reason: a superadmin who
// is also a member of the owner organization and posts a version without the
// header is answered like any member, and the template is reset.
func TestSeatingPlanVerify_SuperadminMemberWithoutReasonResetsTheTemplate(t *testing.T) {
	e := newSPVEnv(t)
	plan := e.plan(t, e.orgA, e.adminA, "SPV member superadmin")
	e.member(t, e.superID, e.orgA, "org_admin")
	verify := "/v1/admin/seating-plans/" + plan.String() + "/verify"
	versions := "/v1/seating-plans/" + plan.String() + "/versions"
	svg4, _ := json.Marshal(spvSVG4)
	body := `{"svg":` + string(svg4) + `}`

	e.call(t, http.StatusOK, "", "POST", verify, e.super, "hall checked", "")
	// With the reason: the superadmin's fix keeps the template.
	e.call(t, http.StatusCreated, "", "POST", versions, e.super, "row fix", body)
	if st, vis, _, _, _ := e.planRow(t, plan); st != "verified" || vis != "operator_verified" {
		t.Fatalf("after the version with a reason: %s %s", st, vis)
	}
	// Without the reason: no 400, the version is a member's and the template is reset.
	out := e.call(t, http.StatusCreated, "", "POST", versions, e.super, "", body)
	if got := out["seating_plan"].(map[string]any)["visibility"]; got != "private" {
		t.Errorf("answer visibility %v, want private", got)
	}
	if st, vis, _, _, by := e.planRow(t, plan); st != "unverified" || vis != "private" || by != nil {
		t.Fatalf("after the version without a reason: %s %s %v", st, vis, by)
	}
}

// A template is visibility operator_verified AND verification_state verified.
// A plan that carries operator_verified from before the verification routes
// (state NULL) is not listed and not copied by another organization.
func TestSeatingPlanVerify_OperatorVerifiedWithoutStateIsNotATemplate(t *testing.T) {
	e := newSPVEnv(t)
	legacy := e.plan(t, e.orgA, e.adminA, "SPV legacy")
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`UPDATE seating_plans SET visibility = 'operator_verified', verification_state = NULL WHERE id = $1`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`UPDATE venues SET review_status = 'verified', verified_at = now() WHERE id = $1`, e.venueA); err != nil {
		t.Fatal(err)
	}
	if items := e.templates(t, e.orgB, e.adminB); len(items) != 0 {
		t.Fatalf("org B lists a plan without a verified state: %v", items)
	}
	if items := e.templates(t, e.orgA, e.adminA); len(items) != 0 {
		t.Fatalf("org A lists a plan without a verified state: %v", items)
	}
	e.call(t, http.StatusNotFound, "seating_plan.not_found", "POST", "/v1/seating-plans/"+legacy.String()+"/fork", e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q}`, e.orgB))

	// The verify route makes it a template.
	e.call(t, http.StatusOK, "", "POST", "/v1/admin/seating-plans/"+legacy.String()+"/verify", e.super, "legacy checked", "")
	if items := e.templates(t, e.orgB, e.adminB); len(items) != 1 {
		t.Fatalf("org B templates after verify: %v", items)
	}
}

// A venue_id in the fork body follows the rule of plan-templates: the forking
// organization's own venue or a verified venue, else 404 venue.not_found.
func TestSeatingPlanVerify_ForkVenueOverride(t *testing.T) {
	e := newSPVEnv(t)
	template := e.plan(t, e.orgA, e.adminA, "SPV fork venue")
	e.call(t, http.StatusOK, "", "POST", "/v1/admin/seating-plans/"+template.String()+"/verify", e.super, "hall checked", "")
	ctx := context.Background()
	otherA, ownB := uuid.New(), uuid.New()
	for _, v := range []struct {
		id  uuid.UUID
		org uuid.UUID
	}{{otherA, e.orgA}, {ownB, e.orgB}} {
		if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`,
			v.id, v.org, "SPV venue "+v.id.String()[:8]); err != nil {
			t.Fatalf("venue fixture: %v", err)
		}
	}
	fork := "/v1/seating-plans/" + template.String() + "/fork"
	var before int
	count := func() int {
		var n int
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM seating_plans WHERE owner_org_id = $1`, e.orgB).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before = count()
	// Another organization's venue that is not verified, and a venue that does not exist.
	for _, venue := range []uuid.UUID{otherA, uuid.New()} {
		e.call(t, http.StatusNotFound, "venue.not_found", "POST", fork, e.adminB, "",
			fmt.Sprintf(`{"owner_org_id":%q,"venue_id":%q}`, e.orgB, venue))
	}
	if count() != before {
		t.Fatal("a refused fork made a plan")
	}
	// The forking organization's own venue, and a verified venue (the template's).
	out := e.call(t, http.StatusCreated, "", "POST", fork, e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q,"venue_id":%q,"name":"SPV copy own venue"}`, e.orgB, ownB))
	if got := out["seating_plan"].(map[string]any)["venue_id"]; got != ownB.String() {
		t.Errorf("fork venue %v, want %s", got, ownB)
	}
	e.call(t, http.StatusCreated, "", "POST", fork, e.adminB, "",
		fmt.Sprintf(`{"owner_org_id":%q,"venue_id":%q,"name":"SPV copy verified venue"}`, e.orgB, e.venueA))
}

// A plan that is not active, or whose current version is missing, is not
// verified.
func TestSeatingPlanVerify_RefusesADraftOrAnEmptyPlan(t *testing.T) {
	e := newSPVEnv(t)
	out := e.call(t, http.StatusCreated, "", "POST", "/v1/venues/"+e.venueA.String()+"/seating-plans", e.adminA, "",
		fmt.Sprintf(`{"owner_org_id":%q,"name":"SPV empty","plan_type":"assigned_seats","status":"active"}`, e.orgA))
	empty := out["seating_plan"].(map[string]any)["id"].(string)
	e.call(t, http.StatusConflict, "seating_plan.no_current_version", "POST", "/v1/admin/seating-plans/"+empty+"/verify", e.super, "check", "")

	draft := e.plan(t, e.orgA, e.adminA, "SPV draft")
	e.call(t, http.StatusOK, "", "PATCH", "/v1/seating-plans/"+draft.String(), e.adminA, "", `{"status":"draft"}`)
	e.call(t, http.StatusConflict, "seating_plan.not_active", "POST", "/v1/admin/seating-plans/"+draft.String()+"/verify", e.super, "check", "")
	e.call(t, http.StatusNotFound, "seating_plan.not_found", "POST", "/v1/admin/seating-plans/"+uuid.NewString()+"/verify", e.super, "check", "")
	var review string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT review_status FROM venues WHERE id = $1`, e.venueA).Scan(&review); err != nil || review == "verified" {
		t.Fatalf("a refused verify verified the venue: %q %v", review, err)
	}
}

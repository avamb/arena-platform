//go:build integration

package httpserver

// seat_layouts_integration_test.go — task S1d through the REAL router, on
// the Palác Akropolis session of the S1e fixture (partial sales included):
//
//   - saved layouts: create from a payload, from the plan's geometry and from
//     the session's current seats; list, get, rename, delete; a taken name is
//     409; a foreign organization sees none of it;
//   - applying a layout: a dry run answers the diff and leaves no trace, the
//     real call is one apply_layout batch with ONE version bump that skips
//     sold and held seats, creates the missing category WITH its colour and
//     reverts through the ordinary batch route;
//   - plan annotations: the owner stores the stage, suggest then uses it
//     (annotations_source "plan"), a request's own annotations still win, a
//     foreign organization cannot read or write them;
//   - the colour of a category: set, cleared, refused when malformed, listed.

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
)

func (e *s1eEnv) planPath(org uuid.UUID, tail string) string {
	return "/v1/organizations/" + org.String() + "/seating-plans/" + e.plan.String() + tail
}

func (e *s1eEnv) sessionLayoutsPath(org, session uuid.UUID, tail string) string {
	return "/v1/organizations/" + org.String() + "/sessions/" + session.String() + tail
}

func (e *s1eEnv) seatTierName(t *testing.T, key string) string {
	t.Helper()
	var name string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT t.name FROM session_seats ss JOIN ticket_tiers t ON t.id = ss.tier_id WHERE ss.session_id = $1 AND ss.seat_key = $2`,
		e.seated, key).Scan(&name); err != nil {
		t.Fatalf("tier of %s: %v", key, err)
	}
	return name
}

func (e *s1eEnv) seatStatus(t *testing.T, key string) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT status FROM session_seats WHERE session_id = $1 AND seat_key = $2`, e.seated, key).Scan(&s); err != nil {
		t.Fatalf("status of %s: %v", key, err)
	}
	return s
}

func (e *s1eEnv) tierCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, e.seated).Scan(&n); err != nil {
		t.Fatalf("tier count: %v", err)
	}
	return n
}

func (e *s1eEnv) version(t *testing.T) int64 {
	t.Helper()
	var v int64
	if err := e.pool.QueryRow(context.Background(), `SELECT seat_status_version FROM sessions WHERE id = $1`, e.seated).Scan(&v); err != nil {
		t.Fatalf("version: %v", err)
	}
	return v
}

func s1dErrCode(out map[string]any) string {
	obj, _ := out["error"].(map[string]any)
	code, _ := obj["code"].(string)
	return code
}

func jsonKeys(keys []string) string {
	out := "["
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += `"` + k + `"`
	}
	return out + "]"
}

// s1dLayoutBody is a manual layout over free seats of the fixture: ten seats
// into "Hall" (the session's existing category, matched by name), ten into a
// new "Gold" category with a colour, and two closed.
func s1dLayoutBody(t *testing.T, e *s1eEnv, name string) (body string, hall, gold, closed []string) {
	t.Helper()
	seats := layoutgen.Seats(e.geometry)
	if len(seats) < 60 {
		t.Fatalf("fixture has %d seats, need 60", len(seats))
	}
	for _, s := range seats[20:30] {
		hall = append(hall, s.Key)
	}
	for _, s := range seats[30:40] {
		gold = append(gold, s.Key)
	}
	for _, s := range seats[40:42] {
		closed = append(closed, s.Key)
	}
	assign := ""
	add := func(keys []string, slot string) {
		for _, k := range keys {
			if assign != "" {
				assign += ","
			}
			assign += `"` + k + `":"` + slot + `"`
		}
	}
	add(hall, "hall")
	add(gold, "gold")
	body = `{"name":"` + name + `","payload":{"slots":[` +
		`{"key":"hall","name":"hall","price_amount":150000,"sort_order":0},` +
		`{"key":"gold","name":"Gold","color":"#FFD700","price_amount":400000,"sort_order":1}],` +
		`"assignments":{` + assign + `},"closed_seat_keys":` + jsonKeys(closed) + `}}`
	return body, hall, gold, closed
}

func TestS1d_LayoutsCRUDApplyAndRevert(t *testing.T) {
	e := newS1eEnv(t)
	layouts := e.planPath(e.orgA, "/layouts")

	// Three ways to make a layout.
	body, hall, gold, closed := s1dLayoutBody(t, e, "Gold front")
	status, made := e.call(t, http.MethodPost, layouts, e.owner, body)
	if status != http.StatusCreated {
		t.Fatalf("create manual = %d %v", status, made)
	}
	layoutID, _ := made["id"].(string)
	if made["source"] != "manual" || made["algorithm"] != nil {
		t.Fatalf("layout = %v", made)
	}
	if status, out := e.call(t, http.MethodPost, layouts, e.owner, body); status != http.StatusConflict || s1dErrCode(out) != "seat_layout.duplicate_name" {
		t.Fatalf("duplicate name = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, layouts, e.owner, `{"name":"gold FRONT ","payload":{"slots":[{"key":"a","name":"A","price_amount":1}],"assignments":{"x":"a"}}}`); status != http.StatusConflict {
		t.Fatalf("a name that differs only by case and spaces = %d %v", status, out)
	}

	status, geo := e.call(t, http.MethodPost, layouts, e.owner, `{"name":"From the plan","source":"geometry"}`)
	if status != http.StatusCreated || geo["source"] != "geometry" {
		t.Fatalf("create geometry = %d %v", status, geo)
	}
	geoPayload, _ := geo["payload"].(map[string]any)
	if slots, _ := geoPayload["slots"].([]any); len(slots) == 0 {
		t.Fatalf("a geometry layout has the plan's categories: %v", geoPayload)
	}

	status, copied := e.call(t, http.MethodPost, layouts, e.owner, `{"name":"Today","source":"copied_from_session","session_id":"`+e.seated.String()+`"}`)
	if status != http.StatusCreated || copied["source"] != "copied_from_session" {
		t.Fatalf("create copied = %d %v", status, copied)
	}
	copyPayload, _ := copied["payload"].(map[string]any)
	if cl, _ := copyPayload["closed_seat_keys"].([]any); len(cl) != len(e.closed) {
		t.Fatalf("the session's closed seats are in the copy: %v vs %v", copyPayload["closed_seat_keys"], e.closed)
	}

	status, alg := e.call(t, http.MethodPost, layouts, e.owner,
		`{"name":"Proposal","source":"allocator","algorithm":"desirability","params":{"price_max":1000,"price_min":500},"payload":{"slots":[{"key":"a","name":"Only","price_amount":900}],"assignments":{"x":"a"},"frozen_seat_keys":["y"]}}`)
	if status != http.StatusCreated || alg["algorithm"] != "desirability" {
		t.Fatalf("create allocator = %d %v", status, alg)
	}
	if params, _ := alg["params"].(map[string]any); params["price_max"] == nil {
		t.Fatalf("the algorithm's parameters are kept: %v", alg["params"])
	}
	if pl, _ := alg["payload"].(map[string]any); pl["frozen_seat_keys"] != nil {
		t.Fatalf("a frozen list must not be stored: %v", pl)
	}

	// List: summaries without the seat maps.
	status, list := e.call(t, http.MethodGet, layouts, e.mgr, "")
	if status != http.StatusOK {
		t.Fatalf("list = %d %v", status, list)
	}
	items, _ := list["layouts"].([]any)
	if len(items) != 4 || list["max_layouts"] == nil {
		t.Fatalf("list = %v", list)
	}
	first, _ := items[len(items)-1].(map[string]any) // oldest = "Gold front"
	if first["name"] != "Gold front" || first["assignment_count"] != float64(20) || first["closed_count"] != float64(2) || first["payload"] != nil {
		t.Fatalf("summary = %v", first)
	}

	// Rename and get.
	if status, out := e.call(t, http.MethodPatch, layouts+"/"+layoutID, e.owner, `{"name":"Gold rows"}`); status != http.StatusOK || out["name"] != "Gold rows" {
		t.Fatalf("rename = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPatch, layouts+"/"+layoutID, e.owner, `{"name":"Today"}`); status != http.StatusConflict {
		t.Fatalf("rename onto a taken name = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodGet, layouts+"/"+layoutID, e.owner, ""); status != http.StatusOK || out["id"] != layoutID {
		t.Fatalf("get = %d %v", status, out)
	}

	// Apply: a slot without a category and no permission to create one.
	apply := e.sessionLayoutsPath(e.orgA, e.seated, "/layouts/apply")
	fpBefore := e.seatFingerprint(t)
	status, out := e.call(t, http.MethodPost, apply, e.owner, `{"layout_id":"`+layoutID+`"}`)
	if status != http.StatusUnprocessableEntity || s1dErrCode(out) != "seat_layout.slots_unmapped" {
		t.Fatalf("unmapped = %d %v", status, out)
	}
	if e.seatFingerprint(t) != fpBefore {
		t.Fatal("a refused apply left a trace")
	}

	// Dry run: the same diff, nothing written, no category created.
	versionBefore, tiersBefore := e.version(t), e.tierCount(t)
	status, dry := e.call(t, http.MethodPost, apply, e.owner, `{"layout_id":"`+layoutID+`","create_missing_tiers":true,"dry_run":true}`)
	if status != http.StatusOK || dry["dry_run"] != true || dry["batch_id"] != nil || dry["seat_status_version"] != nil {
		t.Fatalf("dry run = %d %v", status, dry)
	}
	if ch, _ := dry["changes"].([]any); len(ch) != 12 {
		t.Fatalf("dry run changes = %d, want 10 into Gold + 2 closed (the Hall seats already are in Hall)", len(ch))
	}
	if e.seatFingerprint(t) != fpBefore || e.tierCount(t) != tiersBefore || e.version(t) != versionBefore {
		t.Fatal("a dry run left a trace")
	}

	// A frozen seat in the layout is skipped, not an error.
	frozen := e.soldWithOrder[0]
	frozenBody := `{"name":"With a sold seat","payload":{"slots":[{"key":"hall","name":"Hall","price_amount":1}],"assignments":{"` + frozen + `":"hall","` + e.held[0] + `":"hall"}}}`
	status, fz := e.call(t, http.MethodPost, layouts, e.owner, frozenBody)
	if status != http.StatusCreated {
		t.Fatalf("create frozen = %d %v", status, fz)
	}

	// The real call.
	status, res := e.call(t, http.MethodPost, apply, e.owner, `{"layout_id":"`+layoutID+`","create_missing_tiers":true}`)
	if status != http.StatusCreated || res["dry_run"] != false {
		t.Fatalf("apply = %d %v", status, res)
	}
	batchID, _ := res["batch_id"].(string)
	if batchID == "" || res["seat_status_version"] != float64(versionBefore+1) || e.version(t) != versionBefore+1 {
		t.Fatalf("apply result %v, version %d, want one bump from %d", res, e.version(t), versionBefore)
	}
	if e.tierCount(t) != tiersBefore+1 {
		t.Fatalf("tiers = %d, want Gold created", e.tierCount(t))
	}
	for _, k := range gold {
		if e.seatTierName(t, k) != "Gold" {
			t.Fatalf("seat %s is in %q, want Gold", k, e.seatTierName(t, k))
		}
	}
	for _, k := range closed {
		if e.seatStatus(t, k) != "unavailable" {
			t.Fatalf("seat %s status %s, want closed", k, e.seatStatus(t, k))
		}
	}
	for _, k := range hall {
		if e.seatTierName(t, k) != "Hall" {
			t.Fatalf("seat %s is in %q, want Hall", k, e.seatTierName(t, k))
		}
	}

	// The category carries the colour; the tier route lists it.
	tiersPath := "/v1/organizations/" + e.orgA.String() + "/events/" + e.event.String() + "/sessions/" + e.seated.String() + "/tiers"
	status, tiers := e.call(t, http.MethodGet, tiersPath, e.owner, "")
	if status != http.StatusOK {
		t.Fatalf("tiers = %d %v", status, tiers)
	}
	goldColor := ""
	for _, raw := range tiers["tiers"].([]any) {
		tier := raw.(map[string]any)
		if tier["name"] == "Gold" {
			goldColor, _ = tier["color"].(string)
		}
		if tier["name"] == "Hall" && tier["color"] != nil {
			t.Fatalf("Hall never had a colour: %v", tier["color"])
		}
	}
	if goldColor != "#ffd700" {
		t.Fatalf("Gold colour = %q, want #ffd700 (stored lower-case)", goldColor)
	}

	// The apply batch is in the history, kind apply_layout, and reverts.
	status, hist := e.call(t, http.MethodGet, e.sessionLayoutsPath(e.orgA, e.seated, "/seat-batches"), e.owner, "")
	if status != http.StatusOK {
		t.Fatalf("batches = %d", status)
	}
	var kind string
	for _, raw := range hist["items"].([]any) {
		b := raw.(map[string]any)
		if b["id"] == batchID {
			kind, _ = b["kind"].(string)
		}
	}
	if kind != "apply_layout" {
		t.Fatalf("batch %s kind = %q, want apply_layout (history %v)", batchID, kind, hist)
	}
	if status, out := e.call(t, http.MethodPost, e.sessionLayoutsPath(e.orgA, e.seated, "/seat-batches/"+batchID+"/revert"), e.owner, ""); status != http.StatusCreated {
		t.Fatalf("revert = %d %v", status, out)
	}
	for _, k := range gold {
		if e.seatTierName(t, k) != "Hall" {
			t.Fatalf("after revert seat %s is in %q, want Hall", k, e.seatTierName(t, k))
		}
	}

	// A sold and a held seat in a layout are skipped with a reason.
	status, fres := e.call(t, http.MethodPost, apply, e.owner, `{"layout_id":"`+fz["id"].(string)+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("apply frozen = %d %v", status, fres)
	}
	reasons := map[string]string{}
	for _, raw := range fres["skipped"].([]any) {
		s := raw.(map[string]any)
		reasons[s["seat_key"].(string)], _ = s["reason"].(string)
	}
	if reasons[frozen] != "sold" || reasons[e.held[0]] != "held" {
		t.Fatalf("skipped = %v", reasons)
	}

	// Delete.
	if status, _ := e.call(t, http.MethodDelete, layouts+"/"+layoutID, e.owner, ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	if status, out := e.call(t, http.MethodGet, layouts+"/"+layoutID, e.owner, ""); status != http.StatusNotFound || s1dErrCode(out) != "seat_layout.not_found" {
		t.Fatalf("get after delete = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, apply, e.owner, `{"layout_id":"`+layoutID+`"}`); status != http.StatusNotFound || s1dErrCode(out) != "seat_layout.not_found" {
		t.Fatalf("apply after delete = %d %v", status, out)
	}
}

func TestS1d_LayoutRoutesStayInsideTheOrganization(t *testing.T) {
	e := newS1eEnv(t)
	body, _, _, _ := s1dLayoutBody(t, e, "Mine")
	layouts := e.planPath(e.orgA, "/layouts")
	status, made := e.call(t, http.MethodPost, layouts, e.owner, body)
	if status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, made)
	}
	id, _ := made["id"].(string)

	// The agent holds no session.seats.manage; a foreigner is not a member.
	for _, tok := range []string{e.agent, e.foreignOwner} {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodGet, layouts, ""},
			{http.MethodPost, layouts, body},
			{http.MethodGet, layouts + "/" + id, ""},
			{http.MethodPatch, layouts + "/" + id, `{"name":"x"}`},
			{http.MethodDelete, layouts + "/" + id, ""},
			{http.MethodGet, e.planPath(e.orgA, "/annotations"), ""},
			{http.MethodPatch, e.planPath(e.orgA, "/annotations"), `{"focal_point":{"x":1,"y":2}}`},
			{http.MethodPost, e.sessionLayoutsPath(e.orgA, e.seated, "/layouts/apply"), `{"layout_id":"` + id + `"}`},
		} {
			if status, out := e.call(t, c.method, c.path, tok, c.body); status != http.StatusForbidden {
				t.Fatalf("%s %s = %d %v, want 403", c.method, c.path, status, out)
			}
		}
	}

	// The foreign owner names A's ids under ITS OWN organization.
	otherLayouts := e.planPath(e.orgB, "/layouts")
	for _, c := range []struct{ method, path, body, code string }{
		{http.MethodGet, otherLayouts, "", "seating_plan.not_found"},
		{http.MethodPost, otherLayouts, body, "seating_plan.not_found"},
		{http.MethodGet, e.planPath(e.orgB, "/annotations"), "", "seating_plan.not_found"},
		{http.MethodPatch, e.planPath(e.orgB, "/annotations"), `{"focal_point":{"x":1,"y":2}}`, "seating_plan.not_found"},
		{http.MethodPost, e.sessionLayoutsPath(e.orgB, e.seated, "/layouts/apply"), `{"layout_id":"` + id + `"}`, "session.not_found"},
	} {
		if status, out := e.call(t, c.method, c.path, e.foreignOwner, c.body); status != http.StatusNotFound || s1dErrCode(out) != c.code {
			t.Fatalf("%s %s = %d %v, want 404 %s", c.method, c.path, status, out, c.code)
		}
	}
	// A plan of its own: A's layout id is not found there either.
	ownPlan := uuid.New()
	venue := uuid.New()
	for _, st := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'S1d B hall', 'Europe/Prague')`, []any{venue, e.orgB}},
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, 'S1d B plan', 'assigned_seats', 'active')`, []any{ownPlan, venue, e.orgB}},
	} {
		if _, err := e.pool.Exec(context.Background(), st.sql, st.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	path := "/v1/organizations/" + e.orgB.String() + "/seating-plans/" + ownPlan.String() + "/layouts/" + id
	if status, out := e.call(t, http.MethodGet, path, e.foreignOwner, ""); status != http.StatusNotFound || s1dErrCode(out) != "seat_layout.not_found" {
		t.Fatalf("A's layout id on B's plan = %d %v", status, out)
	}
	// A layout of org B is not applicable to A's session: the layout is looked up in A.
	status, bl := e.call(t, http.MethodPost, "/v1/organizations/"+e.orgB.String()+"/seating-plans/"+ownPlan.String()+"/layouts", e.foreignOwner,
		`{"name":"B layout","payload":{"slots":[{"key":"a","name":"Hall","price_amount":1}],"assignments":{"x":"a"}}}`)
	if status != http.StatusCreated {
		t.Fatalf("B creates on its own plan = %d %v", status, bl)
	}
	if status, out := e.call(t, http.MethodPost, e.sessionLayoutsPath(e.orgA, e.seated, "/layouts/apply"), e.owner, `{"layout_id":"`+bl["id"].(string)+`"}`); status != http.StatusNotFound || s1dErrCode(out) != "seat_layout.not_found" {
		t.Fatalf("applying B's layout in A = %d %v", status, out)
	}
}

func TestS1d_AnnotationsStoredAndUsedBySuggest(t *testing.T) {
	e := newS1eEnv(t)
	ann := e.planPath(e.orgA, "/annotations")
	suggest := e.path(e.orgA, e.seated, "suggest")
	algBody := `{"algorithm":"desirability","params":{"price_max":1000,"price_min":500}}`

	status, out := e.call(t, http.MethodGet, ann, e.mgr, "")
	if status != http.StatusOK || out["writable"] != true {
		t.Fatalf("get empty = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, suggest, e.owner, algBody); status != http.StatusUnprocessableEntity || s1dErrCode(out) != "seat_layout.annotation_required" {
		t.Fatalf("no stage anywhere = %d %v", status, out)
	}

	// A bad value stores nothing.
	for _, bad := range []string{
		`{"section_weights":{"parter":3}}`,
		`{"section_weights":{"no such section":0.5}}`,
		`{"focal_point":"here"}`,
		`{"bogus":1}`,
	} {
		if status, out := e.call(t, http.MethodPatch, ann, e.owner, bad); status != http.StatusBadRequest {
			t.Fatalf("PATCH %s = %d %v, want 400", bad, status, out)
		}
	}

	status, out = e.call(t, http.MethodPatch, ann, e.mgr, s1eStage)
	if status != http.StatusOK {
		t.Fatalf("patch stage = %d %v", status, out)
	}
	var geometryChecksum string
	if err := e.pool.QueryRow(context.Background(), `SELECT geometry_checksum FROM seating_plan_versions WHERE seating_plan_id = $1`, e.plan).Scan(&geometryChecksum); err != nil || geometryChecksum == "" {
		t.Fatalf("checksum: %v", err)
	}

	// Suggest with no annotations in the body now uses the stored stage.
	fp := e.seatFingerprint(t)
	status, sug := e.call(t, http.MethodPost, suggest, e.owner, algBody)
	if status != http.StatusOK || sug["annotations_source"] != "plan" {
		t.Fatalf("suggest = %d source %v (%v)", status, sug["annotations_source"], sug)
	}
	if e.seatFingerprint(t) != fp {
		t.Fatal("suggest wrote something")
	}
	// A request's own annotations win.
	status, sug = e.call(t, http.MethodPost, suggest, e.owner,
		`{"algorithm":"desirability","params":{"price_max":1000,"price_min":500},"annotations":{"focal_point":{"x":10,"y":10}}}`)
	if status != http.StatusOK || sug["annotations_source"] != "request" {
		t.Fatalf("suggest with own annotations = %d source %v", status, sug["annotations_source"])
	}

	// Section weights of this plan are kept and checked; null clears one key.
	sections := e.geometry.Sections
	if len(sections) == 0 {
		t.Fatal("fixture has no sections")
	}
	weights := `{"section_weights":{"` + sections[0].Key + `":0.4}}`
	if status, out := e.call(t, http.MethodPatch, ann, e.owner, weights); status != http.StatusOK {
		t.Fatalf("patch weights = %d %v", status, out)
	}
	_, got := e.call(t, http.MethodGet, ann, e.owner, "")
	stored, _ := got["annotations"].(map[string]any)
	if stored["focal_point"] == nil || stored["section_weights"] == nil {
		t.Fatalf("a PATCH of one key kept the others: %v", got)
	}
	if status, out := e.call(t, http.MethodPatch, ann, e.owner, `{"focal_point":null}`); status != http.StatusOK {
		t.Fatalf("clear stage = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, suggest, e.owner, algBody); status != http.StatusUnprocessableEntity || s1dErrCode(out) != "seat_layout.annotation_required" {
		t.Fatalf("stage cleared = %d %v", status, out)
	}

	// A stale stored section is ignored, not an error.
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE seating_plans SET annotations = '{"focal_point":{"x":152,"y":20},"section_weights":{"gone":0.5},"obstructed_seat_keys":["gone|1|1"]}'::jsonb WHERE id = $1`, e.plan); err != nil {
		t.Fatal(err)
	}
	if status, out := e.call(t, http.MethodPost, suggest, e.owner, algBody); status != http.StatusOK || out["annotations_source"] != "plan" {
		t.Fatalf("stale stored annotations = %d %v", status, out)
	}

	// The agent cannot touch them; a shared plan is readable, never writable by another org.
	if status, _ := e.call(t, http.MethodPatch, ann, e.agent, s1eStage); status != http.StatusForbidden {
		t.Fatalf("agent patch = %d", status)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE seating_plans SET visibility = 'shared_read' WHERE id = $1`, e.plan); err != nil {
		t.Fatal(err)
	}
	status, shared := e.call(t, http.MethodGet, e.planPath(e.orgB, "/annotations"), e.foreignOwner, "")
	if status != http.StatusOK || shared["writable"] != false {
		t.Fatalf("shared plan read = %d %v", status, shared)
	}
	if status, out := e.call(t, http.MethodPatch, e.planPath(e.orgB, "/annotations"), e.foreignOwner, s1eStage); status != http.StatusNotFound {
		t.Fatalf("shared plan write by another org = %d %v, want 404 (fork it)", status, out)
	}
}

func TestS1d_TierColorSetClearAndRefuse(t *testing.T) {
	e := newS1eEnv(t)
	base := "/v1/organizations/" + e.orgA.String() + "/events/" + e.event.String() + "/sessions/" + e.seated.String() + "/tiers"
	one := base + "/" + e.tier.String()

	status, out := e.call(t, http.MethodGet, one, e.owner, "")
	tier, _ := out["tier"].(map[string]any)
	if status != http.StatusOK || tier["color"] != nil {
		t.Fatalf("get = %d %v (a category starts with no colour of its own)", status, out)
	}
	if status, out := e.call(t, http.MethodPatch, one, e.owner, `{"color":"#00AaFf"}`); status != http.StatusOK || out["tier"].(map[string]any)["color"] != "#00aaff" {
		t.Fatalf("set = %d %v", status, out)
	}
	for _, bad := range []string{`"blue"`, `"#12345"`, `"#12345g"`, `"00aaff"`, `5`} {
		if status, out := e.call(t, http.MethodPatch, one, e.owner, `{"color":`+bad+`}`); status != http.StatusBadRequest {
			t.Fatalf("color %s = %d %v, want 400", bad, status, out)
		}
	}
	// Another key leaves the colour alone; null and "" clear it.
	if status, out := e.call(t, http.MethodPatch, one, e.owner, `{"sort_order":3}`); status != http.StatusOK || out["tier"].(map[string]any)["color"] != "#00aaff" {
		t.Fatalf("a patch without color = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPatch, one, e.owner, `{"color":null}`); status != http.StatusOK || out["tier"].(map[string]any)["color"] != nil {
		t.Fatalf("clear = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPatch, one, e.owner, `{"color":"#010203"}`); status != http.StatusOK {
		t.Fatalf("set again = %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPatch, one, e.owner, `{"color":""}`); status != http.StatusOK || out["tier"].(map[string]any)["color"] != nil {
		t.Fatalf("clear with empty = %d %v", status, out)
	}

	// Create with a colour; the list carries it.
	status, made := e.call(t, http.MethodPost, base, e.owner, `{"name":"Colourful","pricing_mode":"fixed","price_amount":1000,"color":"#ABCDEF"}`)
	if status != http.StatusCreated || made["tier"].(map[string]any)["color"] != "#abcdef" {
		t.Fatalf("create = %d %v", status, made)
	}
	if status, out := e.call(t, http.MethodPost, base, e.owner, `{"name":"Bad","pricing_mode":"fixed","price_amount":1000,"color":"nope"}`); status != http.StatusBadRequest {
		t.Fatalf("create with a bad colour = %d %v", status, out)
	}
	status, list := e.call(t, http.MethodGet, base, e.owner, "")
	if status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}
	colors := map[string]any{}
	for _, raw := range list["tiers"].([]any) {
		tr := raw.(map[string]any)
		colors[tr["name"].(string)] = tr["color"]
	}
	if colors["Colourful"] != "#abcdef" || colors["Hall"] != nil {
		t.Fatalf("list colours = %v", colors)
	}

	// The database refuses what the API would never send.
	if _, err := e.pool.Exec(context.Background(), `UPDATE ticket_tiers SET color = 'red' WHERE id = $1`, e.tier); err == nil {
		t.Fatal("ticket_tiers_color_check let 'red' in")
	}
}

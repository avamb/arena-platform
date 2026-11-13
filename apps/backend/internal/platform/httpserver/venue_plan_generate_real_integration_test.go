//go:build integration

package httpserver

// venue_plan_generate_real_integration_test.go — spec 08_architecture/64 wave 2:
// the intake against the REAL venue SVG generator (no stub). Skipped unless
// VENUE_GEN_REAL_URL, VENUE_GEN_REAL_TOKEN and VENUE_GEN_SAMPLES_DIR (a folder
// with plan.xlsx, plan.pdf and plan.png) are set.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generator"
)

func TestVenuePlanGenerateReal(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("VENUE_GEN_REAL_URL"))
	token := strings.TrimSpace(os.Getenv("VENUE_GEN_REAL_TOKEN"))
	dir := strings.TrimSpace(os.Getenv("VENUE_GEN_SAMPLES_DIR"))
	if url == "" || token == "" || dir == "" {
		t.Skip("VENUE_GEN_REAL_URL, VENUE_GEN_REAL_TOKEN and VENUE_GEN_SAMPLES_DIR are not all set")
	}
	g := generator.New(url, token)
	// The steps run as the worker runs them: one short call each, a poll
	// every second.
	run := func(t *testing.T, e *vpiEnv, id string) []int {
		t.Helper()
		return runVenuePlanGenerateJobsWith(t, e.srv.pgxPool, e.srv.media, g, id, time.Second)
	}
	sample := func(t *testing.T, name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read sample %s: %v", name, err)
		}
		return raw
	}

	t.Run("xlsx_goes_to_review_and_is_approved", func(t *testing.T) {
		e := newVPIEnv(t)
		id := e.createReceived(t, vpiFile{"plan.xlsx", sample(t, "plan.xlsx")})
		if got := run(t, e, id); len(got) != 1 {
			t.Fatalf("generate jobs run: %v", got)
		}
		st, reason := e.state(t, id)
		t.Logf("plan.xlsx: state=%s escalation_reason=%q", st, reason)
		if st != "review" {
			t.Fatalf("state %s %s, want review", st, reason)
		}
		card := e.do(t, http.MethodGet, orgIntakePath(e.orgA, id), e.ownerA, "", "", nil, nil).intake(t)
		draft, _ := card["draft"].(map[string]any)
		if card["current_draft"] == nil || draft == nil || draft["author"] != "generator" {
			t.Fatalf("card %v", card)
		}
		svgPath := orgIntakePath(e.orgA, id) + "/drafts/1/svg"
		if got := e.do(t, http.MethodGet, svgPath, e.ownerA, "", "", nil, nil); got.status != http.StatusOK || !strings.Contains(string(got.body), "<svg") {
			t.Errorf("draft svg %s: %d", svgPath, got.status)
		}
		if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'draft_ready'`, id) != 1 {
			t.Errorf("no draft_ready notice")
		}
		r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.ownerA, "", `{"draft":1}`)
		if r.status != http.StatusOK || r.intake(t)["state"] != "approved" {
			t.Fatalf("approve: %d %s", r.status, r.body)
		}
		planID := r.intake(t)["plan_id"].(string)
		var status, visibility, vstate string
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT status, visibility, COALESCE(verification_state, '') FROM seating_plans WHERE id = $1`, planID).Scan(&status, &visibility, &vstate); err != nil {
			t.Fatal(err)
		}
		if status != "active" || visibility != "private" || vstate != "unverified" {
			t.Fatalf("plan %s/%s/%s, want active/private/unverified", status, visibility, vstate)
		}
		e.exec(t, `UPDATE venues SET review_status = 'verified', verified_at = now() WHERE id = $1`, e.venueA)
		templates := "/v1/organizations/" + e.orgB.String() + "/venues/" + e.venueA.String() + "/plan-templates"
		if r := e.do(t, http.MethodGet, templates, e.ownerB, "", "", nil, nil); r.status != http.StatusOK ||
			len(r.json(t)["plan_templates"].([]any)) != 0 {
			t.Fatalf("org B templates: %d %s", r.status, r.body)
		}
	})

	t.Run("pdf_goes_to_an_administrator", func(t *testing.T) {
		e := newVPIEnv(t)
		id := e.createReceived(t, vpiFile{"plan.pdf", sample(t, "plan.pdf")})
		run(t, e, id)
		st, reason := e.state(t, id)
		drafts := e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id)
		t.Logf("plan.pdf: state=%s escalation_reason=%q drafts=%d", st, reason, drafts)
		if st != "operator" || reason != "too_many_questions" {
			t.Fatalf("state %s %s, want operator too_many_questions", st, reason)
		}
	})

	t.Run("png_goes_to_an_administrator", func(t *testing.T) {
		e := newVPIEnv(t)
		id := e.createReceived(t, vpiFile{"plan.png", sample(t, "plan.png")})
		run(t, e, id)
		st, reason := e.state(t, id)
		drafts := e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id)
		t.Logf("plan.png: state=%s escalation_reason=%q drafts=%d", st, reason, drafts)
		if st != "operator" || reason != "unsupported_format" {
			t.Fatalf("state %s %s, want operator unsupported_format", st, reason)
		}
	})
}

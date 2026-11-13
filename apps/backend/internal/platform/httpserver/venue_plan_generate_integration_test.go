//go:build integration

package httpserver

// venue_plan_generate_integration_test.go — spec 08_architecture/64 wave 2: an
// intake without a clean source SVG stays received and queues the worker job
// venue_plan.generate; the real job handler sends the files to a stub of the
// venue SVG generator (HALL_MODEL.md section 7) through the real client and
// writes the outcome. The intake is created through the real router.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generator"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/notify"
)

const vpgToken = "vpg-generator-token"

// vpgXLSX is the smallest zip the intake sniffs as an Excel workbook.
func vpgXLSX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml", "xl/worksheets/sheet1.xml"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `<?xml version="1.0"?><x/>`)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// vpgAuthoringSVG is a valid authoring SVG of the generator's own fixtures.
func vpgAuthoringSVG(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "venue-svg-generator", "arena_export", "testdata", "synthetic_authoring.svg"))
	if err != nil {
		t.Fatalf("read the generator's authoring SVG: %v", err)
	}
	return string(raw)
}

// vpgStub is a fake generator. job answers GET /jobs/{id}; submit (when set)
// answers POST /generate.
type vpgStub struct {
	mu      sync.Mutex
	submits int
	kinds   []string
	badAuth int
	submit  func(n int) (int, string)
	job     func(poll int) (int, string)
	polls   int
}

func newVPGStub(t *testing.T, s *vpgStub) *generator.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if r.Header.Get("Authorization") != "Bearer "+vpgToken {
			s.badAuth++
			s.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"auth.required","message":"no"}}`)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/generate":
			s.submits++
			n := s.submits
			_ = r.ParseMultipartForm(64 << 20)
			s.kinds = append(s.kinds, r.FormValue("kinds"))
			fn := s.submit
			s.mu.Unlock()
			status, body := http.StatusAccepted, fmt.Sprintf(`{"job_id":"job-%d","status":"queued","status_url":"/jobs/job-%d"}`, n, n)
			if fn != nil {
				status, body = fn(n)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/jobs/"):
			s.polls++
			n, fn := s.polls, s.job
			s.mu.Unlock()
			status, body := fn(n)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			s.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return generator.New(ts.URL, vpgToken, generator.WithPollInterval(5*time.Millisecond), generator.WithDeadline(20*time.Second))
}

// vpgDone is a done job: result, validation status (nil = null) and SVG (nil = null).
func vpgDone(result string, status, svg *string, files string) string {
	j := func(s *string) string {
		if s == nil {
			return "null"
		}
		b, _ := json.Marshal(*s)
		return string(b)
	}
	model := `{"schema":"seating-json-v2","sections":[]}`
	preview := `"` + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake")) + `"`
	if result != "ok" {
		model, preview = "null", "null"
	}
	return fmt.Sprintf(`{"job_id":"job-1","status":"done","result":%q,"validation_status":%s,
		"generator_version":{"service":"g2b-0.2.0","commit":"test","model_schema":"seating-json-v2"},
		"files":%s,"model":%s,"authoring_svg":%s,"svg_info":{},"preview_png_base64":%s,"svg_url":null,"preview_png_url":null}`,
		result, j(status), files, model, j(svg), preview)
}

func sp(s string) *string { return &s }

const vpgUsedFile = `[{"index":0,"declared_kind":"xlsx","detected_kind":"xlsx","sha256":"x","size_bytes":1,"adapter":"xlsx_seating_adapter","outcome":"used","reason":null,"seat_count":3,"validation_status":"PASS"}]`

// runGenerate runs every pending venue_plan.generate job of the intake through
// the real handler, oldest first, and marks it done. It returns the attempts run.
func (e *vpiEnv) runGenerate(t *testing.T, g generate.Generator, id string) []int {
	t.Helper()
	return runVenuePlanGenerateJobs(t, e.srv.pgxPool, e.srv.media, g, id)
}

// runVenuePlanGenerateJobs is runGenerate for any test of the package: media
// nil or g nil is a worker without a generator (no_generator).
func runVenuePlanGenerateJobs(t *testing.T, pool *pgxpool.Pool, media *mediastore.Repo, g generate.Generator, id string) []int {
	t.Helper()
	ctx := context.Background()
	h := generate.NewHandler(generate.Options{
		Pool: pool, Media: media, Generator: g, Notifier: notify.Notifier{},
		Audit: audit.WithServiceActor(audit.NewPGWriter(pool)),
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT id::text, payload::text FROM worker_jobs
		WHERE job_type = 'venue_plan.generate' AND status = 'pending' AND payload->>'intake_id' = $1
		ORDER BY scheduled_at, created_at`, id)
	if err != nil {
		t.Fatal(err)
	}
	type job struct{ id, payload string }
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.payload); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	var attempts []int
	for _, j := range jobs {
		exec(`UPDATE worker_jobs SET status = 'claimed', claimed_at = now(), attempts = attempts + 1 WHERE id = $1`, j.id)
		if err := h(ctx, []byte(j.payload)); err != nil {
			t.Fatalf("venue_plan.generate handler: %v", err)
		}
		exec(`UPDATE worker_jobs SET status = 'done' WHERE id = $1`, j.id)
		var p struct {
			Attempt int `json:"attempt"`
		}
		_ = json.Unmarshal([]byte(j.payload), &p)
		attempts = append(attempts, p.Attempt)
	}
	return attempts
}

// toOperator sends a new intake to an administrator the way a worker without
// a generator does (no_generator).
func (e *vpiEnv) toOperator(t *testing.T, id string) {
	t.Helper()
	if got := e.runGenerate(t, nil, id); len(got) != 1 {
		t.Fatalf("generate jobs run: %v, want one", got)
	}
	if n := e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND state = 'operator' AND escalation_reason = 'no_generator'`, id); n != 1 {
		t.Fatalf("the intake did not go to an administrator")
	}
}

func (e *vpiEnv) state(t *testing.T, id string) (state, reason string) {
	t.Helper()
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT state, COALESCE(escalation_reason, '') FROM venue_plan_intakes WHERE id = $1`, id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	return state, reason
}

func (e *vpiEnv) notifyJobs(t *testing.T, id, kind string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'intake_id' = $1
		AND ($2 = '' OR payload->>'kind' = $2)`, id, kind)
}

func (e *vpiEnv) createReceived(t *testing.T, files ...vpiFile) string {
	t.Helper()
	res := e.create(t, e.orgA, e.venueA, e.ownerA, nil, files...)
	if res.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	in := res.intake(t)
	id := in["id"].(string)
	if in["state"] != "received" || in["escalation_reason"] != nil || in["current_draft"] != nil {
		t.Fatalf("a new intake without an SVG: %v", in)
	}
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.generate' AND status = 'pending'
		AND payload->>'intake_id' = $1 AND (payload->>'attempt')::int = 1`, id); n != 1 {
		t.Fatalf("generate jobs queued at create: %d, want 1", n)
	}
	if n := e.notifyJobs(t, id, ""); n != 0 {
		t.Fatalf("the create queued %d administrator notifications, want none", n)
	}
	return id
}

// (1) An Excel table: the generator answers PASS, the draft is in review for
// the organizer, nobody else is told.
func TestVenuePlanGenerate_XLSXPassGoesToReview(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.xlsx", vpgXLSX(t)})
	svg := vpgAuthoringSVG(t)
	s := &vpgStub{job: func(poll int) (int, string) {
		if poll == 1 {
			return 200, `{"job_id":"job-1","status":"running"}`
		}
		return 200, vpgDone("ok", sp("PASS"), &svg, vpgUsedFile)
	}}
	g := newVPGStub(t, s)
	if got := e.runGenerate(t, g, id); len(got) != 1 || got[0] != 1 {
		t.Fatalf("attempts %v", got)
	}
	if s.submits != 1 || s.kinds[0] != `["xlsx"]` || s.badAuth != 0 {
		t.Fatalf("sent %d times, kinds %v, bad auth %d", s.submits, s.kinds, s.badAuth)
	}
	if st, reason := e.state(t, id); st != "review" || reason != "" {
		t.Fatalf("state %s %s, want review", st, reason)
	}
	card := e.do(t, http.MethodGet, orgIntakePath(e.orgA, id), e.ownerA, "", "", nil, nil).intake(t)
	draft, _ := card["draft"].(map[string]any)
	if card["current_draft"] != float64(1) || draft == nil || draft["author"] != "generator" {
		t.Fatalf("card %v", card)
	}
	if s := draft["summary"].(map[string]any); s["seats"].(float64) < 1 {
		t.Errorf("summary %v", s)
	}
	if got := e.do(t, http.MethodGet, draft["svg_path"].(string), e.ownerA, "", "", nil, nil); got.status != http.StatusOK || !strings.Contains(string(got.body), "<svg") {
		t.Errorf("draft svg: %d", got.status)
	}
	if n := e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1 AND n = 1 AND author = 'generator'
		AND author_user_id IS NULL AND generator_version LIKE '%g2b-0.2.0%' AND model->>'schema' = 'seating-json-v2'
		AND preview_media_id IS NOT NULL AND report->>'validation_status' = 'PASS'`, id); n != 1 {
		t.Errorf("generated draft rows: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1`, id); n != 1 ||
		e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'draft_ready'`, id) != 1 {
		t.Errorf("notices: want exactly one draft_ready")
	}
	if n := e.notifyJobs(t, id, ""); n != 0 {
		t.Errorf("administrator notifications: %d, want none", n)
	}
	for _, action := range []string{"generate_started", "draft_generated"} {
		if e.count(t, `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2 AND actor_type = 'system' AND actor_id IS NULL`,
			"v1.venue_plan_intake."+action, id) != 1 {
			t.Errorf("no audit row %s", action)
		}
	}
	// A repeated job finds the intake in review and does nothing.
	if err := intakeEnqueue(e, id); err != nil {
		t.Fatal(err)
	}
	e.runGenerate(t, g, id)
	if s.submits != 1 || e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id) != 1 {
		t.Error("a repeated job sent the files again or made a second draft")
	}
}

func intakeEnqueue(e *vpiEnv, id string) error {
	_, err := e.srv.pgxPool.Exec(context.Background(), `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ('venue_plan.generate', jsonb_build_object('intake_id', $1::text, 'attempt', 1), 5, 'pending', now())`, id)
	return err
}

// (2) A photo the generator has no adapter for goes to an administrator:
// unsupported_format, the organizer's notice escalated, the administrators' job.
func TestVenuePlanGenerate_NoAdapterGoesToAnAdministrator(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"balcony.jpg", vpiJPEG(t)})
	s := &vpgStub{job: func(int) (int, string) {
		return 200, vpgDone("no_adapter", nil, nil,
			`[{"index":0,"declared_kind":"image","detected_kind":"image","outcome":"no_adapter","reason":"raster_profile_required"}]`)
	}}
	e.runGenerate(t, newVPGStub(t, s), id)
	if s.kinds[0] != `["image"]` {
		t.Errorf("kinds %v", s.kinds)
	}
	if st, reason := e.state(t, id); st != "operator" || reason != "unsupported_format" {
		t.Fatalf("state %s %s, want operator unsupported_format", st, reason)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'escalated'`, id) != 1 {
		t.Error("no escalated notice")
	}
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'intake_id' = $1
		AND payload->>'kind' = 'intake_escalated' AND payload->>'reason' = 'unsupported_format'`, id); n != 1 {
		t.Errorf("intake_escalated jobs %d, want 1", n)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id) != 0 {
		t.Error("a draft was written")
	}
	if e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.venue_plan_intake.generate_failed' AND resource_id = $1
		AND metadata->>'reason' = 'unsupported_format'`, id) != 1 {
		t.Error("no generate_failed audit row")
	}

	// A CSV has no reader in the generator: it is never sent.
	csvID := e.createReceived(t, vpiFile{"seats.csv", []byte("row,seat\n1,1\n1,2\n2,1\n")})
	s2 := &vpgStub{job: func(int) (int, string) { return 200, vpgDone("ok", sp("PASS"), nil, "[]") }}
	e.runGenerate(t, newVPGStub(t, s2), csvID)
	if st, reason := e.state(t, csvID); st != "operator" || reason != "unsupported_format" || s2.submits != 0 {
		t.Fatalf("csv: state %s %s, submits %d", st, reason, s2.submits)
	}
}

// (3) PASS but an SVG that does not import: nothing stored, draft_invalid.
func TestVenuePlanGenerate_BadSVGStoresNothing(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.xlsx", vpgXLSX(t)})
	mediaBefore := e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA)
	bad := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><circle r="1"/></svg>`
	s := &vpgStub{job: func(int) (int, string) { return 200, vpgDone("ok", sp("PASS"), &bad, vpgUsedFile) }}
	e.runGenerate(t, newVPGStub(t, s), id)
	if st, reason := e.state(t, id); st != "operator" || reason != "draft_invalid" {
		t.Fatalf("state %s %s, want operator draft_invalid", st, reason)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id) != 0 ||
		e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA) != mediaBefore {
		t.Error("a refused SVG left rows behind")
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'escalated'`, id) != 1 ||
		e.notifyJobs(t, id, "intake_escalated") != 1 {
		t.Error("the escalation was not told")
	}
}

// (4) QUARANTINE: the draft is kept for the administrator, too_many_questions.
func TestVenuePlanGenerate_QuarantineKeepsTheDraftForAnAdministrator(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.xlsx", vpgXLSX(t)})
	svg := vpgAuthoringSVG(t)
	s := &vpgStub{job: func(int) (int, string) { return 200, vpgDone("ok", sp("QUARANTINE"), &svg, vpgUsedFile) }}
	e.runGenerate(t, newVPGStub(t, s), id)
	if st, reason := e.state(t, id); st != "operator" || reason != "too_many_questions" {
		t.Fatalf("state %s %s, want operator too_many_questions", st, reason)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND current_draft = 1`, id) != 1 ||
		e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1 AND author = 'generator' AND report->>'validation_status' = 'QUARANTINE'`, id) != 1 {
		t.Error("the quarantined draft was not kept")
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'escalated'`, id) != 1 ||
		e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'draft_ready'`, id) != 0 {
		t.Error("notices of a quarantined draft")
	}
	if e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.venue_plan_intake.draft_generated' AND resource_id = $1
		AND metadata->>'reason' = 'too_many_questions'`, id) != 1 {
		t.Error("no draft_generated audit row")
	}
}

// (5) Three failures in a row: retries at 1 and 5 minutes, then generator_failed.
func TestVenuePlanGenerate_ThreeFailuresGoToAnAdministrator(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.pdf", vpiPDF()})
	s := &vpgStub{
		submit: func(int) (int, string) { return 503, `{"error":{"code":"queue.full","message":"busy"}}` },
		job:    func(int) (int, string) { return 500, `{}` },
	}
	g := newVPGStub(t, s)
	steps := []struct {
		attempt int
		next    int
		minWait int
	}{{1, 2, 55}, {2, 3, 295}}
	for _, st := range steps {
		if got := e.runGenerate(t, g, id); len(got) != 1 || got[0] != st.attempt {
			t.Fatalf("attempt %d: ran %v", st.attempt, got)
		}
		if state, _ := e.state(t, id); state != "generating" {
			t.Fatalf("after attempt %d: state %s, want generating", st.attempt, state)
		}
		if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.generate' AND status = 'pending'
			AND payload->>'intake_id' = $1 AND (payload->>'attempt')::int = $2
			AND scheduled_at > now() + make_interval(secs => $3)`, id, st.next, st.minWait); n != 1 {
			t.Fatalf("after attempt %d: no attempt %d scheduled %d s later", st.attempt, st.next, st.minWait)
		}
	}
	if got := e.runGenerate(t, g, id); len(got) != 1 || got[0] != 3 {
		t.Fatalf("attempt 3: ran %v", got)
	}
	if st, reason := e.state(t, id); st != "operator" || reason != "generator_failed" {
		t.Fatalf("state %s %s, want operator generator_failed", st, reason)
	}
	if s.submits != 3 {
		t.Errorf("submits %d, want 3", s.submits)
	}
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.generate' AND status = 'pending' AND payload->>'intake_id' = $1`, id); n != 0 {
		t.Errorf("%d attempts still pending", n)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'escalated'`, id) != 1 ||
		e.notifyJobs(t, id, "intake_escalated") != 1 {
		t.Error("the escalation was not told once")
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.venue_plan_intake.generate_failed' AND resource_id = $1`, id); n != 3 {
		t.Errorf("generate_failed audit rows %d, want 3", n)
	}
}

// (6) A worker without a generator: no_generator, told to everybody.
func TestVenuePlanGenerate_NoGeneratorConfigured(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.pdf", vpiPDF()})
	e.toOperator(t, id)
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'escalated'`, id) != 1 {
		t.Error("no escalated notice")
	}
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'intake_id' = $1
		AND payload->>'kind' = 'intake_escalated' AND payload->>'reason' = 'no_generator'`, id); n != 1 {
		t.Errorf("intake_escalated jobs %d, want 1", n)
	}
}

// (7) The organizer cancels while the generator works: the outcome is dropped.
func TestVenuePlanGenerate_CancelledMeanwhileWritesNothing(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.xlsx", vpgXLSX(t)})
	mediaBefore := e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA)
	svg := vpgAuthoringSVG(t)
	cancelStatus := 0
	s := &vpgStub{job: func(poll int) (int, string) {
		if poll == 1 {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.ts.URL+orgIntakePath(e.orgA, id)+"/cancel", nil)
			req.Header.Set("Authorization", "Bearer "+e.ownerA)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				cancelStatus = resp.StatusCode
				_ = resp.Body.Close()
			}
		}
		return 200, vpgDone("ok", sp("PASS"), &svg, vpgUsedFile)
	}}
	e.runGenerate(t, newVPGStub(t, s), id)
	if cancelStatus != http.StatusOK {
		t.Fatalf("cancel during generation: %d", cancelStatus)
	}
	if st, _ := e.state(t, id); st != "cancelled" {
		t.Fatalf("state %s, want cancelled", st)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id) != 0 ||
		e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1`, id) != 0 ||
		e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA) != mediaBefore ||
		e.notifyJobs(t, id, "") != 0 {
		t.Error("the outcome of a cancelled intake was written")
	}
}

// (8) The organizer approves a generated draft through the existing route: a
// private, unverified plan no other organization can copy until a superadmin
// verifies it.
func TestVenuePlanGenerate_ApprovedDraftNeedsVerificationBeforeAFork(t *testing.T) {
	e := newVPIEnv(t)
	id := e.createReceived(t, vpiFile{"hall.xlsx", vpgXLSX(t)})
	svg := vpgAuthoringSVG(t)
	s := &vpgStub{job: func(int) (int, string) { return 200, vpgDone("ok", sp("PASS"), &svg, vpgUsedFile) }}
	e.runGenerate(t, newVPGStub(t, s), id)
	r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.ownerA, "", `{"draft":1}`)
	if r.status != http.StatusOK || r.intake(t)["state"] != "approved" {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	planID := r.intake(t)["plan_id"].(string)
	t.Cleanup(func() {
		c := context.Background()
		pool := e.srv.pgxPool
		forks := `(SELECT id FROM seating_plans WHERE source_seating_plan_id = $1::uuid)`
		_, _ = pool.Exec(c, `UPDATE seating_plans SET current_version_id = NULL WHERE id IN `+forks, planID)
		_, _ = pool.Exec(c, `DELETE FROM seating_plan_versions WHERE seating_plan_id IN `+forks, planID)
		_, _ = pool.Exec(c, `DELETE FROM seating_plans WHERE source_seating_plan_id = $1::uuid`, planID)
	})
	var status, visibility, vstate string
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT status, visibility, COALESCE(verification_state, '') FROM seating_plans WHERE id = $1`, planID).Scan(&status, &visibility, &vstate); err != nil {
		t.Fatal(err)
	}
	if status != "active" || visibility != "private" || vstate != "unverified" {
		t.Fatalf("plan %s/%s/%s, want active/private/unverified", status, visibility, vstate)
	}
	// The venue is open to every organization, the plan is not.
	e.exec(t, `UPDATE venues SET review_status = 'verified', verified_at = now() WHERE id = $1`, e.venueA)
	templates := "/v1/organizations/" + e.orgB.String() + "/venues/" + e.venueA.String() + "/plan-templates"
	if r := e.do(t, http.MethodGet, templates, e.ownerB, "", "", nil, nil); r.status != http.StatusOK ||
		len(r.json(t)["plan_templates"].([]any)) != 0 {
		t.Fatalf("org B templates before verification: %d %s", r.status, r.body)
	}
	fork := "/v1/seating-plans/" + planID + "/fork"
	body := fmt.Sprintf(`{"owner_org_id":%q}`, e.orgB)
	if r := e.jsonDo(t, http.MethodPost, fork, e.ownerB, "", body); r.status != http.StatusNotFound {
		t.Fatalf("fork before verification: %d %s", r.status, r.body)
	}
	if r := e.jsonDo(t, http.MethodPost, "/v1/admin/seating-plans/"+planID+"/verify", e.super, "hall checked", ""); r.status != http.StatusOK {
		t.Fatalf("verify: %d %s", r.status, r.body)
	}
	if r := e.do(t, http.MethodGet, templates, e.ownerB, "", "", nil, nil); r.status != http.StatusOK ||
		len(r.json(t)["plan_templates"].([]any)) != 1 {
		t.Fatalf("org B templates after verification: %d %s", r.status, r.body)
	}
	if r := e.jsonDo(t, http.MethodPost, fork, e.ownerB, "", body); r.status != http.StatusCreated {
		t.Fatalf("fork after verification: %d %s", r.status, r.body)
	}
}

//go:build integration

package httpserver

// venue_plan_intake_integration_test.go — the hall plan intake routes of spec
// 08_architecture/64 (wave 1) through the real router: files in, an
// administrator's draft, the organizer's approval, and the walls around it
// (another organization, an API key, the admin gate, the media routes).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

const vpiSigningSecret = "vpi-media-signing-secret"

type vpiEnv struct {
	srv    *Server
	ts     *httptest.Server
	q      *gen.Queries
	secret string

	orgA, orgB, venueA, venueB uuid.UUID
	ownerA, ownerB, agentA     string
	super                      string
	superID                    uuid.UUID
}

type vpiFile struct {
	name string
	data []byte
}

func newVPIEnv(t *testing.T) *vpiEnv {
	t.Helper()
	base, secret := productionIntegrationServer(t)
	local, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	media, err := mediastore.New(mediastore.Options{Pool: base.pgxPool, Storage: local, SigningSecret: []byte(vpiSigningSecret)})
	if err != nil {
		t.Fatalf("mediastore.New: %v", err)
	}
	cfg := *base.cfg
	srv := New(Options{Config: &cfg, Pool: base.pool, PgxPool: base.pgxPool, Auth: base.stub, Verifier: base.verifier, Media: media})
	t.Cleanup(func() { _ = srv.closeAnalytics(context.Background()) })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	e := &vpiEnv{srv: srv, ts: ts, q: gen.New(srv.pgxPool), secret: secret}
	ctx := context.Background()

	e.orgA, e.orgB = e.org(t, "A"), e.org(t, "B")
	e.venueA, e.venueB = uuid.New(), uuid.New()
	e.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'VPI Hall A', 'Europe/Madrid')`, e.venueA, e.orgA)
	e.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'VPI Hall B', 'Europe/Madrid')`, e.venueB, e.orgB)
	_, e.ownerA = e.member(t, e.orgA, "org_admin")
	_, e.ownerB = e.member(t, e.orgB, "org_admin")
	_, e.agentA = e.member(t, e.orgA, "agent")
	e.superID = e.user(t, "super")
	e.exec(t, `INSERT INTO user_roles (user_id, role_id, org_id)
	           SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, e.superID)
	e.super = e.token(t, e.superID)

	t.Cleanup(func() {
		c := context.Background()
		pool := srv.pgxPool
		for _, org := range []uuid.UUID{e.orgA, e.orgB} {
			_, _ = pool.Exec(c, `DELETE FROM billable_events WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM venue_plan_intakes WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `UPDATE seating_plans SET current_version_id = NULL WHERE owner_org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`, org)
			_, _ = pool.Exec(c, `DELETE FROM seating_plans WHERE owner_org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM media_objects WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM billing_accounts WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM venues WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM memberships WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`, org)
			_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
		}
	})
	_ = ctx
	return e
}

func (e *vpiEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (e *vpiEnv) org(t *testing.T, label string) uuid.UUID {
	t.Helper()
	suffix := uuid.NewString()[:8]
	o, err := e.q.InsertOrganization(context.Background(), "VPI "+label+" "+suffix, "vpi-"+strings.ToLower(label)+"-"+suffix, "ES", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	return o.ID
}

func (e *vpiEnv) user(t *testing.T, label string) uuid.UUID {
	t.Helper()
	u, err := e.q.InsertUser(context.Background(), "vpi-"+label+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	return u.ID
}

func (e *vpiEnv) token(t *testing.T, user uuid.UUID) string {
	t.Helper()
	tok, _, err := auth.IssueJWT(e.secret, user, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

func (e *vpiEnv) member(t *testing.T, org uuid.UUID, role string) (uuid.UUID, string) {
	t.Helper()
	u := e.user(t, role)
	if _, err := e.q.InsertMembership(context.Background(), u, org, role); err != nil {
		t.Fatalf("InsertMembership(%s): %v", role, err)
	}
	return u, e.token(t, u)
}

type vpiResp struct {
	status int
	body   []byte
	header http.Header
}

func (r vpiResp) json(t *testing.T) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode %d %s: %v", r.status, r.body, err)
	}
	return out
}

func (r vpiResp) intake(t *testing.T) map[string]any {
	t.Helper()
	in, ok := r.json(t)["intake"].(map[string]any)
	if !ok {
		t.Fatalf("no intake in %d %s", r.status, r.body)
	}
	return in
}

func (r vpiResp) code() string {
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.body, &out)
	return out.Error.Code
}

// do sends one request. reason "" leaves X-Admin-Reason out.
func (e *vpiEnv) do(t *testing.T, method, path, token, reason, contentType string, body []byte, hdr map[string]string) vpiResp {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return vpiResp{status: resp.StatusCode, body: raw, header: resp.Header}
}

func (e *vpiEnv) jsonDo(t *testing.T, method, path, token, reason, body string) vpiResp {
	t.Helper()
	ct := ""
	if body != "" {
		ct = "application/json"
	}
	return e.do(t, method, path, token, reason, ct, []byte(body), nil)
}

func (e *vpiEnv) create(t *testing.T, org, venue uuid.UUID, token string, hdr map[string]string, files ...vpiFile) vpiResp {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		fw, err := mw.CreateFormFile("files", f.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(f.data)
	}
	_ = mw.WriteField("channel", "event_center_web")
	_ = mw.Close()
	return e.do(t, http.MethodPost, fmt.Sprintf("/v1/organizations/%s/venues/%s/plan-intakes", org, venue), token, "",
		mw.FormDataContentType(), buf.Bytes(), hdr)
}

func orgIntakePath(org uuid.UUID, id string) string {
	return "/v1/organizations/" + org.String() + "/plan-intakes/" + id
}

func vpiPlanSVG(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "domain", "seating", "testdata", "Palac_Akropolis_GA.svg"))
	if err != nil {
		t.Fatalf("read plan svg: %v", err)
	}
	return raw
}

// vpiPDF is a one-page PDF padded past the global 1 MiB body limit.
func vpiPDF() []byte {
	head := "%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n3 0 obj << /Type /Page /Parent 2 0 R >> endobj\n"
	return []byte(head + "%" + strings.Repeat("x", 1_500_000) + "\n%%EOF\n")
}

// vpiJPEG is a small JPEG with an EXIF block that holds orientation 6 and a
// marker standing for a GPS position.
func vpiJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for x := 0; x < 32; x++ {
		img.Set(x, 4, color.RGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	plain := buf.Bytes()
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00GPSSECRET-41.38N-2.17E")
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	out := append([]byte{}, plain[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, plain[2:]...)
}

func (e *vpiEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func sourceOf(t *testing.T, in map[string]any, kind string) map[string]any {
	t.Helper()
	for _, s := range in["sources"].([]any) {
		m := s.(map[string]any)
		if m["kind"] == kind {
			return m
		}
	}
	t.Fatalf("no %s source in %v", kind, in["sources"])
	return nil
}

// (a) A PDF and a photo go to an administrator, who draws the plan and
// returns it; the organizer approves it into a private, unverified plan.
func TestVenuePlanIntake_FilesToAdministratorAndApproval(t *testing.T) {
	e := newVPIEnv(t)
	if _, err := billing.EnsureAccount(context.Background(), e.q, e.orgA, "ES"); err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	res := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.pdf", vpiPDF()}, vpiFile{"balcony.jpg", vpiJPEG(t)})
	if res.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	in := res.intake(t)
	id := in["id"].(string)
	if in["state"] != "operator" || in["escalation_reason"] != "no_generator" || in["venue_name"] != "VPI Hall A" {
		t.Fatalf("new intake: %v", in)
	}
	jpg := sourceOf(t, in, "jpeg")
	sourceOf(t, in, "pdf")

	// The organization downloads its photo: an attachment, sandboxed, no EXIF but the orientation.
	got := e.do(t, http.MethodGet, jpg["download_path"].(string), e.ownerA, "", "", nil, nil)
	if got.status != http.StatusOK {
		t.Fatalf("download: %d %s", got.status, got.body)
	}
	if cd := got.header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition %q", cd)
	}
	if csp := got.header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP %q", csp)
	}
	if got.header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(got.header.Get("Cache-Control"), "no-store") {
		t.Errorf("headers %v", got.header)
	}
	if bytes.Contains(got.body, []byte("GPSSECRET")) {
		t.Error("the stored JPEG still carries the EXIF block")
	}
	if !bytes.Contains(got.body, []byte{0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x06}) {
		t.Error("the stored JPEG lost its orientation")
	}
	if _, err := jpeg.Decode(bytes.NewReader(got.body)); err != nil {
		t.Errorf("the stored JPEG does not decode: %v", err)
	}
	// Another organization and the admin without a reason do not get it.
	other := strings.Replace(jpg["download_path"].(string), e.orgA.String(), e.orgB.String(), 1)
	if r := e.do(t, http.MethodGet, other, e.ownerB, "", "", nil, nil); r.status != http.StatusNotFound {
		t.Errorf("org B download via its own path: %d", r.status)
	}
	if r := e.do(t, http.MethodGet, jpg["download_path"].(string), e.ownerB, "", "", nil, nil); r.status != http.StatusForbidden {
		t.Errorf("org B download via org A's path: %d", r.status)
	}
	adminSrc := "/v1/admin/plan-intakes/" + id + "/sources/" + jpg["id"].(string)
	if r := e.do(t, http.MethodGet, adminSrc, e.super, "", "", nil, nil); r.status != http.StatusBadRequest {
		t.Errorf("admin download without a reason: %d", r.status)
	}
	if r := e.do(t, http.MethodGet, adminSrc, e.super, "check the plan", "", nil, nil); r.status != http.StatusOK || !bytes.Equal(r.body, got.body) {
		t.Errorf("admin download: %d", r.status)
	}
	// The administrator's download is in the audit log: reason, file id and
	// sha256, never the content. The organization's own download is not.
	if n := e.count(t, `SELECT count(*) FROM audit_events a JOIN venue_plan_intake_sources s ON s.id::text = a.metadata->>'source_id'
		WHERE a.action = 'v1.venue_plan_intake.source_download' AND a.resource_id = $1 AND a.metadata->>'reason' = 'check the plan'
		  AND a.metadata->>'sha256' = s.sha256 AND s.id = $2 AND a.metadata::text NOT LIKE '%GPSSECRET%'`, id, jpg["id"].(string)); n != 1 {
		t.Errorf("source download audit rows: %d, want 1", n)
	}

	// The administrator takes it, draws the plan, returns it.
	adm := "/v1/admin/plan-intakes/" + id
	if r := e.jsonDo(t, http.MethodPost, adm+"/claim", e.super, "taking it", ""); r.status != http.StatusOK || r.intake(t)["operator_claimed"] != true {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	draftBody, _ := json.Marshal(map[string]any{"svg": string(vpiPlanSVG(t)), "note": "drawn by hand"})
	r := e.jsonDo(t, http.MethodPost, adm+"/drafts", e.super, "drawn", string(draftBody))
	if r.status != http.StatusCreated {
		t.Fatalf("draft: %d %s", r.status, r.body)
	}
	if c := r.intake(t); c["state"] != "operator" || c["current_draft"] != float64(1) {
		t.Fatalf("after draft: %v", c)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1`, id) != 0 {
		t.Error("a draft upload wrote a notice")
	}
	r = e.jsonDo(t, http.MethodPost, adm+"/return", e.super, "ready", `{"note":"Please check the balcony"}`)
	if r.status != http.StatusOK || r.intake(t)["state"] != "review" {
		t.Fatalf("return: %d %s", r.status, r.body)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'returned'`, id) != 1 {
		t.Error("return wrote no returned notice")
	}

	// An agent may not approve; the owner does.
	if r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.agentA, "", `{"draft":1}`); r.status != http.StatusForbidden {
		t.Errorf("agent approve: %d %s", r.status, r.body)
	}
	r = e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.ownerA, "", `{"draft":1}`)
	if r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	card := r.intake(t)
	planID, _ := card["plan_id"].(string)
	if card["state"] != "approved" || planID == "" || card["plan_version"] != float64(1) {
		t.Fatalf("approved card: %v", card)
	}
	var status, visibility, vstate, name string
	var svgMedia *uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `
		SELECT p.status, p.visibility, COALESCE(p.verification_state, ''), p.name, v.svg_asset_media_id
		FROM seating_plans p JOIN seating_plan_versions v ON v.id = p.current_version_id AND v.version_number = 1
		WHERE p.id = $1`, planID).Scan(&status, &visibility, &vstate, &name, &svgMedia); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if status != "active" || visibility != "private" || vstate != "unverified" || name != "VPI Hall A" || svgMedia == nil {
		t.Errorf("plan %s/%s/%s/%s/%v", status, visibility, vstate, name, svgMedia)
	}
	obj, err := e.srv.media.GetByID(context.Background(), *svgMedia)
	if err != nil || obj.OwnerType != "seating_plan_svg" {
		t.Fatalf("plan svg media: %v %v", obj, err)
	}
	stored, err := e.srv.media.Storage().Get(context.Background(), obj.StorageKey)
	if err != nil {
		t.Fatalf("plan svg bytes: %v", err)
	}
	svg, _ := io.ReadAll(stored.Body)
	_ = stored.Body.Close()
	if _, _, errs := seating.ImportSVG(svg); len(errs) > 0 {
		t.Errorf("the stored plan SVG does not import: %v", errs)
	}
	if n := e.count(t, `SELECT count(*) FROM billable_events WHERE kind = 'venue_plan_intake' AND subject_id = $1`, id); n != 1 {
		t.Errorf("billable events: %d", n)
	}
	for _, action := range []string{"create", "claim", "draft", "return", "approve"} {
		if e.count(t, `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, "v1.venue_plan_intake."+action, id) != 1 {
			t.Errorf("no audit row v1.venue_plan_intake.%s", action)
		}
	}
	// Approving twice is a wrong state, not a second plan.
	if r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.ownerA, "", `{"draft":1}`); r.status != http.StatusConflict {
		t.Errorf("second approve: %d", r.status)
	}
}

// (b) An SVG that does not import is refused before anything is stored.
func TestVenuePlanIntake_BadAdminDraftStoresNothing(t *testing.T) {
	e := newVPIEnv(t)
	id := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.pdf", vpiPDF()}).intake(t)["id"].(string)
	adm := "/v1/admin/plan-intakes/" + id
	if r := e.jsonDo(t, http.MethodPost, adm+"/claim", e.super, "taking it", ""); r.status != http.StatusOK {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	mediaBefore := e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA)
	bad, _ := json.Marshal(map[string]any{"svg": `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><circle r="1"/></svg>`})
	r := e.jsonDo(t, http.MethodPost, adm+"/drafts", e.super, "drawn", string(bad))
	if r.status != http.StatusUnprocessableEntity || r.code() != "venue_plan_intake.draft_validation_failed" {
		t.Fatalf("bad draft: %d %s", r.status, r.body)
	}
	details, _ := r.json(t)["error"].(map[string]any)["details"].(map[string]any)
	if list, _ := details["errors"].([]any); len(list) == 0 {
		t.Errorf("no validation list: %s", r.body)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1`, id) != 0 ||
		e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA) != mediaBefore {
		t.Error("a refused draft left rows behind")
	}
}

// (c) A source SVG that imports cleanly is draft 1, in review.
func TestVenuePlanIntake_CleanSourceSVGStartsInReview(t *testing.T) {
	e := newVPIEnv(t)
	r := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)})
	if r.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	in := r.intake(t)
	draft, _ := in["draft"].(map[string]any)
	if in["state"] != "review" || in["current_draft"] != float64(1) || draft == nil || draft["author"] != "source_svg" {
		t.Fatalf("intake: %v", in)
	}
	// The test plan has seats and a standing area.
	if s := draft["summary"].(map[string]any); s["plan_type"] != "mixed" || s["seats"] != float64(90) {
		t.Errorf("summary %v", s)
	}
	got := e.do(t, http.MethodGet, draft["svg_path"].(string), e.ownerA, "", "", nil, nil)
	if got.status != http.StatusOK || got.header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(got.header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("draft svg: %d %v", got.status, got.header)
	}
}

// (d) Another organization's member sees nothing and moves nothing.
func TestVenuePlanIntake_OtherOrganizationGetsNotFound(t *testing.T) {
	e := newVPIEnv(t)
	in := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}).intake(t)
	id := in["id"].(string)
	src := in["sources"].([]any)[0].(map[string]any)["id"].(string)
	before := e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND state = 'review' AND current_draft = 1`, id)
	viaB := orgIntakePath(e.orgB, id)
	for _, p := range []string{viaB, viaB + "/sources/" + src, viaB + "/drafts/1/svg"} {
		if r := e.do(t, http.MethodGet, p, e.ownerB, "", "", nil, nil); r.status != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, r.status)
		}
	}
	posts := map[string]string{
		"/answers": `{"draft":1,"answers":[{"question_id":"` + uuid.NewString() + `","answer":"x"}]}`, "/corrections-text": `{"draft":1,"text":"fix"}`,
		"/escalate": `{}`, "/cancel": ``, "/approve": `{"draft":1}`,
	}
	for suffix, body := range posts {
		if r := e.jsonDo(t, http.MethodPost, viaB+suffix, e.ownerB, "", body); r.status != http.StatusNotFound {
			t.Errorf("POST %s via B: %d %s", suffix, r.status, r.body)
		}
		if r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+suffix, e.ownerB, "", body); r.status != http.StatusForbidden {
			t.Errorf("POST %s via A: %d %s", suffix, r.status, r.body)
		}
	}
	// A's venue is not verified: B cannot open an intake on it either.
	if r := e.create(t, e.orgB, e.venueA, e.ownerB, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}); r.status != http.StatusNotFound {
		t.Errorf("create on a foreign venue: %d", r.status)
	}
	after := e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND state = 'review' AND current_draft = 1`, id)
	if before != 1 || after != 1 || e.count(t, `SELECT count(*) FROM venue_plan_corrections WHERE intake_id = $1`, id) != 0 {
		t.Error("org B moved org A's intake")
	}
}

// (e) An organization API key with the seating scopes is refused everywhere.
func TestVenuePlanIntake_APIKeyIsRefused(t *testing.T) {
	e := newVPIEnv(t)
	in := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}).intake(t)
	creator := e.user(t, "keyowner")
	_, key, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(e.q), apikeys.IssueInput{
		OrgID: e.orgA, Name: "vpi-" + uuid.NewString()[:6], Scopes: []string{"seating_plan.create", "seating_plan.read"}, CreatedBy: creator,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	id := in["id"].(string)
	src := in["sources"].([]any)[0].(map[string]any)["download_path"].(string)
	checks := []vpiResp{
		e.create(t, e.orgA, e.venueA, key, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}),
		e.do(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/plan-intakes", key, "", "", nil, nil),
		e.do(t, http.MethodGet, orgIntakePath(e.orgA, id), key, "", "", nil, nil),
		e.do(t, http.MethodGet, src, key, "", "", nil, nil),
	}
	for i, r := range checks {
		if r.status != http.StatusForbidden || r.code() != "venue_plan_intake.api_key_forbidden" {
			t.Errorf("check %d: %d %s", i, r.status, r.body)
		}
	}
	// The POST actions are refused too, and nothing changes.
	posts := map[string]string{
		"/answers":          `{"draft":1,"answers":[{"question_id":"` + uuid.NewString() + `","answer":"x"}]}`,
		"/corrections-text": `{"draft":1,"text":"fix"}`, "/escalate": `{}`, "/cancel": ``, "/approve": `{"draft":1}`,
	}
	for suffix, body := range posts {
		if r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+suffix, key, "", body); r.status != http.StatusForbidden || r.code() != "venue_plan_intake.api_key_forbidden" {
			t.Errorf("POST %s with an API key: %d %s", suffix, r.status, r.body)
		}
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.orgA) != 1 {
		t.Error("the API key created an intake")
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND state = 'review' AND current_draft = 1 AND plan_id IS NULL`, id) != 1 ||
		e.count(t, `SELECT count(*) FROM venue_plan_corrections WHERE intake_id = $1`, id) != 0 ||
		e.count(t, `SELECT count(*) FROM seating_plans WHERE owner_org_id = $1`, e.orgA) != 0 {
		t.Error("an API key changed the intake")
	}
}

// Two approve requests of the same intake at once make exactly one plan; the
// other answers 409. The organization has no billing account: the approval
// succeeds and no billable event is written.
func TestVenuePlanIntake_ConcurrentApproveMakesOnePlan(t *testing.T) {
	e := newVPIEnv(t)
	id := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}).intake(t)["id"].(string)
	url := e.ts.URL + orgIntakePath(e.orgA, id) + "/approve"
	statuses := make(chan int, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(`{"draft":1}`))
			if err != nil {
				statuses <- 0
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+e.ownerA)
			resp, err := e.ts.Client().Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	got := map[int]int{}
	for i := 0; i < 2; i++ {
		got[<-statuses]++
	}
	if got[http.StatusOK] != 1 || got[http.StatusConflict] != 1 {
		t.Fatalf("statuses %v, want one 200 and one 409", got)
	}
	if n := e.count(t, `SELECT count(*) FROM seating_plans WHERE owner_org_id = $1`, e.orgA); n != 1 {
		t.Errorf("plans %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1 AND owner_type = 'seating_plan_svg'`, e.orgA); n != 1 {
		t.Errorf("plan SVG media %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM billable_events WHERE subject_id = $1`, id); n != 0 {
		t.Errorf("billable events %d for an organization without a billing account", n)
	}
}

// Approve for an organization without a billing account succeeds and writes
// no billable event.
func TestVenuePlanIntake_ApproveWithoutBillingAccount(t *testing.T) {
	e := newVPIEnv(t)
	id := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}).intake(t)["id"].(string)
	if n := e.count(t, `SELECT count(*) FROM billing_accounts WHERE org_id = $1`, e.orgA); n != 0 {
		t.Fatalf("the fixture organization has %d billing accounts", n)
	}
	r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.ownerA, "", `{"draft":1}`)
	if r.status != http.StatusOK || r.intake(t)["state"] != "approved" {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	if n := e.count(t, `SELECT count(*) FROM billable_events WHERE subject_id = $1`, id); n != 0 {
		t.Errorf("billable events %d, want 0", n)
	}
	if n := e.count(t, `SELECT count(*) FROM seating_plans WHERE owner_org_id = $1 AND status = 'active'`, e.orgA); n != 1 {
		t.Errorf("plans %d, want 1", n)
	}
}

// (f) The admin routes: an organization owner is refused, a superadmin needs
// X-Admin-Reason for every write and the source download.
func TestVenuePlanIntake_AdminGate(t *testing.T) {
	e := newVPIEnv(t)
	in := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.pdf", vpiPDF()}).intake(t)
	id := in["id"].(string)
	adm := "/v1/admin/plan-intakes/" + id
	src := adm + "/sources/" + in["sources"].([]any)[0].(map[string]any)["id"].(string)
	if r := e.do(t, http.MethodGet, "/v1/admin/plan-intakes", e.ownerA, "", "", nil, nil); r.status != http.StatusForbidden {
		t.Errorf("owner list: %d", r.status)
	}
	if r := e.jsonDo(t, http.MethodPost, adm+"/claim", e.ownerA, "x", ""); r.status != http.StatusForbidden {
		t.Errorf("owner claim: %d", r.status)
	}
	writes := map[string]string{"/claim": ``, "/drafts": `{"svg":"<svg/>"}`, "/return": `{}`, "/approve": `{"draft":1}`, "/reject": `{"reason":"no"}`}
	for suffix, body := range writes {
		if r := e.jsonDo(t, http.MethodPost, adm+suffix, e.super, "", body); r.status != http.StatusBadRequest {
			t.Errorf("superadmin %s without a reason: %d %s", suffix, r.status, r.body)
		}
	}
	if r := e.do(t, http.MethodGet, src, e.super, "", "", nil, nil); r.status != http.StatusBadRequest {
		t.Errorf("source without a reason: %d", r.status)
	}
	r := e.do(t, http.MethodGet, "/v1/admin/plan-intakes?unclaimed=true&org_id="+e.orgA.String(), e.super, "", "", nil, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), id) {
		t.Errorf("admin list: %d %s", r.status, r.body)
	}
	if r := e.do(t, http.MethodGet, adm, e.super, "", "", nil, nil); r.status != http.StatusOK || r.intake(t)["org_name"] == nil {
		t.Errorf("admin card: %d %s", r.status, r.body)
	}
	// Every write but claim needs the caller's claim: an intake nobody holds
	// answers 409 not_claimed and nothing changes.
	mediaBefore := e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA)
	draftBody, _ := json.Marshal(map[string]any{"svg": string(vpiPlanSVG(t))})
	unclaimed := map[string]string{"/drafts": string(draftBody), "/return": `{}`, "/approve": `{"draft":1}`, "/reject": `{"reason":"no"}`}
	for suffix, body := range unclaimed {
		if r := e.jsonDo(t, http.MethodPost, adm+suffix, e.super, "check", body); r.status != http.StatusConflict || r.code() != "venue_plan_intake.not_claimed" {
			t.Errorf("unclaimed %s: %d %s", suffix, r.status, r.body)
		}
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND state = 'operator' AND current_draft IS NULL`, id) != 1 ||
		e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA) != mediaBefore {
		t.Error("a write on an unclaimed intake changed it")
	}
	// Another administrator holds it: 409 claimed_by_other.
	otherID := e.user(t, "super2")
	e.exec(t, `INSERT INTO user_roles (user_id, role_id, org_id)
	           SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, otherID)
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, otherID)
	})
	if r := e.jsonDo(t, http.MethodPost, adm+"/claim", e.token(t, otherID), "mine", ""); r.status != http.StatusOK {
		t.Fatalf("claim by another administrator: %d %s", r.status, r.body)
	}
	if r := e.jsonDo(t, http.MethodPost, adm+"/reject", e.super, "check", `{"reason":"no"}`); r.status != http.StatusConflict || r.code() != "venue_plan_intake.claimed_by_other" {
		t.Errorf("reject of another administrator's intake: %d %s", r.status, r.body)
	}
	if r := e.jsonDo(t, http.MethodPost, adm+"/claim", e.super, "taking over", `{"force":true}`); r.status != http.StatusOK {
		t.Fatalf("forced claim: %d %s", r.status, r.body)
	}
	// Rejecting needs the intake and writes the notice.
	if r := e.jsonDo(t, http.MethodPost, adm+"/reject", e.super, "unreadable", `{"reason":"The scan is too blurry"}`); r.status != http.StatusOK || r.intake(t)["state"] != "rejected" {
		t.Errorf("reject: %d %s", r.status, r.body)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = $1 AND kind = 'rejected'`, id) != 1 {
		t.Error("reject wrote no notice")
	}
}

// (g) The same Idempotency-Key returns the same intake and stores no file.
func TestVenuePlanIntake_IdempotentCreate(t *testing.T) {
	e := newVPIEnv(t)
	hdr := map[string]string{"Idempotency-Key": "vpi-" + uuid.NewString()}
	first := e.create(t, e.orgA, e.venueA, e.ownerA, hdr, vpiFile{"plan.svg", vpiPlanSVG(t)})
	if first.status != http.StatusAccepted {
		t.Fatalf("first: %d %s", first.status, first.body)
	}
	media := e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA)
	second := e.create(t, e.orgA, e.venueA, e.ownerA, hdr, vpiFile{"plan.svg", vpiPlanSVG(t)})
	if second.status != http.StatusOK || second.intake(t)["id"] != first.intake(t)["id"] {
		t.Fatalf("second: %d %s", second.status, second.body)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.orgA) != 1 ||
		e.count(t, `SELECT count(*) FROM media_objects WHERE org_id = $1`, e.orgA) != media {
		t.Error("the repeated create stored something")
	}
}

// (h) Answers on a stale draft are 409; a text correction is stored and goes
// to an administrator.
func TestVenuePlanIntake_AnswersAndTextCorrection(t *testing.T) {
	e := newVPIEnv(t)
	id := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}).intake(t)["id"].(string)
	p := orgIntakePath(e.orgA, id)
	r := e.jsonDo(t, http.MethodPost, p+"/answers", e.ownerA, "", `{"draft":2,"answers":[{"question_id":"`+uuid.NewString()+`","answer":"x"}]}`)
	if r.status != http.StatusConflict || r.code() != "venue_plan_intake.draft_stale" {
		t.Fatalf("stale answers: %d %s", r.status, r.body)
	}
	r = e.jsonDo(t, http.MethodPost, p+"/corrections-text", e.ownerA, "", `{"draft":1,"text":"Row 5 has 22 seats"}`)
	if r.status != http.StatusOK {
		t.Fatalf("correction: %d %s", r.status, r.body)
	}
	if c := r.intake(t); c["state"] != "operator" || c["escalation_reason"] != "organizer_text" {
		t.Errorf("after correction: %v", c)
	}
	if e.count(t, `SELECT count(*) FROM venue_plan_corrections WHERE intake_id = $1 AND kind = 'organizer_text'
	               AND status = 'pending' AND text = 'Row 5 has 22 seats'`, id) != 1 {
		t.Error("the correction was not stored")
	}
	if e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.venue_plan_intake.correction_text' AND resource_id = $1
	               AND metadata::text NOT LIKE '%22 seats%'`, id) != 1 {
		t.Error("no audit row, or it carries the organizer's text")
	}
	// A second correction is a wrong state now.
	if r := e.jsonDo(t, http.MethodPost, p+"/corrections-text", e.ownerA, "", `{"draft":1,"text":"again"}`); r.status != http.StatusConflict {
		t.Errorf("correction in operator: %d", r.status)
	}
}

// (i) The media routes never serve or delete a hall plan source.
func TestVenuePlanIntake_MediaRoutesRefuseSources(t *testing.T) {
	e := newVPIEnv(t)
	id := e.create(t, e.orgA, e.venueA, e.ownerA, nil, vpiFile{"plan.svg", vpiPlanSVG(t)}).intake(t)["id"].(string)
	var mediaID uuid.UUID
	var key string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `
		SELECT m.id, m.storage_key FROM venue_plan_intake_sources s JOIN media_objects m ON m.id = s.media_id
		WHERE s.intake_id = $1`, id).Scan(&mediaID, &key); err != nil {
		t.Fatalf("source media: %v", err)
	}
	if r := e.do(t, http.MethodGet, "/v1/media/"+mediaID.String(), e.ownerA, "", "", nil, nil); r.status != http.StatusNotFound {
		t.Errorf("GET /v1/media: %d %s", r.status, r.body)
	}
	if r := e.do(t, http.MethodDelete, "/v1/media/"+mediaID.String(), e.ownerA, "", "", nil, nil); r.status != http.StatusConflict {
		t.Errorf("DELETE /v1/media: %d %s", r.status, r.body)
	}
	signed, err := e.srv.media.SignedURL(mediaID, key, time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if r := e.do(t, http.MethodGet, signed, "", "", "", nil, nil); r.status != http.StatusNotFound {
		t.Errorf("GET a signed media-files link: %d %s", r.status, r.body)
	}
	if e.count(t, `SELECT count(*) FROM media_objects WHERE id = $1 AND deleted_at IS NULL`, mediaID) != 1 {
		t.Error("the source was deleted")
	}
}

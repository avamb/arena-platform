//go:build integration

// legal_terms_integration_test.go — P3a: the organizer's terms of sale
// through the real router.
//
//   - GET /v1/public/pages/{org_slug}/terms renders the published template
//     with the seller's data, stores one rendering per data (a second read
//     answers the same rendering_id), falls back to English, follows the
//     event's refund policy and promoter, resolves a promoter slug, answers
//     the hosted page's 404 for every miss, and serves a script-free HTML
//     page with format=html.
//
//   - The database keeps a published template and a rendering frozen.
//
//   - The superadmin's template routes: draft, 422 for an unknown
//     placeholder, publish retires the previous version, 409 for a second
//     publish, 403 for an owner, legal.manage is never an API key scope.
//
//   - The checkout consent names the rendering behind its terms link
//     (assertConsentNamesTheRendering, called from the P2 test).
//
//     go test -tags integration ./apps/backend/internal/platform/httpserver/ -run 'TestLegal|TestCheckoutConsent'
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type termsWire struct {
	DocumentVersion int    `json:"document_version"`
	RenderingID     string `json:"rendering_id"`
	Locale          string `json:"locale"`
	HTML            string `json:"html"`
	SHA256          string `json:"sha256"`
	RefundMode      string `json:"refund_mode"`
	SellerLegalName string `json:"seller_legal_name"`
}

func getTerms(t *testing.T, srv *Server, path string, header map[string]string) (*httptest.ResponseRecorder, termsWire) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var w termsWire
	if rec.Code == http.StatusOK && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
			t.Fatalf("decode terms: %v %s", err, rec.Body.String())
		}
	}
	return rec, w
}

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestLegalTerms_PublicPage(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildConsentServer(t, pool)
	f := newConsentFixture(t, ctx, pool, "terms")
	defer f.close()
	f.completeProfile(t, ctx)
	suffix := f.orgID.String()[:8]
	orgSlug := "hoco-" + suffix
	promoterSlug := "terms-prom-" + suffix
	promoterID := uuid.New()
	if _, err := pool.Exec(ctx, `UPDATE events SET slug = 'legal-show', refund_policy_mode = 'no_refund' WHERE id = $1`, f.eventID); err != nil {
		t.Fatalf("event slug: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = jsonb_set(settings, '{hosted_page,public_contact,phone}', '"+34 600 111 222"') WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("phone: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO org_promoters (id, org_id, name, legal_id, slug) VALUES ($1, $2, 'Kolybel Productions', 'B-777', $3)`,
		promoterID, f.orgID, promoterSlug); err != nil {
		t.Fatalf("promoter: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM event_promoters WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM org_promoters WHERE org_id = $1`, f.orgID)
	}()
	base := "/v1/public/pages/" + orgSlug + "/terms"

	// ── the organization's terms, in Spanish, stored once ─────────────────
	rec, es := getTerms(t, srv, base+"?locale=es-ES", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("terms: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"DRAFT — awaiting legal review", "Consent Teatro S.L.", "Calle Mayor 1, 28001 Madrid", "ESB12345678", "+34 600 111 222", "No right of withdrawal"} {
		if !strings.Contains(es.HTML, want) {
			t.Errorf("terms miss %q:\n%s", want, es.HTML)
		}
	}
	if strings.Contains(es.HTML, "tickets@consent.example") || strings.Contains(es.HTML, "<script") {
		t.Fatalf("the terms publish the e-mail or a script: %s", es.HTML)
	}
	if es.Locale != "es" || es.RefundMode != "refund_by_law" || es.SellerLegalName != "Consent Teatro S.L." ||
		es.SHA256 != shaHex(es.HTML) || es.DocumentVersion < 1 {
		t.Fatalf("terms = %+v", es)
	}
	if rec.Header().Get("Cache-Control") == "" || strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Language") {
		t.Errorf("headers: cache %q vary %q", rec.Header().Get("Cache-Control"), rec.Header().Values("Vary"))
	}
	_, again := getTerms(t, srv, base+"?locale=es", nil)
	if again.RenderingID != es.RenderingID {
		t.Fatalf("a second read made another rendering: %s vs %s", again.RenderingID, es.RenderingID)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM legal_renderings WHERE org_id = $1`, f.orgID).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("renderings stored = %d (%v)", stored, err)
	}

	// ── language: an unknown one is English, Accept-Language is read ─────
	if _, w := getTerms(t, srv, base+"?locale=it", nil); w.Locale != "en" {
		t.Errorf("it -> %q", w.Locale)
	}
	rec, he := getTerms(t, srv, base, map[string]string{"Accept-Language": "he-IL,he;q=0.9"})
	vary := strings.Join(rec.Header().Values("Vary"), ",")
	if he.Locale != "he" || !strings.Contains(he.HTML, `dir="rtl"`) || !strings.Contains(vary, "Accept-Language") || !strings.Contains(vary, "Origin") {
		t.Errorf("he: %q vary %q", he.Locale, vary)
	}

	// ── the event's refund policy wins; its promoter is named ────────────
	if _, err := pool.Exec(ctx, `INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, f.eventID, f.orgID, promoterID); err != nil {
		t.Fatalf("link promoter: %v", err)
	}
	_, byID := getTerms(t, srv, base+"?locale=en&event_id="+f.eventID.String(), nil)
	_, ev := getTerms(t, srv, base+"?locale=en&event_slug=LEGAL-SHOW", nil)
	if byID.RenderingID != ev.RenderingID {
		t.Errorf("event by id and by slug render differently: %s %s", byID.RenderingID, ev.RenderingID)
	}
	if ev.RefundMode != "no_refund" || !strings.Contains(ev.HTML, "Kolybel Productions (B-777)") ||
		!strings.Contains(ev.HTML, "You cannot return a ticket") || ev.RenderingID == es.RenderingID {
		t.Fatalf("event terms: %+v", ev)
	}

	// ── a promoter slug: its event, never another one ────────────────────
	if rec, w := getTerms(t, srv, "/v1/public/pages/"+promoterSlug+"/terms?event_slug=legal-show", nil); rec.Code != http.StatusOK || !strings.Contains(w.HTML, "Kolybel") {
		t.Fatalf("promoter page terms: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := pool.Exec(ctx, `DELETE FROM event_promoters WHERE event_id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := getTerms(t, srv, "/v1/public/pages/"+promoterSlug+"/terms?event_slug=legal-show", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an event not linked to the promoter: %d", rec.Code)
	}

	// ── every miss is the hosted page's 404 ──────────────────────────────
	other := newFreeBuyerFieldsFixture(t, ctx, pool, "terms-other", false, false)
	defer other.cleanup()
	for _, path := range []string{
		"/v1/public/pages/no-such-slug-" + suffix + "/terms",
		base + "?event_slug=no-such-event",
		base + "?event_id=" + uuid.NewString(),
		base + "?event_id=not-a-uuid",
		base + "?feed_token=no-such-token",
		base + "?feed_token=" + url.QueryEscape(other.feedToken),
	} {
		rec, _ := getTerms(t, srv, path, nil)
		if rec.Code != http.StatusNotFound || buyerFieldsErrorCode(t, rec.Body.Bytes()) != "page.not_found" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// ── the HTML page carries no script ──────────────────────────────────
	rec, _ = getTerms(t, srv, base+"?locale=es&format=html&feed_token="+url.QueryEscape(f.feedToken), nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") ||
		strings.Contains(rec.Body.String(), "<script") || !strings.Contains(rec.Body.String(), es.SHA256) {
		t.Fatalf("html page: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}

	// ── the database keeps evidence frozen ───────────────────────────────
	if _, err := pool.Exec(ctx, `UPDATE legal_renderings SET body_html = 'x' WHERE id = $1`, es.RenderingID); err == nil {
		t.Error("a rendering was updated")
	}
	var docID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM legal_documents WHERE kind = 'sale_terms' AND locale = 'es' AND status = 'published'`).Scan(&docID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE legal_documents SET body_template = 'x' WHERE id = $1`, docID); err == nil {
		t.Error("a published template was changed")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM legal_documents WHERE id = $1`, docID); err == nil {
		t.Error("a published template was deleted")
	}
}

// assertConsentNamesTheRendering checks a checkout consent against the
// organizer terms behind the link the feed showed (P3a).
func assertConsentNamesTheRendering(t *testing.T, ctx context.Context, srv *Server, pool *pgxpool.Pool, c consentRowWire, termsURL string) {
	t.Helper()
	if c.LegalRenderingID == nil || c.TermsSHA256 == nil {
		t.Fatalf("the consent names no rendering: %+v", c)
	}
	var html, sum, locale string
	if err := pool.QueryRow(ctx, `SELECT body_html, body_sha256, locale FROM legal_renderings WHERE id = $1`, *c.LegalRenderingID).Scan(&html, &sum, &locale); err != nil {
		t.Fatalf("read rendering: %v", err)
	}
	if sum != *c.TermsSHA256 || sum != shaHex(html) || locale != "es" {
		t.Fatalf("rendering sha %s / %s, locale %s", sum, *c.TermsSHA256, locale)
	}
	// The link the buyer saw opens that very rendering in their language.
	u, err := url.Parse(termsURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Del("format")
	q.Set("locale", "es")
	_, w := getTerms(t, srv, u.Path+"?"+q.Encode(), nil)
	if w.RenderingID != *c.LegalRenderingID {
		t.Fatalf("the terms link renders %s, the consent names %s", w.RenderingID, *c.LegalRenderingID)
	}
}

func TestLegalDocuments_AdminRoutes(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildConsentServer(t, pool)
	q := gen.New(pool)

	admin, err := q.InsertUser(ctx, "legal-admin-"+uuid.NewString()[:8]+"@arena-integration.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	owner := newBotInviteFixture(t, pool)
	var created []string
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM audit_events WHERE resource_type = 'legal_document' AND resource_id = ANY($1)`, created)
		_, _ = pool.Exec(bg, `DELETE FROM legal_documents WHERE status = 'draft' AND id::text = ANY($1)`, created)
		_, _ = pool.Exec(bg, `DELETE FROM user_roles WHERE user_id = $1`, admin.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, admin.ID)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, admin.ID); err != nil {
		t.Fatalf("grant superadmin: %v", err)
	}
	adminToken, _, err := auth.IssueJWT(botTestJWTSecret, admin.ID, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, reason, method, path, body string) (int, []byte) {
		return consentBearer(t, srv, token, reason, method, path, body)
	}
	decode := func(raw []byte) struct {
		ID, Status, Locale, BodyTemplate string
		Version                          int
	} {
		var d struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			Locale       string `json:"locale"`
			BodyTemplate string `json:"body_template"`
			Version      int    `json:"version"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("decode document: %v %s", err, raw)
		}
		return struct {
			ID, Status, Locale, BodyTemplate string
			Version                          int
		}{d.ID, d.Status, d.Locale, d.BodyTemplate, d.Version}
	}

	// The current German version, to put its text back at the end.
	var prevBody string
	var prevVersion int
	if err := pool.QueryRow(ctx, `SELECT body_template, version FROM legal_documents WHERE kind = 'sale_terms' AND locale = 'de' AND status = 'published'`).Scan(&prevBody, &prevVersion); err != nil {
		t.Fatalf("current de version: %v", err)
	}

	// ── an owner of an organization is refused ───────────────────────────
	ownerToken, _, err := auth.IssueJWT(botTestJWTSecret, owner.ownerID, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := call(ownerToken, "x", http.MethodGet, "/v1/admin/legal-documents", ""); code != http.StatusForbidden {
		t.Fatalf("owner list: %d %s", code, raw)
	}
	// ── and legal.manage is never an API key scope ───────────────────────
	if _, _, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: owner.orgID, Name: "legal-" + uuid.NewString()[:6], Scopes: []string{"legal.manage"}, CreatedBy: owner.ownerID,
	}); !errors.Is(err, apikeys.ErrForbiddenScope) {
		t.Fatalf("legal.manage key: %v", err)
	}

	// ── list ─────────────────────────────────────────────────────────────
	code, raw := call(adminToken, "", http.MethodGet, "/v1/admin/legal-documents?locale=de", "")
	if code != http.StatusOK || !strings.Contains(string(raw), `"seller.legal_name"`) {
		t.Fatalf("list: %d %s", code, raw)
	}

	// ── an unknown placeholder is refused with its line ──────────────────
	code, raw = call(adminToken, "P3a test", http.MethodPost, "/v1/admin/legal-documents",
		`{"kind":"sale_terms","locale":"de","body_template":"ok\n{{seller.iban}}"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "line 2") {
		t.Fatalf("unknown placeholder: %d %s", code, raw)
	}
	if code, _ := call(adminToken, "", http.MethodPost, "/v1/admin/legal-documents",
		`{"kind":"sale_terms","locale":"de","body_template":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d", code)
	}

	// ── draft, publish, second publish refused ───────────────────────────
	code, raw = call(adminToken, "P3a test", http.MethodPost, "/v1/admin/legal-documents",
		`{"kind":"sale_terms","locale":"de","body_template":"## Test\n\nSeller {{seller.legal_name}}."}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	draft := decode(raw)
	created = append(created, draft.ID)
	if draft.Status != "draft" || draft.Version != prevVersion+1 {
		t.Fatalf("draft = %+v (previous version %d)", draft, prevVersion)
	}
	code, raw = call(adminToken, "P3a test", http.MethodPost, "/v1/admin/legal-documents/"+draft.ID+"/publish", "")
	if code != http.StatusOK || decode(raw).Status != "published" {
		t.Fatalf("publish: %d %s", code, raw)
	}
	var publishedDE, retiredPrev string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT id::text FROM legal_documents WHERE kind = 'sale_terms' AND locale = 'de' AND status = 'published'),
		(SELECT status FROM legal_documents WHERE kind = 'sale_terms' AND locale = 'de' AND version = $1)`, prevVersion).Scan(&publishedDE, &retiredPrev); err != nil {
		t.Fatal(err)
	}
	if publishedDE != draft.ID || retiredPrev != "retired" {
		t.Fatalf("after publish: published %s, previous %s", publishedDE, retiredPrev)
	}
	if code, raw := call(adminToken, "P3a test", http.MethodPost, "/v1/admin/legal-documents/"+draft.ID+"/publish", ""); code != http.StatusConflict ||
		buyerFieldsErrorCode(t, raw) != "legal.not_draft" {
		t.Fatalf("second publish: %d %s", code, raw)
	}
	if code, raw := call(adminToken, "", http.MethodGet, "/v1/admin/legal-documents/"+draft.ID, ""); code != http.StatusOK || decode(raw).ID != draft.ID {
		t.Fatalf("get: %d %s", code, raw)
	}
	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action IN ('v1.legal_document.create','v1.legal_document.publish')`, draft.ID).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audit rows = %d (%v)", audited, err)
	}

	// ── put the previous German text back (a published row cannot be deleted)
	body, _ := json.Marshal(map[string]string{"kind": "sale_terms", "locale": "de", "body_template": prevBody})
	code, raw = call(adminToken, "P3a test restore", http.MethodPost, "/v1/admin/legal-documents", string(body))
	if code != http.StatusCreated {
		t.Fatalf("restore create: %d %s", code, raw)
	}
	restore := decode(raw)
	created = append(created, restore.ID)
	if code, raw := call(adminToken, "P3a test restore", http.MethodPost, "/v1/admin/legal-documents/"+restore.ID+"/publish", ""); code != http.StatusOK {
		t.Fatalf("restore publish: %d %s", code, raw)
	}
}

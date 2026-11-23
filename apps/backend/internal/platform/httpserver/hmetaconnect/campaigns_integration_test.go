//go:build integration

package hmetaconnect

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

const campToken = "EAAB-campaign-plaintext-token-777"

// campGraph is a Graph stub of the ad account act_77 with campaigns 501
// (ACTIVE), 502 (PAUSED), 503 (ARCHIVED), and 601 of another account 88. It
// records every request; dead=true answers 190 to everything.
type campGraph struct {
	mu     sync.Mutex
	status map[string]string
	posts  []string // "id status=..."
	dead   bool
	// onDead runs before a 190 is answered (a reconnect in the meantime).
	onDead func()
	// postErr, when set, is the 400 body of every POST (recorded first).
	postErr string
}

func (g *campGraph) postCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.posts)
}

func (g *campGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v25.0/")
	if g.dead || r.Header.Get("Authorization") != "Bearer "+campToken {
		if g.onDead != nil {
			g.onDead()
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":190,"message":"token ` + campToken + ` expired"}}`))
		return
	}
	account := map[string]string{"501": "77", "502": "77", "503": "77", "601": "88"}
	switch {
	case r.Method == http.MethodGet && path == "act_77/campaigns":
		_, _ = w.Write([]byte(`{"data":[{"id":"501","name":"Spring","status":"` + g.status["501"] + `","effective_status":"` + g.status["501"] + `"},` +
			`{"id":"502","name":"Summer","status":"` + g.status["502"] + `","effective_status":"CAMPAIGN_PAUSED"}]}`))
	case r.Method == http.MethodGet && account[path] != "":
		_, _ = w.Write([]byte(`{"id":"` + path + `","name":"Camp ` + path + `","status":"` + g.status[path] + `","effective_status":"` + g.status[path] + `","account_id":"` + account[path] + `"}`))
	case r.Method == http.MethodGet:
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":100,"error_subcode":33}}`))
	case r.Method == http.MethodPost && account[path] != "":
		body, _ := io.ReadAll(r.Body)
		g.posts = append(g.posts, path+" "+string(body))
		if g.postErr != "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(g.postErr))
			return
		}
		if st := strings.TrimPrefix(string(body), "status="); st == "ACTIVE" || st == "PAUSED" {
			g.status[path] = st
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	default:
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":100}}`))
	}
}

type campEnv struct {
	*itEnv
	cg *campGraph
	h  *Handler
}

func newCampEnv(t *testing.T) *campEnv {
	e := newEnv(t)
	cg := &campGraph{status: map[string]string{"501": "ACTIVE", "502": "PAUSED", "503": "ARCHIVED", "601": "ACTIVE"}}
	srv := httptest.NewServer(cg)
	t.Cleanup(srv.Close)
	ce := &campEnv{itEnv: e, cg: cg}
	cfg := e.h.cfg
	cfg.OwnerTelegram = false
	ce.h = New(e.pool, audit.NewPGWriter(e.pool), cfg, e.h.box, metaconnect.NewClient(srv.URL, "v25.0", "app-1", itSecret, srv.Client()), nil)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'user_id' = ANY($2)`,
			adminnotify.JobTypeEmail, []string{e.ownerA.String(), e.ownerB.String(), e.mgrA.String()})
	})
	return ce
}

// connectA stores an active connection of orgA with act_77 selected.
func (ce *campEnv) connectA(ctx context.Context) {
	tx, err := ce.pool.Begin(ctx)
	if err != nil {
		ce.t.Fatal(err)
	}
	if err := metaconnect.SaveConnection(ctx, tx, ce.h.box, metaconnect.NewConnection{
		OrgID: ce.orgA, UserID: ce.ownerA, MetaUserID: "camp-user", Token: metaconnect.Token{AccessToken: campToken},
		Assets: []metaconnect.Asset{{Kind: metaconnect.KindAdAccount, MetaID: "act_77", Name: "Ads"}},
	}); err != nil {
		ce.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		ce.t.Fatal(err)
	}
}

func (ce *campEnv) setStatus(user uuid.UUID, campaign, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader(body))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("org_id", ce.orgA.String())
	rc.URLParams.Add("campaign_id", campaign)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = auth.WithActor(ctx, auth.Actor{ID: user.String(), Type: auth.ActorTypeUser})
	w := httptest.NewRecorder()
	ce.h.HandleSetCampaignStatus(w, req.WithContext(ctx))
	return w
}

func (ce *campEnv) list(user uuid.UUID) *httptest.ResponseRecorder {
	return ce.call(http.MethodGet, "/campaigns", "", user, ce.orgA, ce.h.HandleListCampaigns)
}

func (ce *campEnv) audits(t *testing.T) int {
	t.Helper()
	var n int
	if err := ce.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.campaign_status'`, ce.orgA.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMetaCampaigns_ListPauseResume_LiveDB(t *testing.T) {
	ce := newCampEnv(t)
	ctx := context.Background()

	// Before a connection.
	if w := ce.list(ce.ownerA); w.Code != 404 || !strings.Contains(w.Body.String(), "meta.not_connected") {
		t.Fatalf("list without connection: %d %s", w.Code, w.Body.String())
	}
	ce.connectA(ctx)

	w := ce.list(ce.ownerA)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, campToken) {
		t.Fatalf("list: %d %s", w.Code, body)
	}
	var list CampaignList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.AdAccountID != "act_77" || len(list.Campaigns) != 2 || list.Campaigns[0].ID != "501" || list.Campaigns[1].EffectiveStatus != "CAMPAIGN_PAUSED" {
		t.Fatalf("list = %+v", list)
	}

	// Pause 501.
	w = ce.setStatus(ce.ownerA, "501", `{"status":"PAUSED"}`)
	var res CampaignStatusResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || !res.Changed || res.Campaign.Status != "PAUSED" || res.Campaign.ID != "501" {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if ce.cg.postCount() != 1 || ce.cg.posts[0] != "501 status=PAUSED" {
		t.Fatalf("posts = %v", ce.cg.posts)
	}
	var md map[string]any
	var raw string
	if err := ce.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.campaign_status' ORDER BY occurred_at DESC LIMIT 1`, ce.orgA.String()).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &md); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, campToken) || md["from"] != "ACTIVE" || md["to"] != "PAUSED" || md["campaign_id"] != "501" ||
		md["ad_account_id"] != "act_77" || md["org_id"] != ce.orgA.String() || md["campaign_name"] != "Camp 501" || len(md) != 6 {
		t.Fatalf("audit metadata = %s", raw)
	}

	// The same status again: no POST, no audit.
	w = ce.setStatus(ce.ownerA, "501", `{"status":"PAUSED"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"changed":false`) || ce.cg.postCount() != 1 || ce.audits(t) != 1 {
		t.Fatalf("same status: %d %s posts=%d audits=%d", w.Code, w.Body.String(), ce.cg.postCount(), ce.audits(t))
	}

	// Resume.
	w = ce.setStatus(ce.ownerA, "501", `{"status":"ACTIVE"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"changed":true`) || !strings.Contains(w.Body.String(), `"status":"ACTIVE"`) ||
		ce.cg.postCount() != 2 || ce.cg.posts[1] != "501 status=ACTIVE" || ce.audits(t) != 2 {
		t.Fatalf("resume: %d %s posts=%v", w.Code, w.Body.String(), ce.cg.posts)
	}

	// A campaign of another ad account, and one Meta does not know: 404, no POST.
	for _, id := range []string{"601", "999"} {
		if w = ce.setStatus(ce.ownerA, id, `{"status":"PAUSED"}`); w.Code != 404 || !strings.Contains(w.Body.String(), "meta.campaign_not_found") {
			t.Fatalf("campaign %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	// An archived campaign is not changeable.
	if w = ce.setStatus(ce.ownerA, "503", `{"status":"ACTIVE"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "meta.campaign_not_changeable") {
		t.Fatalf("archived: %d %s", w.Code, w.Body.String())
	}
	// Bad bodies and ids: 400 before Meta.
	for _, c := range []struct{ id, body string }{
		{"501", `{"status":"DELETED"}`}, {"501", `{"status":"paused"}`}, {"501", `{}`}, {"501", `{"status":"PAUSED","daily_budget":1}`},
		{"501", `{"status":"PAUSED"}{}`}, {"501", `not json`}, {"501", `{"status":"PAUSED","x":"` + strings.Repeat("a", 5000) + `"}`},
		{"50a", `{"status":"PAUSED"}`}, {"..", `{"status":"PAUSED"}`}, {strings.Repeat("1", 33), `{"status":"PAUSED"}`},
	} {
		if w = ce.setStatus(ce.ownerA, c.id, c.body); w.Code != 400 {
			t.Errorf("bad %q %q: %d %s", c.id, c.body[:min(len(c.body), 40)], w.Code, w.Body.String())
		}
	}
	if ce.cg.postCount() != 2 || ce.audits(t) != 2 {
		t.Fatalf("a refused call reached Meta or audit: posts=%v audits=%d", ce.cg.posts, ce.audits(t))
	}

	// The manager of A (no org.update) is refused by the database check.
	if w = ce.setStatus(ce.mgrA, "501", `{"status":"PAUSED"}`); w.Code != 403 {
		t.Fatalf("manager: %d %s", w.Code, w.Body.String())
	}
	if w = ce.list(ce.mgrA); w.Code != 403 {
		t.Fatalf("manager list: %d", w.Code)
	}
	// An API key is refused.
	req := httptest.NewRequest(http.MethodGet, "/campaigns", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("org_id", ce.orgA.String())
	kctx := auth.WithActor(context.WithValue(req.Context(), chi.RouteCtxKey, rc), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService})
	kw := httptest.NewRecorder()
	ce.h.HandleListCampaigns(kw, req.WithContext(kctx))
	if kw.Code != 403 || !strings.Contains(kw.Body.String(), "meta.api_key_forbidden") {
		t.Fatalf("api key: %d %s", kw.Code, kw.Body.String())
	}
	// The owner of B on A's path: the database check refuses.
	if w = ce.list(ce.ownerB); w.Code != 403 {
		t.Fatalf("owner of B: %d", w.Code)
	}
	if ce.cg.postCount() != 2 {
		t.Fatalf("posts = %v", ce.cg.posts)
	}

	// No selected ad account: 409.
	if _, err := ce.pool.Exec(ctx, `UPDATE org_meta_assets SET selected = false WHERE org_id = $1`, ce.orgA); err != nil {
		t.Fatal(err)
	}
	if w = ce.list(ce.ownerA); w.Code != 409 || !strings.Contains(w.Body.String(), "meta.no_ad_account") {
		t.Fatalf("no ad account: %d %s", w.Code, w.Body.String())
	}
	if _, err := ce.pool.Exec(ctx, `UPDATE org_meta_assets SET selected = true WHERE org_id = $1`, ce.orgA); err != nil {
		t.Fatal(err)
	}

	// Graph 190: the connection moves to error, the token is wiped, the owner
	// is told once, 409; a second call is 409 without Meta.
	ce.cg.mu.Lock()
	ce.cg.dead = true
	ce.cg.mu.Unlock()
	w = ce.setStatus(ce.ownerA, "502", `{"status":"ACTIVE"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "meta.reconnect_required") || strings.Contains(w.Body.String(), campToken) {
		t.Fatalf("190: %d %s", w.Code, w.Body.String())
	}
	var status string
	var enc *string
	if err := ce.pool.QueryRow(ctx, `SELECT status, access_token_enc FROM org_meta_connections WHERE org_id = $1`, ce.orgA).Scan(&status, &enc); err != nil {
		t.Fatal(err)
	}
	if status != "error" || enc != nil {
		t.Fatalf("after 190: status=%s token wiped=%v", status, enc == nil)
	}
	var jobs int
	if err := ce.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'user_id' = $2 AND payload->>'kind' = $3`,
		adminnotify.JobTypeEmail, ce.ownerA.String(), adminnotify.KindMetaConnectionBroken).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("owner notice jobs = %d", jobs)
	}
	if w = ce.list(ce.ownerA); w.Code != 409 || !strings.Contains(w.Body.String(), "meta.reconnect_required") {
		t.Fatalf("list after 190: %d %s", w.Code, w.Body.String())
	}
	if ce.cg.postCount() != 2 || ce.audits(t) != 2 {
		t.Fatalf("after 190 posts=%v audits=%d", ce.cg.posts, ce.audits(t))
	}
}

// failAudit is an audit writer that always fails.
type failAudit struct{}

func (failAudit) Write(context.Context, audit.Event) error { return errors.New("audit down") }
func (failAudit) WriteTx(context.Context, pgx.Tx, audit.Event) error {
	return errors.New("audit down")
}

func (ce *campEnv) failedAudits(t *testing.T) []string {
	t.Helper()
	rows, err := ce.pool.Query(context.Background(), `SELECT metadata::text FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.campaign_status_failed' ORDER BY occurred_at`, ce.orgA.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (ce *campEnv) connStatus(t *testing.T) (string, *string, *string) {
	t.Helper()
	var status string
	var enc, lastErr *string
	if err := ce.pool.QueryRow(context.Background(), `SELECT status, access_token_enc, last_error FROM org_meta_connections WHERE org_id = $1`, ce.orgA).Scan(&status, &enc, &lastErr); err != nil {
		t.Fatal(err)
	}
	return status, enc, lastErr
}

// The audit row is mandatory: no writer or a failing writer means 503 and
// no POST; a failed POST leaves both rows; Graph 100 on the POST is 422 and
// not a connection fault.
func TestMetaCampaigns_AuditAndRejectedPost_LiveDB(t *testing.T) {
	ce := newCampEnv(t)
	ctx := context.Background()
	ce.connectA(ctx)

	// No audit writer: 503 before anything.
	saved := ce.h.audit
	ce.h.audit = nil
	if w := ce.setStatus(ce.ownerA, "501", `{"status":"PAUSED"}`); w.Code != 503 || !strings.Contains(w.Body.String(), "meta.unavailable") {
		t.Fatalf("nil audit: %d %s", w.Code, w.Body.String())
	}
	// A failing audit writer: 503 and no POST.
	ce.h.audit = failAudit{}
	if w := ce.setStatus(ce.ownerA, "501", `{"status":"PAUSED"}`); w.Code != 503 || !strings.Contains(w.Body.String(), "meta.unavailable") {
		t.Fatalf("failing audit: %d %s", w.Code, w.Body.String())
	}
	ce.h.audit = saved
	if ce.cg.postCount() != 0 || ce.audits(t) != 0 {
		t.Fatalf("no audit reached Meta: posts=%v audits=%d", ce.cg.posts, ce.audits(t))
	}

	// Graph 100 on the POST: 422, both rows, the connection keeps no error.
	ce.cg.mu.Lock()
	ce.cg.postErr = `{"error":{"code":100,"error_subcode":1487}}`
	ce.cg.mu.Unlock()
	w := ce.setStatus(ce.ownerA, "501", `{"status":"PAUSED"}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "meta.campaign_rejected") {
		t.Fatalf("rejected: %d %s", w.Code, w.Body.String())
	}
	failed := ce.failedAudits(t)
	if ce.cg.postCount() != 1 || ce.audits(t) != 1 || len(failed) != 1 {
		t.Fatalf("rejected: posts=%v audits=%d failed=%v", ce.cg.posts, ce.audits(t), failed)
	}
	var md map[string]any
	if err := json.Unmarshal([]byte(failed[0]), &md); err != nil {
		t.Fatal(err)
	}
	if md["org_id"] != ce.orgA.String() || md["campaign_id"] != "501" || md["to"] != "PAUSED" || md["reason"] != "graph_100" || len(md) != 4 {
		t.Fatalf("failed audit metadata = %s", failed[0])
	}
	if status, enc, lastErr := ce.connStatus(t); status != "active" || enc == nil || lastErr != nil {
		t.Fatalf("after 100: status=%s token=%v last_error=%v", status, enc != nil, lastErr)
	}

	// Another Graph error on the POST: 502 and a second failed row.
	ce.cg.mu.Lock()
	ce.cg.postErr = `{"error":{"code":1}}`
	ce.cg.mu.Unlock()
	if w = ce.setStatus(ce.ownerA, "501", `{"status":"PAUSED"}`); w.Code != 502 || !strings.Contains(w.Body.String(), "meta.graph_error") {
		t.Fatalf("graph error: %d %s", w.Code, w.Body.String())
	}
	if failed = ce.failedAudits(t); ce.cg.postCount() != 2 || ce.audits(t) != 2 || len(failed) != 2 || !strings.Contains(failed[1], "graph_1") {
		t.Fatalf("graph error: posts=%v audits=%d failed=%v", ce.cg.posts, ce.audits(t), failed)
	}
}

// A 190 for an OLD token does not wipe a token saved by a reconnect in the
// meantime: the connection stays active and the owner is not told.
func TestMetaCampaigns_190AfterReconnectKeepsNewToken_LiveDB(t *testing.T) {
	ce := newCampEnv(t)
	ctx := context.Background()
	ce.connectA(ctx)

	ce.cg.mu.Lock()
	ce.cg.dead = true
	ce.cg.onDead = func() {
		tx, err := ce.pool.Begin(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := metaconnect.SaveConnection(ctx, tx, ce.h.box, metaconnect.NewConnection{
			OrgID: ce.orgA, UserID: ce.ownerA, MetaUserID: "camp-user", Token: metaconnect.Token{AccessToken: "EAAB-reconnected-token-888"},
			Assets: []metaconnect.Asset{{Kind: metaconnect.KindAdAccount, MetaID: "act_77", Name: "Ads"}},
		}); err != nil {
			t.Error(err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Error(err)
		}
	}
	ce.cg.mu.Unlock()

	if w := ce.list(ce.ownerA); w.Code != 409 || !strings.Contains(w.Body.String(), "meta.reconnect_required") {
		t.Fatalf("190: %d %s", w.Code, w.Body.String())
	}
	status, enc, _ := ce.connStatus(t)
	if status != "active" || enc == nil {
		t.Fatalf("the reconnected token was wiped: status=%s token=%v", status, enc != nil)
	}
	tok, err := metaconnect.OpenToken(ce.h.box, ce.orgA, *enc)
	if err != nil || tok != "EAAB-reconnected-token-888" {
		t.Fatalf("stored token is not the reconnected one: err=%v", err)
	}
	var jobs int
	if err := ce.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'user_id' = $2 AND payload->>'kind' = $3`,
		adminnotify.JobTypeEmail, ce.ownerA.String(), adminnotify.KindMetaConnectionBroken).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("owner notice jobs = %d", jobs)
	}
}

func TestMetaCampaigns_Disabled_LiveDB(t *testing.T) {
	e := newEnv(t)
	h := New(e.pool, nil, Settings{}, nil, nil, nil)
	if w := e.call(http.MethodGet, "/campaigns", "", e.ownerA, e.orgA, h.HandleListCampaigns); w.Code != 503 || !strings.Contains(w.Body.String(), "meta.disabled") {
		t.Fatalf("disabled: %d %s", w.Code, w.Body.String())
	}
}

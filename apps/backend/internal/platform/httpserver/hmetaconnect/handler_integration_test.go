//go:build integration

package hmetaconnect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

const (
	itSecret   = "it-app-secret"
	itToken    = "EAAB-plaintext-meta-token-0123456789"
	itMetaUser = "4242"
	itEC       = "https://events.example.test"
	itRedirect = "https://api.example.test/v1/integrations/meta/callback"
)

// itGraph is a Graph stub: one code, one token, a business with two pixels
// and an ad account, a forbidden edge, and DELETE /me/permissions.
type itGraph struct {
	mu      sync.Mutex
	revoked int
	codes   map[string]string // code -> redirect_uri expected
}

func (g *itGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v25.0/")
	if path == "oauth/access_token" {
		// Meta documents the exchange as GET with query parameters.
		q := r.URL.Query()
		want, ok := g.codes[q.Get("code")]
		if !ok || r.Method != http.MethodGet || q.Get("client_secret") != itSecret || q.Get("redirect_uri") != want {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":100,"type":"OAuthException"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + itToken + `","token_type":"bearer"}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+itToken {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":190}}`))
		return
	}
	switch {
	case path == "me":
		_, _ = w.Write([]byte(`{"id":"` + itMetaUser + `","client_business_id":"900"}`))
	case path == "me/permissions" && r.Method == http.MethodDelete:
		g.mu.Lock()
		g.revoked++
		g.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true}`))
	case path == "me/permissions":
		_, _ = w.Write([]byte(`{"data":[{"permission":"ads_management","status":"granted"},{"permission":"business_management","status":"declined"}]}`))
	case path == "900/owned_pixels":
		_, _ = w.Write([]byte(`{"data":[{"id":"1001","name":"Main pixel"},{"id":"1002","name":"Second pixel"}]}`))
	case path == "900/owned_ad_accounts":
		_, _ = w.Write([]byte(`{"data":[{"id":"act_77","name":"Ads","currency":"ILS","timezone_name":"Asia/Jerusalem"}]}`))
	case path == "900/client_pixels":
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":10}}`))
	default:
		_, _ = w.Write([]byte(`{"data":[]}`))
	}
}

type itEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	h      *Handler
	graph  *itGraph
	orgA   uuid.UUID
	orgB   uuid.UUID
	ownerA uuid.UUID
	ownerB uuid.UUID
	mgrA   uuid.UUID
}

func newEnv(t *testing.T) *itEnv {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e := &itEnv{t: t, pool: pool, orgA: uuid.New(), orgB: uuid.New(), ownerA: uuid.New(), ownerB: uuid.New(), mgrA: uuid.New()}
	for _, o := range []uuid.UUID{e.orgA, e.orgB} {
		n := o.String()[:8]
		if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, o, "Meta IT "+n, "meta-it-"+n); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []struct {
		id   uuid.UUID
		org  uuid.UUID
		role string
	}{{e.ownerA, e.orgA, "org_admin"}, {e.ownerB, e.orgB, "org_admin"}, {e.mgrA, e.orgA, "organizer"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, u.id, "meta-it-"+u.id.String()+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)`, u.id, u.org, u.role); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM audit_events WHERE resource_id = ANY($1)`, []string{e.orgA.String(), e.orgB.String()})
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{e.orgA, e.orgB})
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{e.ownerA, e.ownerB, e.mgrA})
	})
	e.graph = &itGraph{codes: map[string]string{"code-1": itRedirect, "code-2": itRedirect, "code-js": ""}}
	srv := httptest.NewServer(e.graph)
	t.Cleanup(srv.Close)
	box, err := secretbox.ParseKeys("v1:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	edges, err := metaconnect.ParseEdges("dataset:{business}/owned_pixels,dataset:{business}/client_pixels,ad_account:{business}/owned_ad_accounts,page:me/accounts")
	if err != nil {
		t.Fatal(err)
	}
	e.h = New(pool, audit.NewPGWriter(pool), Settings{
		Enabled: true, AppID: "app-1", AppSecret: itSecret, ConfigID: "cfg-1", GraphVersion: "v25.0",
		RedirectURL: itRedirect, EventCenterURL: itEC, Edges: edges,
	}, box, metaconnect.NewClient(srv.URL, "v25.0", "app-1", itSecret, srv.Client()), nil)
	return e
}

func (e *itEnv) call(method, target string, body string, user uuid.UUID, org uuid.UUID, fn http.HandlerFunc) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := req.Context()
	if org != uuid.Nil {
		rc := chi.NewRouteContext()
		rc.URLParams.Add("org_id", org.String())
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rc)
	}
	if user != uuid.Nil {
		ctx = auth.WithActor(ctx, auth.Actor{ID: user.String(), Type: auth.ActorTypeUser})
	}
	if strings.HasPrefix(body, "signed_request=") {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

func (e *itEnv) connect(user, org uuid.UUID) ConnectAnswer {
	w := e.call(http.MethodPost, "/connect", "", user, org, e.h.HandleConnect)
	if w.Code != 200 {
		e.t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	var a ConnectAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *itEnv) callback(code, state string) string {
	q := url.Values{"code": {code}, "state": {state}}
	w := e.call(http.MethodGet, "/v1/integrations/meta/callback?"+q.Encode(), "", uuid.Nil, uuid.Nil, e.h.HandleCallback)
	if w.Code != http.StatusFound || strings.Contains(w.Header().Get("Content-Type"), "json") {
		e.t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	return w.Header().Get("Location")
}

// stateOf reads the signed state out of a connect answer's login_url.
func stateOf(t *testing.T, a ConnectAnswer) string {
	t.Helper()
	u, err := url.Parse(a.LoginURL)
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("login_url without a state: %q", a.LoginURL)
	}
	return u.Query().Get("state")
}

func (e *itEnv) finish(user, org uuid.UUID, code, state string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"code": code, "state": state})
	return e.call(http.MethodPost, "/finish", string(b), user, org, e.h.HandleFinish)
}

// login runs connect, the callback and finish for user in org and returns
// the finish answer. The callback must hand code and state over in the
// fragment only.
func (e *itEnv) login(user, org uuid.UUID, code string) *httptest.ResponseRecorder {
	e.t.Helper()
	st := stateOf(e.t, e.connect(user, org))
	loc := e.callback(code, st)
	prefix := itEC + "/o/" + org.String() + "/ads/meta?meta=finish#"
	if !strings.HasPrefix(loc, prefix) {
		e.t.Fatalf("callback location = %q", loc)
	}
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, prefix))
	if err != nil || frag.Get("code") != code || frag.Get("state") != st {
		e.t.Fatalf("callback fragment = %q", loc)
	}
	return e.finish(user, org, frag.Get("code"), frag.Get("state"))
}

func signedRequest(secret, userID string) string {
	b, _ := json.Marshal(map[string]any{"algorithm": "HMAC-SHA256", "user_id": userID, "issued_at": time.Now().Unix()})
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(p))
	return "signed_request=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(m.Sum(nil))+"."+p)
}

func TestMetaConnect_FullFlow_LiveDB(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// GET before any connection.
	w := e.call(http.MethodGet, "/", "", e.ownerA, e.orgA, e.h.HandleGet)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"connection":null`) {
		t.Fatalf("get empty: %d %s", w.Code, w.Body.String())
	}

	ans := e.connect(e.ownerA, e.orgA)
	if ans.AppID != "app-1" || ans.ConfigID != "cfg-1" || !strings.Contains(ans.LoginURL, "config_id=cfg-1") ||
		!strings.Contains(ans.LoginURL, "override_default_response_type=true") || !strings.HasPrefix(ans.LoginURL, "https://www.facebook.com/v25.0/dialog/oauth?") ||
		!strings.Contains(ans.LoginURL, "redirect_uri="+url.QueryEscape(itRedirect)) {
		t.Fatalf("connect answer: %+v", ans)
	}
	// The callback itself stores nothing: a public GET never exchanges a code.
	st := stateOf(t, ans)
	_ = e.callback("code-1", st)
	var stored int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM org_meta_connections WHERE org_id = $1`, e.orgA).Scan(&stored)
	if stored != 0 {
		t.Fatal("the public callback stored a connection")
	}
	w = e.finish(e.ownerA, e.orgA, "code-1", st)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, itToken) || !strings.Contains(body, `"status":"active"`) {
		t.Fatalf("finish: %d %s", w.Code, body)
	}
	w = e.call(http.MethodGet, "/", "", e.ownerA, e.orgA, e.h.HandleGet)
	body = w.Body.String()
	if w.Code != 200 || strings.Contains(body, itToken) || !strings.Contains(body, `"status":"active"`) ||
		!strings.Contains(body, `"meta_business_id":"900"`) || !strings.Contains(body, `"selected_ad_account_id":"act_77"`) ||
		!strings.Contains(body, `"selected_dataset_id":null`) || !strings.Contains(body, `"ads_management"`) || strings.Contains(body, "business_management") {
		t.Fatalf("get after connect: %d %s", w.Code, body)
	}

	// The sealed column is not the plaintext and opens only for orgA.
	var sealed, metaUser string
	if err := e.pool.QueryRow(ctx, `SELECT access_token_enc, meta_user_id FROM org_meta_connections WHERE org_id = $1`, e.orgA).Scan(&sealed, &metaUser); err != nil {
		t.Fatal(err)
	}
	if sealed == itToken || strings.Contains(sealed, itToken) || metaUser != itMetaUser {
		t.Fatalf("stored token is not sealed: %q", sealed)
	}
	if got, err := metaconnect.OpenToken(e.h.box, e.orgA, sealed); err != nil || got != itToken {
		t.Fatalf("open: %v", err)
	}
	if _, err := metaconnect.OpenToken(e.h.box, e.orgB, sealed); err == nil {
		t.Fatal("a token of orgA opened for orgB")
	}
	var leaked int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND metadata::text LIKE '%' || $2 || '%'`, e.orgA.String(), itToken).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("token in audit: %d %v", leaked, err)
	}
	var connects int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.connect'`, e.orgA.String()).Scan(&connects)
	if connects != 1 {
		t.Fatalf("connect audit rows = %d", connects)
	}

	// No dataset selected yet (two pixels): ActiveDataset says no.
	if _, _, ok, err := metaconnect.ActiveDataset(ctx, e.pool, e.h.box, e.orgA); ok || err != nil {
		t.Fatalf("ActiveDataset before select: %v %v", ok, err)
	}
	// PATCH: a dataset of this connection, then a foreign / unknown id.
	w = e.call(http.MethodPatch, "/", `{"dataset_id":"1002"}`, e.ownerA, e.orgA, e.h.HandlePatch)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"selected_dataset_id":"1002"`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if pix, tok, ok, err := metaconnect.ActiveDataset(ctx, e.pool, e.h.box, e.orgA); !ok || err != nil || pix != "1002" || tok != itToken {
		t.Fatalf("ActiveDataset: %q %v %v", pix, ok, err)
	}
	if w = e.call(http.MethodPatch, "/", `{"dataset_id":"act_77"}`, e.ownerA, e.orgA, e.h.HandlePatch); w.Code != 422 {
		t.Fatalf("patch wrong kind: %d", w.Code)
	}
	// orgB has no connection; a meta id of orgA is not orgB's.
	if w = e.call(http.MethodPatch, "/", `{"dataset_id":"1002"}`, e.ownerB, e.orgB, e.h.HandlePatch); w.Code != 404 {
		t.Fatalf("patch other org: %d %s", w.Code, w.Body.String())
	}
	// orgB connected with its own pixel: neither organization may pick the
	// other's meta id.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := metaconnect.SaveConnection(ctx, tx, e.h.box, metaconnect.NewConnection{
		OrgID: e.orgB, UserID: e.ownerB, MetaUserID: "other-user", Token: metaconnect.Token{AccessToken: "tok-b"},
		Assets: []metaconnect.Asset{{Kind: metaconnect.KindDataset, MetaID: "2001", Name: "B pixel"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if w = e.call(http.MethodPatch, "/", `{"dataset_id":"1002"}`, e.ownerB, e.orgB, e.h.HandlePatch); w.Code != 422 || !strings.Contains(w.Body.String(), "meta.asset_not_found") {
		t.Fatalf("patch foreign meta id into orgB: %d %s", w.Code, w.Body.String())
	}
	if w = e.call(http.MethodPatch, "/", `{"dataset_id":"2001"}`, e.ownerA, e.orgA, e.h.HandlePatch); w.Code != 422 {
		t.Fatalf("patch foreign meta id into orgA: %d", w.Code)
	}
	if pix, _, ok, _ := metaconnect.ActiveDataset(ctx, e.pool, e.h.box, e.orgB); !ok || pix != "2001" {
		t.Fatalf("orgB single pixel auto-selected: %q %v", pix, ok)
	}
	if w = e.call(http.MethodPatch, "/", `{"bogus":"1"}`, e.ownerA, e.orgA, e.h.HandlePatch); w.Code != 400 {
		t.Fatalf("patch unknown key: %d", w.Code)
	}

	// A reconnect keeps the selection.
	if w := e.login(e.ownerA, e.orgA, "code-2"); w.Code != 200 {
		t.Fatalf("reconnect: %d %s", w.Code, w.Body.String())
	}
	// A code the exchange does not accept with META_OAUTH_REDIRECT_URL (a JS
	// SDK code) fails with a JSON reason, nothing changes.
	if w := e.login(e.ownerA, e.orgA, "code-js"); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "meta.exchange_failed") {
		t.Fatalf("exchange failure: %d %s", w.Code, w.Body.String())
	}
	if pix, _, ok, _ := metaconnect.ActiveDataset(ctx, e.pool, e.h.box, e.orgA); !ok || pix != "1002" {
		t.Fatalf("selection lost on reconnect: %q %v", pix, ok)
	}

	// MarkTokenInvalid moves active -> error once and wipes the token.
	if changed, err := metaconnect.MarkTokenInvalid(ctx, e.pool, e.orgA, "graph_190"); !changed || err != nil {
		t.Fatalf("mark: %v %v", changed, err)
	}
	var wiped bool
	_ = e.pool.QueryRow(ctx, `SELECT access_token_enc IS NULL FROM org_meta_connections WHERE org_id = $1`, e.orgA).Scan(&wiped)
	if !wiped {
		t.Fatal("MarkTokenInvalid kept the dead token")
	}
	if changed, _ := metaconnect.MarkTokenInvalid(ctx, e.pool, e.orgA, "graph_190"); changed {
		t.Fatal("mark twice changed the row again")
	}
	if _, _, ok, _ := metaconnect.ActiveDataset(ctx, e.pool, e.h.box, e.orgA); ok {
		t.Fatal("ActiveDataset of an error connection")
	}

	// Disconnect of the error connection: the token is already gone, so Meta
	// is not called; the row is revoked and the assets are gone.
	w = e.call(http.MethodDelete, "/", "", e.ownerA, e.orgA, e.h.HandleDelete)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"revoked"`) || !strings.Contains(w.Body.String(), `"assets":[]`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if e.graph.revoked != 0 {
		t.Fatalf("DELETE /me/permissions calls with a wiped token = %d", e.graph.revoked)
	}
	// Reconnect, then disconnect an active connection: revoke at Meta.
	if w := e.login(e.ownerA, e.orgA, "code-1"); w.Code != 200 {
		t.Fatalf("reconnect after disconnect: %d %s", w.Code, w.Body.String())
	}
	w = e.call(http.MethodDelete, "/", "", e.ownerA, e.orgA, e.h.HandleDelete)
	if w.Code != 200 || e.graph.revoked != 1 {
		t.Fatalf("delete active: %d revoked=%d", w.Code, e.graph.revoked)
	}
	var enc *string
	_ = e.pool.QueryRow(ctx, `SELECT access_token_enc FROM org_meta_connections WHERE org_id = $1`, e.orgA).Scan(&enc)
	if enc != nil {
		t.Fatal("token not wiped on disconnect")
	}
	if w = e.call(http.MethodDelete, "/", "", uuid.New(), uuid.New(), e.h.HandleDelete); w.Code != 404 {
		t.Fatalf("delete without connection: %d", w.Code)
	}
}

func TestMetaConnect_CallbackRefusals_LiveDB(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A state of orgA's manager (a member without org.update).
	st, err := metaconnect.SignState(itSecret, e.orgA, e.mgrA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := e.finish(e.mgrA, e.orgA, "code-1", st); w.Code != 403 || !strings.Contains(w.Body.String(), "meta.forbidden") {
		t.Fatalf("manager: %d %s", w.Code, w.Body.String())
	}
	// A state naming orgB posted to orgA, and a state of another person.
	st, _ = metaconnect.SignState(itSecret, e.orgB, e.ownerA, time.Now())
	if w := e.finish(e.ownerA, e.orgA, "code-1", st); w.Code != 403 || !strings.Contains(w.Body.String(), "meta.state_mismatch") {
		t.Fatalf("foreign org: %d %s", w.Code, w.Body.String())
	}
	st, _ = metaconnect.SignState(itSecret, e.orgA, e.mgrA, time.Now())
	if w := e.finish(e.ownerA, e.orgA, "code-1", st); w.Code != 403 || !strings.Contains(w.Body.String(), "meta.state_mismatch") {
		t.Fatalf("state of another person: %d %s", w.Code, w.Body.String())
	}
	// Bad bodies and a tampered state.
	if w := e.call(http.MethodPost, "/finish", `{"code":`, e.ownerA, e.orgA, e.h.HandleFinish); w.Code != 400 || !strings.Contains(w.Body.String(), "meta.invalid_body") {
		t.Fatalf("bad body: %d %s", w.Code, w.Body.String())
	}
	if w := e.finish(e.ownerA, e.orgA, "", stateOf(t, e.connect(e.ownerA, e.orgA))); w.Code != 400 || !strings.Contains(w.Body.String(), "meta.no_code") {
		t.Fatalf("no code: %d %s", w.Code, w.Body.String())
	}
	bad, _ := metaconnect.SignState("other-secret", e.orgA, e.ownerA, time.Now())
	if w := e.finish(e.ownerA, e.orgA, "code-1", bad); w.Code != 400 || !strings.Contains(w.Body.String(), "meta.state_invalid") {
		t.Fatalf("bad state at finish: %d %s", w.Code, w.Body.String())
	}
	// The membership is checked again at finish, not only at connect.
	ans := e.connect(e.ownerA, e.orgA)
	if _, err := e.pool.Exec(ctx, `UPDATE memberships SET status = 'suspended' WHERE user_id = $1`, e.ownerA); err != nil {
		t.Fatal(err)
	}
	if w := e.finish(e.ownerA, e.orgA, "code-1", stateOf(t, ans)); w.Code != 403 || !strings.Contains(w.Body.String(), "meta.forbidden") {
		t.Fatalf("suspended member: %d %s", w.Code, w.Body.String())
	}
	// Tampered / wrong-secret state at the callback goes to the root, never
	// the org page.
	if loc := e.callback("code-1", bad); loc != itEC+"/?meta=error&reason=state_invalid" {
		t.Fatalf("bad state: %q", loc)
	}
	// The person cancelled at Meta.
	st, _ = metaconnect.SignState(itSecret, e.orgB, e.ownerB, time.Now())
	w := e.call(http.MethodGet, "/cb?error=access_denied&state="+url.QueryEscape(st), "", uuid.Nil, uuid.Nil, e.h.HandleCallback)
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "reason=denied") {
		t.Fatalf("denied: %d %q", w.Code, w.Header().Get("Location"))
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM org_meta_connections WHERE org_id = ANY($1)`, []uuid.UUID{e.orgA, e.orgB}).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused callback stored %d connections", n)
	}

	// An API key is refused on the organization routes.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("org_id", e.orgB.String())
	rctx := auth.WithActor(context.WithValue(req.Context(), chi.RouteCtxKey, rc), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService})
	rw := httptest.NewRecorder()
	e.h.HandleGet(rw, req.WithContext(rctx))
	if rw.Code != 403 || !strings.Contains(rw.Body.String(), "meta.api_key_forbidden") {
		t.Fatalf("api key: %d %s", rw.Code, rw.Body.String())
	}
}

// TestMetaConnect_StaleSignedRequest_LiveDB: a deauthorize older than a day is
// refused and changes nothing.
func TestMetaConnect_StaleSignedRequest_LiveDB(t *testing.T) {
	e := newEnv(t)
	b, _ := json.Marshal(map[string]any{"algorithm": "HMAC-SHA256", "user_id": itMetaUser, "issued_at": time.Now().Add(-25 * time.Hour).Unix()})
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, []byte(itSecret))
	m.Write([]byte(p))
	body := "signed_request=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(m.Sum(nil))+"."+p)
	if w := e.call(http.MethodPost, "/", body, uuid.Nil, uuid.Nil, e.h.HandleDeauthorize); w.Code != 400 {
		t.Fatalf("stale deauthorize: %d %s", w.Code, w.Body.String())
	}
}

func TestMetaConnect_MetaCallbacks_LiveDB(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, p := range []struct{ user, org uuid.UUID }{{e.ownerA, e.orgA}, {e.ownerB, e.orgB}} {
		if w := e.login(p.user, p.org, "code-1"); w.Code != 200 {
			t.Fatalf("connect %s: %d %s", p.org, w.Code, w.Body.String())
		}
	}
	// Bad signature: 400, nothing changes.
	if w := e.call(http.MethodPost, "/", signedRequest("wrong", itMetaUser), uuid.Nil, uuid.Nil, e.h.HandleDeauthorize); w.Code != 400 {
		t.Fatalf("bad deauthorize: %d", w.Code)
	}
	if w := e.call(http.MethodPost, "/", "signed_request=", uuid.Nil, uuid.Nil, e.h.HandleDataDeletion); w.Code != 400 {
		t.Fatalf("empty data deletion: %d", w.Code)
	}
	// Deauthorize revokes both organizations of that Meta user.
	if w := e.call(http.MethodPost, "/", signedRequest(itSecret, itMetaUser), uuid.Nil, uuid.Nil, e.h.HandleDeauthorize); w.Code != 200 {
		t.Fatalf("deauthorize: %d %s", w.Code, w.Body.String())
	}
	var revoked int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM org_meta_connections WHERE org_id = ANY($1) AND status = 'revoked' AND access_token_enc IS NULL`,
		[]uuid.UUID{e.orgA, e.orgB}).Scan(&revoked)
	if revoked != 2 {
		t.Fatalf("revoked rows = %d", revoked)
	}
	var sysRows int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.deauthorize' AND actor_type = 'system'`, e.orgA.String()).Scan(&sysRows)
	if sysRows != 1 {
		t.Fatalf("deauthorize audit rows = %d", sysRows)
	}
	// Data deletion removes connections and assets and answers {url, confirmation_code}.
	w := e.call(http.MethodPost, "/", signedRequest(itSecret, itMetaUser), uuid.Nil, uuid.Nil, e.h.HandleDataDeletion)
	var ans struct {
		URL  string `json:"url"`
		Code string `json:"confirmation_code"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ans) != nil || ans.Code == "" ||
		ans.URL != DefaultDataDeletionStatusURL+"?code="+ans.Code {
		t.Fatalf("data deletion: %d %s", w.Code, w.Body.String())
	}
	var left int
	_ = e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM org_meta_connections WHERE org_id = ANY($1)) + (SELECT count(*) FROM org_meta_assets WHERE org_id = ANY($1))`,
		[]uuid.UUID{e.orgA, e.orgB}).Scan(&left)
	if left != 0 {
		t.Fatalf("rows left after data deletion = %d", left)
	}
	// An unknown user still gets a confirmation.
	if w := e.call(http.MethodPost, "/", signedRequest(itSecret, "nobody"), uuid.Nil, uuid.Nil, e.h.HandleDataDeletion); w.Code != 200 {
		t.Fatalf("unknown user: %d", w.Code)
	}
}

func TestMetaConnect_Disabled_LiveDB(t *testing.T) {
	e := newEnv(t)
	off := New(e.pool, nil, Settings{EventCenterURL: itEC}, nil, nil, nil)
	if w := e.call(http.MethodPost, "/", "", e.ownerA, e.orgA, off.HandleConnect); w.Code != 503 || !strings.Contains(w.Body.String(), "meta.disabled") {
		t.Fatalf("connect disabled: %d %s", w.Code, w.Body.String())
	}
	if w := e.call(http.MethodGet, "/", "", e.ownerA, e.orgA, off.HandleGet); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("get disabled: %d %s", w.Code, w.Body.String())
	}
	if w := e.call(http.MethodGet, "/cb?code=x&state=y", "", uuid.Nil, uuid.Nil, off.HandleCallback); w.Code != 302 || w.Header().Get("Location") != itEC+"/?meta=error&reason=disabled" {
		t.Fatalf("callback disabled: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := e.call(http.MethodPost, "/finish", `{"code":"x","state":"y"}`, e.ownerA, e.orgA, off.HandleFinish); w.Code != 503 || !strings.Contains(w.Body.String(), "meta.disabled") {
		t.Fatalf("finish disabled: %d %s", w.Code, w.Body.String())
	}
	if w := e.call(http.MethodPost, "/", signedRequest(itSecret, itMetaUser), uuid.Nil, uuid.Nil, off.HandleDeauthorize); w.Code != 503 {
		t.Fatalf("deauthorize disabled: %d", w.Code)
	}
}

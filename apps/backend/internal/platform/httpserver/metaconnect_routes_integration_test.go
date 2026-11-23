//go:build integration

// metaconnect_routes_integration_test.go — the Meta connection routes (spec
// 66, stage M1) through the real router: org.update, the membership check of
// {org_id}, the API-key refusal and the finish step that closes the login
// CSRF (a state must name the caller AND the path organization).
//
//	DATABASE_URL=... go.exe test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestMetaRoutes_
package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

const (
	metaRtSecret   = "meta-routes-app-secret"
	metaRtToken    = "EAAB-routes-plaintext-token-42"
	metaRtRedirect = "https://api.example.test/v1/integrations/meta/callback"
	metaRtEC       = "https://events.example.test"
)

// metaRtGraph accepts one code with the configured redirect_uri and answers
// a business with one pixel.
func metaRtGraph(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v25.0/")
		if path == "oauth/access_token" {
			q := r.URL.Query()
			if q.Get("code") != "good-code" || q.Get("redirect_uri") != metaRtRedirect || q.Get("client_secret") != metaRtSecret {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":100}}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"` + metaRtToken + `"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+metaRtToken {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":190}}`))
			return
		}
		switch path {
		case "me":
			_, _ = w.Write([]byte(`{"id":"rt-meta-user","client_business_id":"5150"}`))
		case "me/permissions":
			_, _ = w.Write([]byte(`{"data":[{"permission":"ads_management","status":"granted"}]}`))
		case "5150/owned_pixels":
			_, _ = w.Write([]byte(`{"data":[{"id":"6001","name":"Pixel"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func metaRtEnv(t *testing.T) *sec1Env {
	t.Helper()
	graph := metaRtGraph(t)
	key := "v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("r", 32)))
	return newSec1EnvCfg(t, func(c *config.Config) {
		c.MetaAppID = "rt-app"
		c.MetaAppSecret = metaRtSecret
		c.MetaLoginConfigID = "rt-cfg"
		c.MetaTokenEncKey = key
		c.MetaGraphVersion = "v25.0"
		c.MetaGraphBaseURL = graph.URL
		c.MetaOAuthRedirectURL = metaRtRedirect
		c.MetaAssetEdges = "dataset:{business}/owned_pixels"
		c.EventCenterPublicURL = metaRtEC
	})
}

func metaRtState(t *testing.T, org, user uuid.UUID) string {
	t.Helper()
	st, err := metaconnect.SignState(metaRtSecret, org, user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func metaRtFinishBody(code, state string) string {
	b, _ := json.Marshal(map[string]string{"code": code, "state": state})
	return string(b)
}

func metaRtConnections(t *testing.T, e *sec1Env, org uuid.UUID) int {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM org_meta_connections WHERE org_id = $1`, org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The owner of A, a manager of B and an API key of B get nothing of B.
func TestMetaRoutes_IsolationAndRoles(t *testing.T) {
	e := metaRtEnv(t)
	orgA, orgB := e.org(t, "metaA"), e.org(t, "metaB")
	ownerA, ownerB, mgrB := e.user(t, "ownerA"), e.user(t, "ownerB"), e.user(t, "mgrB")
	e.member(t, ownerA, orgA, "org_admin")
	e.member(t, ownerB, orgB, "org_admin")
	e.member(t, mgrB, orgB, "organizer")
	tokA, tokB, tokMgr := e.token(t, ownerA), e.token(t, ownerB), e.token(t, mgrB)
	keyB := e.key(t, orgB, "event.read")
	base := "/v1/organizations/" + orgB.String() + "/integrations/meta"
	finishB := metaRtFinishBody("good-code", metaRtState(t, orgB, ownerB))

	routes := []struct{ method, path, body string }{
		{http.MethodGet, base, ""},
		{http.MethodPatch, base, `{"dataset_id":"6001"}`},
		{http.MethodDelete, base, ""},
		{http.MethodPost, base + "/connect", ""},
		{http.MethodPost, base + "/finish", finishB},
	}
	for _, who := range []struct{ name, bearer string }{{"owner of A", tokA}, {"manager of B", tokMgr}, {"API key of B", keyB}} {
		for _, r := range routes {
			if st, out := e.do(t, r.method, r.path, who.bearer, "", r.body); st != http.StatusForbidden {
				t.Errorf("%s %s %s: got %d %s, want 403", who.name, r.method, r.path, st, out)
			}
		}
	}
	if n := metaRtConnections(t, e, orgB); n != 0 {
		t.Fatalf("a refused finish stored %d connections", n)
	}
	// The owner of B reads its own (empty) connection; a bad org_id is 400.
	if out := e.expect(t, http.StatusOK, http.MethodGet, base, tokB, "", ""); !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("owner GET: %s", out)
	}
	e.expect(t, http.StatusBadRequest, http.MethodGet, "/v1/organizations/not-a-uuid/integrations/meta", tokB, "", "")
	// The connect answer has no state field any more.
	out := e.expect(t, http.StatusOK, http.MethodPost, base+"/connect", tokB, "", "")
	var ans map[string]any
	if json.Unmarshal([]byte(out), &ans) != nil || ans["login_url"] == "" {
		t.Fatalf("connect: %s", out)
	}
	if _, has := ans["state"]; has {
		t.Fatalf("connect still answers a state: %s", out)
	}
}

// The login CSRF: a code+state of another person of the same organization,
// or of another organization, is refused at finish; the owner's own login
// finishes against the Graph stub. The public callback stores nothing.
func TestMetaRoutes_FinishStateBinding(t *testing.T) {
	e := metaRtEnv(t)
	org, other := e.org(t, "metaF"), e.org(t, "metaO")
	owner, coOwner, otherOwner := e.user(t, "owner"), e.user(t, "coowner"), e.user(t, "other")
	e.member(t, owner, org, "org_admin")
	e.member(t, coOwner, org, "org_admin")
	e.member(t, otherOwner, other, "org_admin")
	tok := e.token(t, owner)
	path := "/v1/organizations/" + org.String() + "/integrations/meta/finish"

	// The public callback only verifies and hands over the code in the fragment.
	st := metaRtState(t, org, owner)
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/integrations/meta/callback?"+url.Values{"code": {"good-code"}, "state": {st}}.Encode(), nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, metaRtEC+"/o/"+org.String()+"/ads/meta?meta=finish#") ||
		strings.Contains(strings.SplitN(loc, "#", 2)[0], "good-code") {
		t.Fatalf("callback: %d %q", resp.StatusCode, loc)
	}
	if n := metaRtConnections(t, e, org); n != 0 {
		t.Fatalf("the public callback stored %d connections", n)
	}

	// A state of another person of the same organization: 403 state_mismatch.
	st2, out := e.do(t, http.MethodPost, path, tok, "", metaRtFinishBody("good-code", metaRtState(t, org, coOwner)))
	if st2 != http.StatusForbidden || sec1Code(out) != "meta.state_mismatch" {
		t.Fatalf("state of a co-owner: %d %s", st2, out)
	}
	// A state of another organization posted to mine.
	st2, out = e.do(t, http.MethodPost, path, tok, "", metaRtFinishBody("good-code", metaRtState(t, other, owner)))
	if st2 != http.StatusForbidden || sec1Code(out) != "meta.state_mismatch" {
		t.Fatalf("state of another org: %d %s", st2, out)
	}
	if n := metaRtConnections(t, e, org); n != 0 {
		t.Fatalf("a mismatched finish stored %d connections", n)
	}

	// The happy path.
	out = e.expect(t, http.StatusOK, http.MethodPost, path, tok, "", metaRtFinishBody("good-code", st))
	if strings.Contains(out, metaRtToken) || !strings.Contains(out, `"status":"active"`) || !strings.Contains(out, `"selected_dataset_id":"6001"`) {
		t.Fatalf("finish: %s", out)
	}
	var connectedBy uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT connected_by FROM org_meta_connections WHERE org_id = $1`, org).Scan(&connectedBy); err != nil || connectedBy != owner {
		t.Fatalf("connected_by = %s, %v", connectedBy, err)
	}
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM audit_events WHERE resource_id = $1`, org.String())
	})
}

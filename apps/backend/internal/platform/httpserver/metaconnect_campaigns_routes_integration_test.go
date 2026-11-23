//go:build integration

// metaconnect_campaigns_routes_integration_test.go - the Meta campaign
// routes (spec 66) through the real router: org.update, the membership
// check of {org_id} and the API-key refusal; a refused caller never reaches
// Meta.
//
//	DATABASE_URL=... go.exe test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestMetaCampaignRoutes_
package httpserver

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

const metaCampToken = "EAAB-campaign-routes-token-31"

func TestMetaCampaignRoutes_IsolationAndRoles(t *testing.T) {
	var mu sync.Mutex
	var posts []string
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v25.0/")
		if r.Header.Get("Authorization") != "Bearer "+metaCampToken {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":190}}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && path == "act_91/campaigns":
			_, _ = w.Write([]byte(`{"data":[{"id":"7001","name":"Camp","status":"ACTIVE","effective_status":"ACTIVE"}]}`))
		case r.Method == http.MethodGet && path == "7001":
			_, _ = w.Write([]byte(`{"id":"7001","name":"Camp","status":"ACTIVE","effective_status":"ACTIVE","account_id":"91"}`))
		case r.Method == http.MethodPost && path == "7001":
			mu.Lock()
			posts = append(posts, path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":100}}`))
		}
	}))
	t.Cleanup(graph.Close)
	key := "v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	e := newSec1EnvCfg(t, func(c *config.Config) {
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
	orgA, orgB := e.org(t, "campA"), e.org(t, "campB")
	ownerA, ownerB, mgrA := e.user(t, "ownerA"), e.user(t, "ownerB"), e.user(t, "mgrA")
	e.member(t, ownerA, orgA, "org_admin")
	e.member(t, ownerB, orgB, "org_admin")
	e.member(t, mgrA, orgA, "organizer")
	tokA, tokB, tokMgr := e.token(t, ownerA), e.token(t, ownerB), e.token(t, mgrA)
	keyA := e.key(t, orgA, "event.read")

	box, err := secretbox.ParseKeys(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := e.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := metaconnect.SaveConnection(ctx, tx, box, metaconnect.NewConnection{
		OrgID: orgA, UserID: ownerA, MetaUserID: "camp-rt-user", Token: metaconnect.Token{AccessToken: metaCampToken},
		Assets: []metaconnect.Asset{{Kind: metaconnect.KindAdAccount, MetaID: "act_91", Name: "Ads"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM audit_events WHERE resource_id = $1`, orgA.String())
	})

	base := "/v1/organizations/" + orgA.String() + "/integrations/meta/campaigns"
	status := base + "/7001/status"
	body := `{"status":"PAUSED"}`
	for _, who := range []struct{ name, bearer string }{{"owner of B", tokB}, {"manager of A", tokMgr}, {"API key of A", keyA}} {
		if st, out := e.do(t, http.MethodGet, base, who.bearer, "", ""); st != http.StatusForbidden {
			t.Errorf("%s list: got %d %s, want 403", who.name, st, out)
		}
		if st, out := e.do(t, http.MethodPost, status, who.bearer, "", body); st != http.StatusForbidden {
			t.Errorf("%s status: got %d %s, want 403", who.name, st, out)
		}
	}
	mu.Lock()
	n := len(posts)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused caller reached Meta: %d posts", n)
	}

	out := e.expect(t, http.StatusOK, http.MethodGet, base, tokA, "", "")
	if !strings.Contains(out, `"ad_account_id":"act_91"`) || !strings.Contains(out, `"id":"7001"`) || strings.Contains(out, metaCampToken) {
		t.Fatalf("owner list: %s", out)
	}
	out = e.expect(t, http.StatusOK, http.MethodPost, status, tokA, "", body)
	if !strings.Contains(out, `"changed":true`) || !strings.Contains(out, `"status":"PAUSED"`) {
		t.Fatalf("owner pause: %s", out)
	}
	if st, out := e.do(t, http.MethodPost, base+"/70a1/status", tokA, "", body); st != http.StatusBadRequest {
		t.Fatalf("bad campaign id: %d %s", st, out)
	}
	mu.Lock()
	n = len(posts)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("posts = %d", n)
	}
}

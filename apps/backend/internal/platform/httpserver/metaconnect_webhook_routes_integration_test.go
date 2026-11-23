//go:build integration

// metaconnect_webhook_routes_integration_test.go — Meta's app webhook
// (business_integration_uninstall) through the real router: public, no
// login, the verify token on GET and X-Hub-Signature-256 on POST.
package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestMetaRoutes_Webhook(t *testing.T) {
	graph := metaRtGraph(t)
	key := "v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("r", 32)))
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
		c.MetaWebhookVerifyToken = "rt-verify-token"
	})
	send := func(method, path, body, sig string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if st, out := send(http.MethodGet, "/v1/integrations/meta/webhook?hub.mode=subscribe&hub.verify_token=rt-verify-token&hub.challenge=777", "", ""); st != 200 || out != "777" {
		t.Fatalf("verify: %d %q", st, out)
	}
	if st, _ := send(http.MethodGet, "/v1/integrations/meta/webhook?hub.mode=subscribe&hub.verify_token=nope&hub.challenge=777", "", ""); st != 403 {
		t.Fatalf("wrong verify token: %d", st)
	}
	body := `{"object":"application","entry":[{"changes":[{"field":"business_integration_uninstall","value":{"business_manager_id":"no-such-business"}}]}]}`
	if st, _ := send(http.MethodPost, "/v1/integrations/meta/webhook", body, "sha256=00"); st != 401 {
		t.Fatalf("bad signature: %d", st)
	}
	m := hmac.New(sha256.New, []byte(metaRtSecret))
	m.Write([]byte(body))
	if st, out := send(http.MethodPost, "/v1/integrations/meta/webhook", body, "sha256="+hex.EncodeToString(m.Sum(nil))); st != 200 || !strings.Contains(out, `"received":true`) {
		t.Fatalf("signed: %d %s", st, out)
	}
}

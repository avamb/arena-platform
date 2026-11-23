//go:build integration

package hmetaconnect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (e *itEnv) webhook(body, sig string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/meta/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	e.h.HandleWebhook(w, req)
	return w
}

func itHubSign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// TestMetaWebhook_Uninstall_LiveDB: a business_integration_uninstall with a
// valid X-Hub-Signature-256 deletes only the connection to that business
// with its assets (the system audit row stays); another organization's
// connection to another business is untouched, and a bad signature changes
// nothing.
func TestMetaWebhook_Uninstall_LiveDB(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, p := range []struct{ user, org uuid.UUID }{{e.ownerA, e.orgA}, {e.ownerB, e.orgB}} {
		if w := e.login(p.user, p.org, "code-1"); w.Code != 200 {
			t.Fatalf("connect %s: %d %s", p.org, w.Code, w.Body.String())
		}
	}
	// The stub names business 900 for both; B is moved to another business.
	if _, err := e.pool.Exec(ctx, `UPDATE org_meta_connections SET meta_business_id = '901' WHERE org_id = $1`, e.orgB); err != nil {
		t.Fatal(err)
	}
	// state answers status "deleted" when the organization has no connection
	// row; assets counts its asset rows either way.
	state := func(org uuid.UUID) (status string, hasToken bool, assets int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT COALESCE((SELECT status FROM org_meta_connections WHERE org_id = $1), 'deleted'),
		        COALESCE((SELECT access_token_enc IS NOT NULL FROM org_meta_connections WHERE org_id = $1), false),
		        (SELECT count(*) FROM org_meta_assets WHERE org_id = $1)`, org).Scan(&status, &hasToken, &assets); err != nil {
			t.Fatal(err)
		}
		return
	}
	if st, tok, n := state(e.orgA); st != "active" || !tok || n == 0 {
		t.Fatalf("A before: %s token=%v assets=%d", st, tok, n)
	}

	body := `{"object":"application","entry":[{"id":"app-1","time":1700000000,"changes":[` +
		`{"field":"business_integration_uninstall","value":{"business_manager_id":"900","user_id":"` + itMetaUser + `"}}]}]}`
	// Bad and missing signatures: 401, nothing changes.
	for _, sig := range []string{"", itHubSign("wrong", body), strings.TrimPrefix(itHubSign(itSecret, body), "sha256=")} {
		if w := e.webhook(body, sig); w.Code != http.StatusUnauthorized {
			t.Fatalf("bad signature %q: %d %s", sig, w.Code, w.Body.String())
		}
	}
	if st, tok, _ := state(e.orgA); st != "active" || !tok {
		t.Fatalf("a bad signature changed A: %s token=%v", st, tok)
	}

	if w := e.webhook(body, itHubSign(itSecret, body)); w.Code != 200 || !strings.Contains(w.Body.String(), `"received":true`) {
		t.Fatalf("uninstall: %d %s", w.Code, w.Body.String())
	}
	if st, tok, n := state(e.orgA); st != "deleted" || tok || n != 0 {
		t.Fatalf("A after uninstall: %s token=%v assets=%d", st, tok, n)
	}
	if st, tok, n := state(e.orgB); st != "active" || !tok || n == 0 {
		t.Fatalf("B touched by A's uninstall: %s token=%v assets=%d", st, tok, n)
	}
	var audits int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND action = 'v1.org.meta.uninstall' AND actor_type = 'system'`,
		e.orgA.String()).Scan(&audits)
	if audits != 1 {
		t.Fatalf("uninstall audit rows = %d", audits)
	}
	var leak int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1 AND metadata::text LIKE '%' || $2 || '%'`,
		e.orgA.String(), itToken).Scan(&leak)
	if leak != 0 {
		t.Fatal("audit carries the token")
	}

	// A repeat, an ignored field and business_id as a number: 200 each; the
	// number form deletes B.
	if w := e.webhook(body, itHubSign(itSecret, body)); w.Code != 200 {
		t.Fatalf("repeat: %d", w.Code)
	}
	other := `{"object":"application","entry":[{"changes":[{"field":"permissions","value":{"business_manager_id":"901"}}]}]}`
	if w := e.webhook(other, itHubSign(itSecret, other)); w.Code != 200 {
		t.Fatalf("ignored field: %d", w.Code)
	}
	if st, _, _ := state(e.orgB); st != "active" {
		t.Fatalf("an ignored field changed B: %s", st)
	}
	byID := `{"object":"application","entry":[{"changes":[{"field":"business_integration_uninstall","value":{"business_id":901}}]}]}`
	if w := e.webhook(byID, itHubSign(itSecret, byID)); w.Code != 200 {
		t.Fatalf("business_id: %d", w.Code)
	}
	if st, tok, n := state(e.orgB); st != "deleted" || tok || n != 0 {
		t.Fatalf("B after business_id uninstall: %s token=%v assets=%d", st, tok, n)
	}
}

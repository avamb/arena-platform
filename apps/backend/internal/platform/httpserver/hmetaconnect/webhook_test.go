package hmetaconnect

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

const unitSecret = "unit-app-secret"

func unitHandler(t *testing.T, verifyToken string) *Handler {
	t.Helper()
	box, err := secretbox.ParseKeys("v1:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	edges, err := metaconnect.ParseEdges("dataset:{business}/owned_pixels")
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, nil, Settings{
		Enabled: true, AppID: "app-1", AppSecret: unitSecret, ConfigID: "cfg-1", GraphVersion: "v25.0",
		RedirectURL: "https://api.example.test/cb", EventCenterURL: "https://events.example.test",
		WebhookVerifyToken: verifyToken, Edges: edges,
	}, box, metaconnect.NewClient("http://127.0.0.1:1", "v25.0", "app-1", unitSecret, nil), nil)
}

func unitSign(body string) string {
	mac := hmac.New(sha256.New, []byte(unitSecret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookVerify(t *testing.T) {
	h := unitHandler(t, "verify-tok")
	get := func(h *Handler, q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.HandleWebhookVerify(w, httptest.NewRequest(http.MethodGet, "/v1/integrations/meta/webhook?"+q, nil))
		return w
	}
	w := get(h, "hub.mode=subscribe&hub.verify_token=verify-tok&hub.challenge=12345")
	if w.Code != 200 || w.Body.String() != "12345" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("good check: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	for _, q := range []string{
		"hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=1",
		"hub.mode=other&hub.verify_token=verify-tok&hub.challenge=1",
		"hub.challenge=1",
		"hub.mode=subscribe&hub.verify_token=verify-tok&hub.challenge=%3Cscript%3E",
		"hub.mode=subscribe&hub.verify_token=verify-tok",
	} {
		if w := get(h, q); w.Code != 403 || strings.Contains(w.Body.String(), "verify-tok") {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	// No configured token: every check is refused, even an empty token.
	if w := get(unitHandler(t, ""), "hub.mode=subscribe&hub.verify_token=&hub.challenge=1"); w.Code != 403 {
		t.Errorf("empty config: %d", w.Code)
	}
	off := New(nil, nil, Settings{AppSecret: unitSecret, WebhookVerifyToken: "verify-tok"}, nil, nil, nil)
	if w := get(off, "hub.mode=subscribe&hub.verify_token=verify-tok&hub.challenge=1"); w.Code != 404 || !strings.Contains(w.Body.String(), "meta.disabled") {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}
}

func TestWebhookPost_Signature(t *testing.T) {
	h := unitHandler(t, "")
	post := func(h *Handler, body, sig string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/integrations/meta/webhook", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if sig != "" {
			r.Header.Set("X-Hub-Signature-256", sig)
		}
		w := httptest.NewRecorder()
		h.HandleWebhook(w, r)
		return w
	}
	ignored := `{"object":"application","entry":[{"id":"1","changes":[{"field":"permissions","value":{"business_manager_id":"900"}}]}]}`
	if w := post(h, ignored, unitSign(ignored)); w.Code != 200 || !strings.Contains(w.Body.String(), `"received":true`) {
		t.Fatalf("good signature, ignored field: %d %s", w.Code, w.Body.String())
	}
	// A signed body that is not JSON is still received (Meta would retry otherwise).
	if w := post(h, "not json", unitSign("not json")); w.Code != 200 {
		t.Fatalf("signed non-JSON: %d", w.Code)
	}
	// An uninstall with a valid signature reaches the database: no pool -> 503.
	uninstall := `{"object":"application","entry":[{"changes":[{"field":"business_integration_uninstall","value":{"business_manager_id":"900"}}]}]}`
	if w := post(h, uninstall, unitSign(uninstall)); w.Code != 503 {
		t.Fatalf("uninstall without a database: %d", w.Code)
	}
	for name, sig := range map[string]string{
		"missing":      "",
		"bad":          unitSign(uninstall + " "),
		"no prefix":    strings.TrimPrefix(unitSign(uninstall), "sha256="),
		"sha1 prefix":  "sha1=" + strings.TrimPrefix(unitSign(uninstall), "sha256="),
		"other secret": "sha256=" + strings.Repeat("0", 64),
	} {
		if w := post(h, uninstall, sig); w.Code != 401 || !strings.Contains(w.Body.String(), "meta.invalid_signature") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	big := `{"object":"application","pad":"` + strings.Repeat("x", metaconnect.WebhookBodyLimit) + `"}`
	if w := post(h, big, unitSign(big)); w.Code != 413 {
		t.Errorf("too large: %d", w.Code)
	}
	off := New(nil, nil, Settings{AppSecret: unitSecret}, nil, nil, nil)
	if w := post(off, ignored, unitSign(ignored)); w.Code != 404 || !strings.Contains(w.Body.String(), "meta.disabled") {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}
}

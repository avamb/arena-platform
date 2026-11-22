package metaconnect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testSecret = "app-secret-for-tests"

func TestState_RoundTrip(t *testing.T) {
	org, user := uuid.New(), uuid.New()
	now := time.Unix(1_800_000_000, 0)
	raw, err := SignState(testSecret, org, user, ModeJS, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, testSecret) {
		t.Fatal("state carries the secret")
	}
	st, err := VerifyState(testSecret, raw, now.Add(9*time.Minute))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if st.OrgID != org || st.UserID != user || st.Mode != ModeJS || st.Nonce == "" {
		t.Fatalf("state = %+v", st)
	}
	raw2, _ := SignState(testSecret, org, user, ModeJS, now)
	if raw2 == raw {
		t.Error("two states must differ (nonce)")
	}
}

func TestState_Refusals(t *testing.T) {
	org, user := uuid.New(), uuid.New()
	now := time.Unix(1_800_000_000, 0)
	raw, _ := SignState(testSecret, org, user, ModeRedirect, now)
	p, sig, _ := strings.Cut(raw, ".")

	// A state of another organization, re-signed by nobody: swap the payload.
	foreign, _ := json.Marshal(State{OrgID: uuid.New(), UserID: user, Mode: ModeRedirect, Exp: now.Add(StateTTL).Unix(), Nonce: "x"})
	swapped := base64.RawURLEncoding.EncodeToString(foreign) + "." + sig

	cases := map[string]struct {
		raw    string
		secret string
		at     time.Time
	}{
		"expired":        {raw, testSecret, now.Add(StateTTL + time.Second)},
		"other secret":   {raw, "another-secret", now},
		"tampered sig":   {p + "." + sig[:len(sig)-2] + "AA", testSecret, now},
		"foreign org":    {swapped, testSecret, now},
		"no dot":         {p, testSecret, now},
		"empty":          {"", testSecret, now},
		"empty secret":   {raw, "", now},
		"garbage":        {"a.b", testSecret, now},
		"too far future": {raw, testSecret, now.Add(-time.Hour)},
	}
	for name, c := range cases {
		if _, err := VerifyState(c.secret, c.raw, c.at); !errors.Is(err, ErrStateInvalid) {
			t.Errorf("%s: err = %v, want ErrStateInvalid", name, err)
		}
	}
	if _, err := SignState(testSecret, org, user, "popup", now); err == nil {
		t.Error("an unknown mode must be refused")
	}
}

func signRequest(secret string, payload map[string]any) string {
	b, _ := json.Marshal(payload)
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(p))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)) + "." + p
}

func TestParseSignedRequest(t *testing.T) {
	good := signRequest(testSecret, map[string]any{"algorithm": "HMAC-SHA256", "user_id": "12345", "issued_at": 1700000000})
	sr, err := ParseSignedRequest(good, testSecret)
	if err != nil || sr.UserID != "12345" || sr.IssuedAt != 1700000000 {
		t.Fatalf("good = %+v, %v", sr, err)
	}
	num := signRequest(testSecret, map[string]any{"algorithm": "HMAC-SHA256", "user_id": 987654321})
	if sr, err := ParseSignedRequest(num, testSecret); err != nil || sr.UserID != "987654321" {
		t.Fatalf("numeric user id = %+v, %v", sr, err)
	}
	bad := map[string]string{
		"wrong secret": signRequest("other", map[string]any{"algorithm": "HMAC-SHA256", "user_id": "1"}),
		"wrong alg":    signRequest(testSecret, map[string]any{"algorithm": "HMAC-SHA1", "user_id": "1"}),
		"no alg":       signRequest(testSecret, map[string]any{"user_id": "1"}),
		"no user":      signRequest(testSecret, map[string]any{"algorithm": "HMAC-SHA256"}),
		"no dot":       "abc",
		"empty":        "",
		"tampered":     good[:len(good)-3] + "xyz",
	}
	for name, raw := range bad {
		if _, err := ParseSignedRequest(raw, testSecret); !errors.Is(err, ErrSignedRequestInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ParseSignedRequest(good, ""); err == nil {
		t.Error("an empty app secret must refuse")
	}
}

func TestParseEdges(t *testing.T) {
	def, err := ParseEdges("")
	if err != nil || len(def) != 8 || def[0] != (Edge{KindDataset, "{business}/owned_pixels"}) {
		t.Fatalf("default = %v, %v", def, err)
	}
	got, err := ParseEdges(" page:me/accounts , dataset:{business}/owned_pixels,page:me/accounts")
	if err != nil || len(got) != 2 {
		t.Fatalf("custom = %v, %v", got, err)
	}
	for _, spec := range []string{"pixel:me/x", "dataset:", "dataset:../x", "dataset:me/x?access_token=1", "dataset:https://evil/x", ",", "dataset"} {
		if _, err := ParseEdges(spec); err == nil {
			t.Errorf("%q must be refused", spec)
		}
	}
}

func TestErrorClassify(t *testing.T) {
	e190 := parseGraphError(400, []byte(`{"error":{"message":"Error validating access token: secret-token-xyz","type":"OAuthException","code":190,"error_subcode":463}}`))
	if !IsTokenInvalid(e190) || IsPermissionError(e190) || ErrorCode(e190) != "graph_190" {
		t.Fatalf("190: %v", e190)
	}
	if strings.Contains(e190.Error(), "secret-token-xyz") || strings.Contains(e190.Error(), "validating") {
		t.Errorf("error text repeats Meta's message: %q", e190.Error())
	}
	for _, code := range []int{10, 100, 200, 270, 299} {
		e := &GraphError{HTTPStatus: 403, Code: code}
		if !IsPermissionError(e) || IsTokenInvalid(e) {
			t.Errorf("code %d misclassified", code)
		}
	}
	if IsPermissionError(&GraphError{Code: 300}) || IsPermissionError(errors.New("x")) || IsTokenInvalid(errors.New("x")) {
		t.Error("non permission errors misclassified")
	}
	if ErrorCode(errors.New("boom")) != "graph_unavailable" {
		t.Error("ErrorCode of a transport error")
	}
	if !IsTokenInvalid(errors.Join(errors.New("wrap"), e190)) {
		t.Error("wrapped 190")
	}
}

// graphStub answers a fixed Graph surface and records what it saw.
type graphStub struct {
	t        *testing.T
	token    string
	sawQuery []string
}

func (g *graphStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.sawQuery = append(g.sawQuery, r.URL.RawQuery)
	if strings.Contains(r.URL.RawQuery, "access_token") || strings.Contains(r.URL.RawQuery, "client_secret") || strings.Contains(r.URL.RawQuery, "code=") {
		g.t.Errorf("a secret travelled in the URL: %s", r.URL.String())
	}
	path := strings.TrimPrefix(r.URL.Path, "/v25.0/")
	if path == "oauth/access_token" {
		_ = r.ParseForm()
		if r.Method != http.MethodPost || r.PostForm.Get("client_secret") != testSecret || r.PostForm.Get("code") != "the-code" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":100,"type":"OAuthException"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + g.token + `","token_type":"bearer","expires_in":3600}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+g.token {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":190,"error_subcode":460,"message":"bad ` + r.Header.Get("Authorization") + `"}}`))
		return
	}
	switch path {
	case "me":
		_, _ = w.Write([]byte(`{"id":"555","client_business_id":"777"}`))
	case "777/owned_pixels":
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"111","name":"Pixel A"}],"paging":{"cursors":{"after":"c1"},"next":"https://graph/x"}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":[{"id":"112","name":"Pixel B"}],"paging":{"cursors":{"after":"c2"}}}`))
		}
	case "777/client_pixels":
		_, _ = w.Write([]byte(`{"data":[{"id":"111","name":"Pixel A dup"}]}`))
	case "777/owned_ad_accounts":
		_, _ = w.Write([]byte(`{"data":[{"id":"act_9","name":"Ads","currency":"EUR","timezone_name":"Europe/Madrid"}]}`))
	case "777/client_ad_accounts":
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":200,"type":"OAuthException"}}`))
	default:
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":100}}`))
	}
}

func TestClient_FlowAgainstStub(t *testing.T) {
	stub := &graphStub{t: t, token: "TOKEN-abc-123"}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	c := NewClient(srv.URL, "v25.0", "app1", testSecret, srv.Client())
	ctx := context.Background()

	tok, err := c.ExchangeCode(ctx, "the-code", "")
	if err != nil || tok.AccessToken != stub.token || tok.ExpiresAt == nil {
		t.Fatalf("exchange: %+v %v", tok, err)
	}
	if strings.Contains(tok.String(), stub.token) {
		t.Error("Token.String shows the value")
	}
	if b, _ := json.Marshal(tok); strings.Contains(string(b), stub.token) {
		t.Error("Token JSON shows the value")
	}
	if _, err := c.ExchangeCode(ctx, "wrong", ""); err == nil || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("bad exchange: %v", err)
	}
	me, err := c.Me(ctx, tok.AccessToken)
	if err != nil || me.ID != "555" || me.ClientBusinessID != "777" {
		t.Fatalf("me: %+v %v", me, err)
	}
	edges, _ := ParseEdges("dataset:{business}/owned_pixels,dataset:{business}/client_pixels,ad_account:{business}/owned_ad_accounts,ad_account:{business}/client_ad_accounts")
	assets, skips, err := c.Assets(ctx, tok.AccessToken, me.ClientBusinessID, edges)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 || assets[0].MetaID != "111" || assets[1].MetaID != "112" || assets[2].Currency != "EUR" || assets[2].Timezone != "Europe/Madrid" {
		t.Fatalf("assets = %+v", assets)
	}
	if len(skips) != 1 || skips[0].Reason != "graph_200" {
		t.Fatalf("skips = %+v", skips)
	}
	// Without a business every {business} edge is skipped, not fatal.
	if a, s, err := c.Assets(ctx, tok.AccessToken, "", edges); err != nil || len(a) != 0 || len(s) != 4 || s[0].Reason != "no_business" {
		t.Fatalf("no business: %v %v %v", a, s, err)
	}
	// A wrong token is a 190 and its error never shows the token.
	_, err = c.Me(ctx, "WRONG-token-zzz")
	if !IsTokenInvalid(err) || strings.Contains(err.Error(), "WRONG-token-zzz") {
		t.Fatalf("190 err = %v", err)
	}
	// A 190 on an edge is fatal.
	if _, _, err := c.Assets(ctx, "WRONG-token-zzz", "777", edges); !IsTokenInvalid(err) {
		t.Fatalf("edge 190 = %v", err)
	}
}

func TestClient_TransportErrorHasNoURL(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "v25.0", "app1", testSecret, &http.Client{Timeout: time.Second})
	_, err := c.ExchangeCode(context.Background(), "code-secret-value", "")
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "code-secret-value") {
		t.Fatalf("transport err = %v", err)
	}
}

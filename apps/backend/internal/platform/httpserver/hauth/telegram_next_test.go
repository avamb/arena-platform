package hauth

import (
	"encoding/base64"
	"strings"
	"testing"
)

const nextOrg = "0190aaaa-bbbb-7ccc-8ddd-eeeeffff0001"

func TestSafeMiniappNext(t *testing.T) {
	seats := "/o/" + nextOrg + "/events/0190aaaa-bbbb-7ccc-8ddd-eeeeffff0002/sessions/0190aaaa-bbbb-7ccc-8ddd-eeeeffff0003/seats?embed=1"
	cases := []struct {
		name, in, want string
	}{
		{"seat manager", seats, seats},
		{"org home", "/o/" + nextOrg, "/o/" + nextOrg},
		{"trailing slash", "/o/" + nextOrg + "/events/", "/o/" + nextOrg + "/events/"},
		{"empty", "", ""},
		{"absolute url", "https://evil.example/o/" + nextOrg, ""},
		{"scheme relative", "//evil.example/o/" + nextOrg, ""},
		{"backslash", "/o/" + nextOrg + "/" + string(rune(92)) + "evil.example", ""},
		{"not an organization path", "/login", ""},
		{"admin path", "/admin/users", ""},
		{"org id is not a uuid", "/o/abc/events", ""},
		{"dot dot", "/o/" + nextOrg + "/../../admin", ""},
		{"single dot", "/o/" + nextOrg + "/./events", ""},
		{"double slash inside", "/o/" + nextOrg + "//evil.example", ""},
		{"encoded slash", "/o/" + nextOrg + "/%2f%2fevil.example", ""},
		{"encoded dots", "/o/" + nextOrg + "/%2e%2e/admin", ""},
		{"fragment", "/o/" + nextOrg + "/events#x", ""},
		{"scheme in query", "/o/" + nextOrg + "/events?next=https://evil.example", ""},
		{"space", "/o/" + nextOrg + "/ev ents", ""},
		{"newline", "/o/" + nextOrg + "/events\n", ""},
		{"too long", "/o/" + nextOrg + "/" + strings.Repeat("a", 300), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeMiniappNext(tc.in); got != tc.want {
				t.Fatalf("SafeMiniappNext(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMiniappNextFromStartParam(t *testing.T) {
	path := "/o/" + nextOrg + "/events/0190aaaa-bbbb-7ccc-8ddd-eeeeffff0002/sessions/0190aaaa-bbbb-7ccc-8ddd-eeeeffff0003/seats?embed=1"
	enc := base64.RawURLEncoding.EncodeToString([]byte(path))
	if strings.ContainsAny(enc, "+/=") {
		t.Fatalf("start_param %q must stay inside Telegram's alphabet", enc)
	}
	if got := MiniappNextFromStartParam(enc); got != path {
		t.Fatalf("got %q, want %q", got, path)
	}
	for name, in := range map[string]string{
		"empty":            "",
		"not base64":       "!!!",
		"padded":           base64.URLEncoding.EncodeToString([]byte("/o/" + nextOrg + "/")),
		"external":         base64.RawURLEncoding.EncodeToString([]byte("https://evil.example")),
		"not a safe path":  base64.RawURLEncoding.EncodeToString([]byte("/o/" + nextOrg + "/../x")),
		"over the maximum": strings.Repeat("A", 600),
	} {
		if got := MiniappNextFromStartParam(in); got != "" {
			t.Fatalf("%s: got %q, want empty", name, got)
		}
	}
}

func TestMiniappNext_SignedStartParamWinsOverBody(t *testing.T) {
	signed := "/o/" + nextOrg + "/events"
	body := "/o/" + nextOrg + "/orders"
	enc := base64.RawURLEncoding.EncodeToString([]byte(signed))
	if got := miniappNext(enc, body); got != signed {
		t.Fatalf("got %q, want the signed one", got)
	}
	if got := miniappNext("", body); got != body {
		t.Fatalf("got %q, want the body's", got)
	}
	// A bad start_param falls back to a good body path, a bad body gives "".
	if got := miniappNext("!!!", body); got != body {
		t.Fatalf("got %q", got)
	}
	if got := miniappNext("", "https://evil.example"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestVerifyInitData_ReturnsTheSignedStartParam(t *testing.T) {
	key := DeriveMiniappSecretKey(testBotToken)
	f := goodFields(testClock, 7)
	f["start_param"] = base64.RawURLEncoding.EncodeToString([]byte("/o/" + nextOrg))
	u, err := VerifyInitData(signedInitData(key, f), key, testClock, InitDataMaxAge)
	if err != nil || u.StartParam != f["start_param"] {
		t.Fatalf("user %+v err %v", u, err)
	}
	// Tampering with the start_param breaks the signature: it is signed.
	raw := signedInitData(key, f)
	forged := strings.Replace(raw, "start_param="+f["start_param"], "start_param="+base64.RawURLEncoding.EncodeToString([]byte("/o/"+nextOrg+"/admin")), 1)
	if forged == raw {
		t.Fatal("test bug: start_param not replaced")
	}
	if _, err := VerifyInitData(forged, key, testClock, InitDataMaxAge); err == nil {
		t.Fatal("a changed start_param must not verify")
	}
}

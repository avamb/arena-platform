package hshop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeCaptcha struct {
	ok    bool
	err   error
	token string
	ip    string
	calls int
}

func (f *fakeCaptcha) Verify(_ context.Context, token, ip string) (bool, error) {
	f.calls++
	f.token, f.ip = token, ip
	return f.ok, f.err
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := rec.Body.String()
	for _, code := range []string{"shop.captcha_required", "shop.captcha_failed", "shop.captcha_unavailable"} {
		if strings.Contains(body, code) {
			return code
		}
	}
	return body
}

func TestCaptchaPassed(t *testing.T) {
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/public/shop/x/checkout", nil)
		r.RemoteAddr = "203.0.113.9:5000"
		return r
	}
	cases := []struct {
		name       string
		captcha    *fakeCaptcha
		token      string
		wantOK     bool
		wantStatus int
		wantCode   string
		wantCalls  int
	}{
		{"no captcha configured", nil, "", true, 0, "", 0},
		{"token missing", &fakeCaptcha{ok: true}, "  ", false, http.StatusUnprocessableEntity, "shop.captcha_required", 0},
		{"token rejected", &fakeCaptcha{ok: false}, "tok", false, http.StatusUnprocessableEntity, "shop.captcha_failed", 1},
		{"check cannot run", &fakeCaptcha{err: errors.New("cloudflare down")}, "tok", false, http.StatusServiceUnavailable, "shop.captcha_unavailable", 1},
		{"token accepted", &fakeCaptcha{ok: true}, " tok ", true, 0, "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := Options{}
			if c.captcha != nil {
				o.Captcha = c.captcha
			}
			h := New(o)
			rec := httptest.NewRecorder()
			ok := h.captchaPassed(rec, req(), c.token)
			if ok != c.wantOK {
				t.Fatalf("captchaPassed = %v; want %v", ok, c.wantOK)
			}
			if !c.wantOK {
				if rec.Code != c.wantStatus || errCode(t, rec) != c.wantCode {
					t.Fatalf("refusal = %d %s; want %d %s", rec.Code, rec.Body.String(), c.wantStatus, c.wantCode)
				}
			}
			if c.captcha != nil {
				if c.captcha.calls != c.wantCalls {
					t.Fatalf("verifier called %d times; want %d", c.captcha.calls, c.wantCalls)
				}
				if c.wantCalls > 0 && (c.captcha.token != strings.TrimSpace(c.token) || c.captcha.ip != "203.0.113.9") {
					t.Fatalf("verifier got token %q ip %q", c.captcha.token, c.captcha.ip)
				}
			}
		})
	}
}

func TestCaptchaSiteKeyOnlyWithASecret(t *testing.T) {
	if got := New(Options{CaptchaSiteKey: "0xAAA"}).captchaSiteKey(); got != "" {
		t.Fatalf("site key without a verifier = %q; want none", got)
	}
	if got := New(Options{Captcha: &fakeCaptcha{}, CaptchaSiteKey: " 0xAAA "}).captchaSiteKey(); got != "0xAAA" {
		t.Fatalf("site key with a verifier = %q; want 0xAAA", got)
	}
}

func TestPublicAvailable(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		name string
		real *int
		max  int32
		want *int
	}{
		{"shelf above the cap", n(100), 24, n(24)},
		{"shelf below the cap", n(5), 24, n(5)},
		{"sold out", n(0), 24, n(0)},
		{"untracked stock offers the cap", nil, 24, n(24)},
		{"no line limit shows the real figure", n(100), 0, n(100)},
		{"no line limit and untracked", nil, 0, nil},
	}
	for _, c := range cases {
		got := publicAvailable(c.real, c.max)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: publicAvailable = %v; want %v", c.name, deref(got), deref(c.want))
		}
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// An IPv6 subscriber is one client: both the checkout budget and the hash
// stored on an order use the /64 prefix, and the hash never holds the address.
func TestClientKeyHashIsPerIPv6Prefix(t *testing.T) {
	h := New(Options{IPSalt: "salt"})
	from := func(remote string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = remote
		return r
	}
	a := h.clientKeyHash(from("[2001:db8:1:2::1]:1"))
	b := h.clientKeyHash(from("[2001:db8:1:2:ffff::9]:1"))
	c := h.clientKeyHash(from("[2001:db8:1:3::1]:1"))
	if a == "" || a != b {
		t.Fatalf("one /64 hashes to %q and %q; want one value", a, b)
	}
	if a == c {
		t.Fatal("another /64 shares the hash")
	}
	if len(a) != 64 || strings.Contains(a, "2001") {
		t.Fatalf("hash = %q; want 64 hex characters", a)
	}
	if h.clientKey(from("[2001:db8:1:2::1]:1")) != "2001:db8:1:2::/64" {
		t.Fatalf("client key = %q", h.clientKey(from("[2001:db8:1:2::1]:1")))
	}
	if x, y := New(Options{IPSalt: "one"}).clientKeyHash(from("203.0.113.9:1")), New(Options{IPSalt: "two"}).clientKeyHash(from("203.0.113.9:1")); x == y {
		t.Fatal("the salt does not change the hash")
	}
	// The checkout budget keys the same way.
	l := newMinuteLimiter(1)
	if ok, _ := l.allow("shop|"+h.clientKey(from("[2001:db8:9:9::1]:1")), time.Now()); !ok {
		t.Fatal("first request refused")
	}
	if ok, _ := l.allow("shop|"+h.clientKey(from("[2001:db8:9:9::2]:1")), time.Now()); ok {
		t.Fatal("a second address of the same /64 has its own budget")
	}
}

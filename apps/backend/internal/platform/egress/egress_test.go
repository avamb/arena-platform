package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func withResolver(t *testing.T, r fakeResolver) {
	t.Helper()
	prev := resolver
	resolver = r
	t.Cleanup(func() { resolver = prev })
}

var deny = Policy{Private: PrivateDeny}

func TestTestBinaryAllowsPrivateByDefault(t *testing.T) {
	if !allowPrivate.Load() {
		t.Fatal("a test binary must allow private destinations by default")
	}
}

func TestPublic(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":                true,
		"2606:4700:4700::1111":   true,
		"178.105.217.195":        true,
		"172.15.255.255":         true,
		"172.32.0.1":             true,
		"100.128.0.1":            true,
		"169.253.255.255":        true,
		"::ffff:8.8.8.8":         true,
		"64:ff9b:2::1":           true,
		"2001:4860::1%2":         true,
		"127.0.0.1":              false,
		"127.8.9.10":             false,
		"::1":                    false,
		"0.0.0.0":                false,
		"0.1.2.3":                false,
		"::":                     false,
		"10.0.0.5":               false,
		"172.16.0.1":             false,
		"192.168.1.1":            false,
		"100.64.0.1":             false,
		"169.254.169.254":        false,
		"fe80::1":                false,
		"fe80::1%eth0":           false,
		"fc00::1":                false,
		"fd12:3456::1":           false,
		"224.0.0.1":              false,
		"ff02::1":                false,
		"255.255.255.255":        false,
		"240.0.0.1":              false,
		"192.0.0.8":              false,
		"192.0.2.7":              false,
		"198.18.0.1":             false,
		"198.51.100.7":           false,
		"203.0.113.7":            false,
		"100::1":                 false,
		"2001:db8::1":            false,
		"::ffff:127.0.0.1":       false,
		"::ffff:169.254.169.254": false,
		"::127.0.0.1":            false,
		"64:ff9b::a00:1":         false,
		"64:ff9b:1::1":           false,
		"2002:a00:1::":           false,
	}
	for s, want := range cases {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if got := Public(a); got != want {
			t.Errorf("Public(%s) = %v, want %v", s, got, want)
		}
	}
	if Public(netip.Addr{}) {
		t.Error("the zero address must not be public")
	}
}

func TestCheckURL_Shape(t *testing.T) {
	withResolver(t, fakeResolver{"good.example": {netip.MustParseAddr("8.8.8.8")}})
	ctx := context.Background()
	ok := []string{
		"https://good.example/api/_wh/tickets",
		"https://good.example:8443/hook?x=1",
		"https://8.8.8.8/hook",
		"  https://good.example/hook  ",
		"https://unresolvable.example/hook", // DNS failure is accepted, the dial guard decides later
	}
	for _, u := range ok {
		if err := deny.CheckURL(ctx, u); err != nil {
			t.Errorf("CheckURL(%q) = %v, want nil", u, err)
		}
	}
	bad := map[string]error{
		"":                               ErrInvalidURL,
		"/relative/path":                 ErrInvalidURL,
		"good.example/hook":              ErrInvalidURL,
		"https://":                       ErrInvalidURL,
		"https://user:pw@good.example/":  ErrInvalidURL,
		"https://user@good.example/":     ErrInvalidURL,
		"https://good.example/#frag":     ErrInvalidURL,
		"https://good.example/x?a=1#/b":  ErrInvalidURL,
		"https://good.example:99999/":    ErrInvalidURL,
		"https://good.example:abc/":      ErrInvalidURL,
		"https://good.example:0/":        ErrInvalidURL,
		"https://good.example:/":         ErrInvalidURL,
		"http://good.example/hook":       ErrSchemeNotAllowed,
		"ftp://good.example/hook":        ErrSchemeNotAllowed,
		"file:///etc/passwd":             ErrInvalidURL,
		"gopher://good.example/":         ErrSchemeNotAllowed,
		"mailto:a@b.c":                   ErrInvalidURL,
		"https://127.0.0.1/hook":         ErrDestinationNotAllowed,
		"https://[::1]/hook":             ErrDestinationNotAllowed,
		"https://169.254.169.254/latest": ErrDestinationNotAllowed,
		"https://10.1.2.3/":              ErrDestinationNotAllowed,
		"https://[::ffff:127.0.0.1]/":    ErrDestinationNotAllowed,
		"https://localhost/hook":         ErrDestinationNotAllowed,
		"https://LOCALHOST./hook":        ErrDestinationNotAllowed,
		"https://api.localhost/hook":     ErrDestinationNotAllowed,
		"https://0.0.0.0/":               ErrDestinationNotAllowed,
		"https://[fe80::1%25eth0]/":      ErrDestinationNotAllowed,
		"https://224.0.0.1/":             ErrDestinationNotAllowed,
		"https://100.64.1.1/":            ErrDestinationNotAllowed,
	}
	for u, want := range bad {
		if err := deny.CheckURL(ctx, u); !errors.Is(err, want) {
			t.Errorf("CheckURL(%q) = %v, want %v", u, err, want)
		}
	}
	if err := Fetch.CheckURL(ctx, "http://good.example/poster.jpg"); err != nil && !errors.Is(err, ErrDestinationNotAllowed) {
		t.Errorf("Fetch must accept http: %v", err)
	}
}

func TestCheckURL_HostnameResolvingInward(t *testing.T) {
	withResolver(t, fakeResolver{
		"inward.example": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.7")},
		"meta.example":   {netip.MustParseAddr("169.254.169.254")},
	})
	for _, u := range []string{"https://inward.example/hook", "https://meta.example/x"} {
		if err := deny.CheckURL(context.Background(), u); !errors.Is(err, ErrDestinationNotAllowed) {
			t.Errorf("CheckURL(%q) = %v, want ErrDestinationNotAllowed", u, err)
		}
	}
}

func TestCheckURL_AllowPrivateSkipsAddressChecks(t *testing.T) {
	p := Policy{Private: PrivateAllow}
	if err := p.CheckURL(context.Background(), "https://127.0.0.1:8443/hook"); err != nil {
		t.Fatalf("PrivateAllow must accept loopback: %v", err)
	}
	if err := p.CheckURL(context.Background(), "http://127.0.0.1/hook"); err != nil {
		t.Fatalf("PrivateAllow is the local-stand mode and accepts plain http: %v", err)
	}
	if err := p.CheckURL(context.Background(), "ftp://127.0.0.1/hook"); !errors.Is(err, ErrSchemeNotAllowed) {
		t.Fatalf("PrivateAllow must still refuse other schemes: %v", err)
	}
}

func TestClient_RefusesLoopbackAtDial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	c := Policy{AllowHTTP: true, Private: PrivateDeny}.Client(5 * time.Second)
	resp, err := c.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the dial to be refused")
	}
	if !errors.Is(err, ErrDestinationNotAllowed) {
		t.Fatalf("err = %v, want ErrDestinationNotAllowed", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("the server received %d requests, want 0", hits.Load())
	}
}

func TestClient_HostnameResolvingToLoopbackIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	c := Policy{AllowHTTP: true, Private: PrivateDeny}.Client(5 * time.Second)
	resp, err := c.Get("http://localhost:" + port + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("localhost must be refused at dial time")
	}
	if !errors.Is(err, ErrDestinationNotAllowed) {
		t.Fatalf("err = %v, want ErrDestinationNotAllowed", err)
	}
}

func TestClient_AllowPrivateReaches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := Policy{AllowHTTP: true, Private: PrivateAllow}.Client(5 * time.Second)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestClient_InheritReadsTheProcessSettingAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c := Policy{AllowHTTP: true}.Client(5 * time.Second) // built while private is allowed

	SetAllowPrivate(false)
	t.Cleanup(func() { SetAllowPrivate(true) })
	resp, err := c.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected refusal after SetAllowPrivate(false)")
	}
	if !errors.Is(err, ErrDestinationNotAllowed) {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_WebhookDoesNotFollowRedirects(t *testing.T) {
	var target atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { target.Add(1) }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()

	c := Policy{AllowHTTP: true, Private: PrivateAllow}.Client(5 * time.Second)
	resp, err := c.Post(src.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want the 307 itself", resp.StatusCode)
	}
	if target.Load() != 0 {
		t.Fatal("the redirect target must not be called")
	}
}

func TestClient_FetchFollowsRedirectsButNotInward(t *testing.T) {
	// Every hop is dialed through the same guard (TestClient_RefusesLoopbackAtDial),
	// so following a redirect cannot reach an address the first hop could not.
	// Here we only prove that Fetch follows and that a non-http(s) hop fails.
	var target atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		target.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL, http.StatusFound)
	}))
	defer src.Close()

	allow := Policy{AllowHTTP: true, MaxRedirects: 5, Private: PrivateAllow}.Client(5 * time.Second)
	resp, err := allow.Get(src.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || target.Load() != 1 {
		t.Fatalf("status=%d hits=%d, want a followed redirect", resp.StatusCode, target.Load())
	}

	// A redirect to a non-http(s) scheme is refused by CheckRedirect.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "ftp://example.com/x")
		w.WriteHeader(http.StatusFound)
	}))
	defer bad.Close()
	resp, err = allow.Get(bad.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to ftp must fail")
	}
}

func TestControl_RejectsEveryForbiddenAddress(t *testing.T) {
	for _, a := range []string{"127.0.0.1:443", "[::1]:443", "169.254.169.254:80", "10.0.0.1:443", "[fe80::1%eth0]:443", "garbage"} {
		if err := deny.control("tcp", a, nil); !errors.Is(err, ErrDestinationNotAllowed) {
			t.Errorf("control(%s) = %v", a, err)
		}
	}
	if err := deny.control("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("control(8.8.8.8) = %v", err)
	}
}

func TestStripURL_DropsTheRequestURL(t *testing.T) {
	c := Policy{Private: PrivateDeny}.Client(2 * time.Second)
	_, err := c.Post("https://127.0.0.1:1/hook/path?token=secret", "application/json", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := StripURL(err)
	if strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "/hook/path") {
		t.Fatalf("StripURL kept the URL: %v", got)
	}
	if !errors.Is(got, ErrDestinationNotAllowed) {
		t.Fatalf("StripURL lost the cause: %v", got)
	}
	plain := errors.New("plain")
	if StripURL(plain) != plain {
		t.Fatal("a non-url error must be returned unchanged")
	}
}

func TestOrigin(t *testing.T) {
	cases := map[string]string{
		"https://user:pw@Site.Example:8443/a/b?token=secret#x": "https://Site.Example:8443",
		"HTTPS://site.example/path":                            "https://site.example",
		"not a url":                                            "",
		"":                                                     "",
	}
	for in, want := range cases {
		if got := Origin(in); got != want {
			t.Errorf("Origin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorsNameNoAddress(t *testing.T) {
	for _, e := range []error{ErrInvalidURL, ErrSchemeNotAllowed, ErrDestinationNotAllowed} {
		for _, leak := range []string{"127.", "169.254", "10.", "localhost"} {
			if strings.Contains(e.Error(), leak) {
				t.Errorf("%v names %q", e, leak)
			}
		}
	}
}

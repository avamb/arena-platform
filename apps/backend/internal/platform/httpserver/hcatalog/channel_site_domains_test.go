package hcatalog

import "testing"

func TestNormalizeSiteDomain(t *testing.T) {
	good := map[string]string{
		"example.com":                         "example.com",
		"  Example.COM ":                      "example.com",
		"https://www.Example.com/tickets?x=1": "www.example.com",
		"http://shop.example.co.il:8080/":     "shop.example.co.il",
		"example.com:443":                     "example.com",
		"example.com.":                        "example.com",
		"example.com/path":                    "example.com",
		"xn--80ak6aa92e.com":                  "xn--80ak6aa92e.com",
		"a-b.example.com":                     "a-b.example.com",
	}
	for in, want := range good {
		got, reason := NormalizeSiteDomain(in)
		if reason != "" || got != want {
			t.Errorf("NormalizeSiteDomain(%q) = %q, %q; want %q", in, got, reason, want)
		}
	}
	bad := []string{
		"", "   ", "localhost", "http://localhost:3000", "app.localhost",
		"127.0.0.1", "http://10.0.0.1/x", "[::1]", "::1", "intranet",
		"-bad.example.com", "bad-.example.com", "under_score.example.com",
		"exa mple.com", "пример.рф", "example.123", "a..b.com",
		"https://",
	}
	for _, in := range bad {
		if got, reason := NormalizeSiteDomain(in); reason == "" {
			t.Errorf("NormalizeSiteDomain(%q) = %q, want a refusal", in, got)
		}
	}
}

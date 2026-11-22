package hsite

import "testing"

func TestValidWebsite(t *testing.T) {
	for s, want := range map[string]bool{
		"https://example.com":      true,
		"http://example.com/a?b=1": true,
		"example.com":              true,
		"www.example.com/path":     true,
		"ftp://example.com":        false,
		"javascript:alert(1)":      false,
		"theatre":                  false,
		"https://":                 false,
		"exa mple.com":             false,
		"https://u:p@example.com":  false,
	} {
		if got := ValidWebsite(s); got != want {
			t.Errorf("ValidWebsite(%q) = %v, want %v", s, got, want)
		}
	}
}

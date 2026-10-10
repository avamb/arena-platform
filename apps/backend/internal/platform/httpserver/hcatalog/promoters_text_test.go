package hcatalog

import (
	"strings"
	"testing"
)

func TestNormalizePromoterAddress(t *testing.T) {
	okLong := strings.Repeat("я", 300)
	cases := []struct {
		in   string
		want string // "" = nil
		ok   bool
	}{
		{"  Dlouhá 12,\n110 00   Praha ", "Dlouhá 12, 110 00 Praha", true},
		{"   ", "", true},
		{okLong, okLong, true},
		{okLong + "я", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizePromoterAddress(c.in)
		if ok != c.ok || (c.want == "") != (got == nil) || (got != nil && *got != c.want) {
			t.Errorf("NormalizePromoterAddress(%.20q) = (%v, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizePromoterWebsite(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = nil
		ok   bool
	}{
		{"partner.example", "https://partner.example", true},
		{" http://partner.example/a?b=1 ", "http://partner.example/a?b=1", true},
		{"https://www.partner.example", "https://www.partner.example", true},
		{"", "", true},
		{"two words.example", "", false},
		{"ftp://partner.example", "", false},
		{"nodot", "", false},
		{"https://", "", false},
		{"javascript:alert(1)", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizePromoterWebsite(c.in)
		if ok != c.ok || (c.want == "") != (got == nil) || (got != nil && *got != c.want) {
			t.Errorf("NormalizePromoterWebsite(%q) = (%v, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

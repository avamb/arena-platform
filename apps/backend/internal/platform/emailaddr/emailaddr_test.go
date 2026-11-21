package emailaddr

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"anna@example.com", true},
		{"Anna.Smith@Example.COM", true},
		{"assistant+fc043@abhteam.com", true},
		{"user@mail.example.co.uk", true},
		{"user@sub-domain.example.org", true},
		{"user@123.example.io", true},
		{"user@example.xn--p1ai", true},
		{"user@пример.рф", true},
		{"o'brien@example.ie", true},

		// F-BR-03: the address Stripe refused after the hold was taken.
		{"assistant+fc043@abhteam.comRUN1109-FC FC-0", false},
		{"assistant+fc043@abhteam.comRUN1109-FC", false},
		{"user@example.c0m", false},
		{"user@example.123", false},
		{"user@example.c", false},
		{"user@example.xn--", false},
		{"", false},
		{" anna@example.com", false},
		{"anna@example.com ", false},
		{"an na@example.com", false},
		{"anna@exa mple.com", false},
		{"anna@example.com\n", false},
		{"annaexample.com", false},
		{"@example.com", false},
		{"anna@", false},
		{"anna@localhost", false},
		{"anna@.example.com", false},
		{"anna@example..com", false},
		{"anna@example.com.", false},
		{"anna@-example.com", false},
		{"anna@example-.com", false},
		{"anna@exam_ple.com", false},
		{"anna@[127.0.0.1]", false},
		{"Anna <anna@example.com>", false},
		{"anna@example.com, bob@example.com", false},
		{`"anna"@example.com`, false},
		{"anna@@example.com", false},
		{strings.Repeat("a", 65) + "@example.com", false},
		{"anna@" + strings.Repeat("a", 64) + ".com", false},
		{"anna@" + strings.Repeat("a.", 130) + "com", false},
	}
	for _, c := range cases {
		if got := Valid(c.in); got != c.want {
			t.Errorf("Valid(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

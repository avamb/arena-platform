package eventslug

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"summer-festival-2026", "summer-festival-2026", true},
		{"  Summer-Festival  ", "summer-festival", true},
		{"a", "a", true},
		{"", "", false},
		{"   ", "", false},
		{"two--hyphens", "", false},
		{"-lead", "", false},
		{"trail-", "", false},
		{"space inside", "", false},
		{"под-названием", "", false},
		{"under_score", "", false},
		{"terms", "", false},
		{strings.Repeat("a", MaxLen), strings.Repeat("a", MaxLen), true},
		{strings.Repeat("a", MaxLen+1), "", false},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Normalize(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

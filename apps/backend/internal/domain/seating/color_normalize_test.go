package seating

import "testing"

// TestNormalizeColor_KeepsLongformAndIsIdempotent guards F-43: a six-digit
// colour must survive unchanged, and normalizing an already-normalized value
// (every Canonicalize does) must not change it again.
func TestNormalizeColor_KeepsLongformAndIsIdempotent(t *testing.T) {
	cases := map[string]string{
		"#e53935": "#e53935",
		"#1E88E5": "#1e88e5",
		" #abc ":  "#aabbcc",
		"#ABC":    "#aabbcc",
		"red":     "red",
		"":        "",
	}
	for in, want := range cases {
		got := normalizeColor(in)
		if got != want {
			t.Errorf("normalizeColor(%q) = %q, want %q", in, got, want)
		}
		if again := normalizeColor(got); again != got {
			t.Errorf("normalizeColor is not idempotent for %q: %q -> %q", in, got, again)
		}
	}
}

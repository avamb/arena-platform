package email

import (
	"strings"
	"testing"
)

// Spec 08_architecture/53: a white label letter names the brand as the
// sender. The address stays the platform's; a name that could inject a
// header line is dropped.
func TestHeaderFrom(t *testing.T) {
	for _, tc := range []struct{ from, name, want string }{
		{"tickets@arenasoldout.com", "", "tickets@arenasoldout.com"},
		{"tickets@arenasoldout.com", "  ", "tickets@arenasoldout.com"},
		{"tickets@arenasoldout.com", "Teatro Kolybel", `"Teatro Kolybel" <tickets@arenasoldout.com>`},
		{"Arena Sold Out <tickets@arenasoldout.com>", "Vino&Co", `"Vino&Co" <tickets@arenasoldout.com>`},
		{"tickets@arenasoldout.com", "Evil\r\nBcc: x@y.z", "tickets@arenasoldout.com"},
	} {
		if got := HeaderFrom(tc.from, tc.name); got != tc.want {
			t.Errorf("HeaderFrom(%q, %q) = %q, want %q", tc.from, tc.name, got, tc.want)
		}
	}
	if got := HeaderFrom("tickets@arenasoldout.com", "Театр"); !strings.HasPrefix(got, "=?utf-8?") || !strings.HasSuffix(got, "<tickets@arenasoldout.com>") {
		t.Errorf("non-ASCII name not encoded: %q", got)
	}
	// A very long brand name is cut, so the encoded line stays short.
	long := HeaderFrom("tickets@arenasoldout.com", strings.Repeat("Театр ", 100))
	if len(long) > 600 || !strings.HasSuffix(long, "<tickets@arenasoldout.com>") {
		t.Errorf("long name not cut: %d bytes", len(long))
	}
}

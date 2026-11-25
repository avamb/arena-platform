package hfeed

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

// The thank-you version is a version a site grant may store, and it is not the
// checkout tick's one (they stand for different visible texts).
func TestThankYouTextVersion_IsItsOwn(t *testing.T) {
	if !marketing.ValidSiteTextVersion(marketing.ThankYouTextVersion) {
		t.Fatalf("%q cannot be stored by a site grant", marketing.ThankYouTextVersion)
	}
	if marketing.ThankYouTextVersion == marketing.TextVersion {
		t.Fatal("the thank-you text must have its own version")
	}
}

func TestCleanCheckoutPageURL(t *testing.T) {
	long := "https://x.example/" + strings.Repeat("a", 2040)
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"  ", "", true},
		{" https://x.example/thanks?a=1 ", "https://x.example/thanks?a=1", true},
		{"http://x.example/", "", false},
		{"https://user:pw@x.example/", "", false},
		{"https://user@x.example/", "", false},
		{"javascript:alert(1)", "", false},
		{"/relative", "", false},
		{long, "", false},
	}
	for _, c := range cases {
		got, ok := cleanCheckoutPageURL(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("cleanCheckoutPageURL(%.40q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

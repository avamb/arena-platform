package publicurl

import "testing"

func TestPage(t *testing.T) {
	cases := []struct {
		name                          string
		base, org, promoter, eventSlg string
		want                          string
	}{
		{"organization event", "https://tickets.example", "acme", "", "show-1", "https://tickets.example/acme/show-1"},
		{"promoter wins over organization", "https://tickets.example", "acme", "kolibel", "show-1", "https://tickets.example/kolibel/show-1"},
		{"no event slug gives the organization page", "https://tickets.example", "acme", "", "", "https://tickets.example/acme"},
		{"no event slug gives the promoter page", "https://tickets.example", "acme", "kolibel", "", "https://tickets.example/kolibel"},
		{"empty base", "", "acme", "kolibel", "show-1", ""},
		{"blank base", "  ", "acme", "", "show-1", ""},
		{"empty organization and promoter slug", "https://tickets.example", "", "", "show-1", ""},
		{"promoter slug alone is enough", "https://tickets.example", "", "kolibel", "show-1", "https://tickets.example/kolibel/show-1"},
		{"trailing slash of the base", "https://tickets.example/", "acme", "", "show-1", "https://tickets.example/acme/show-1"},
		{"several trailing slashes", "https://tickets.example///", "acme", "", "", "https://tickets.example/acme"},
		{"base with a path", "https://example.com/t", "acme", "", "show-1", "https://example.com/t/acme/show-1"},
		{"unsafe characters are escaped", "https://tickets.example", "a b", "", "x/y", "https://tickets.example/a%20b/x%2Fy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Page(c.base, c.org, c.promoter, c.eventSlg); got != c.want {
				t.Fatalf("Page(%q,%q,%q,%q) = %q, want %q", c.base, c.org, c.promoter, c.eventSlg, got, c.want)
			}
		})
	}
}

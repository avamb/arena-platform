package delivery

import "testing"

func TestMyTicketsLinkTarget(t *testing.T) {
	cases := []struct{ page, base, want string }{
		{"https://vinoandco.events/my-tickets/", "https://tickets.example", "https://vinoandco.events/my-tickets/"},
		{"", "https://tickets.example/", "https://tickets.example/my-tickets"},
		{"", "", ""},
		{"  ", " ", ""},
	}
	for _, c := range cases {
		if got := MyTicketsLinkTarget(c.page, c.base); got != c.want {
			t.Errorf("MyTicketsLinkTarget(%q,%q) = %q, want %q", c.page, c.base, got, c.want)
		}
	}
}

func TestMyTicketsLinkURL_PutsTheTokenInTheFragment(t *testing.T) {
	cases := []struct{ target, want string }{
		{"https://vinoandco.events/my-tickets/?lang=he", "https://vinoandco.events/my-tickets/?lang=he#t=abc123"},
		{"https://tickets.example/my-tickets", "https://tickets.example/my-tickets#t=abc123"},
		// A stale t in the query and an old fragment never survive.
		{"https://vinoandco.events/my-tickets/?t=old&lang=he#top", "https://vinoandco.events/my-tickets/?lang=he#t=abc123"},
	}
	for _, c := range cases {
		got, err := MyTicketsLinkURL(c.target, "abc123")
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("MyTicketsLinkURL(%q) = %q, want %q", c.target, got, c.want)
		}
	}
}

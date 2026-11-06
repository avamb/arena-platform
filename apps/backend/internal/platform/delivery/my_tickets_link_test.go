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

func TestMyTicketsLinkURL_PutsTheTokenInQueryParameterT(t *testing.T) {
	got, err := MyTicketsLinkURL("https://vinoandco.events/my-tickets/?lang=he", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://vinoandco.events/my-tickets/?lang=he&t=abc123" {
		t.Errorf("link = %q", got)
	}
}

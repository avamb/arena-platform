package delivery

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

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

func TestMyTicketsLinkMessage_FromOnlyWhenVerified(t *testing.T) {
	out := templates.Rendered{Subject: "s", HTMLBody: "<p>h</p>", TextBody: "t"}
	m := myTicketsLinkMessage("buyer@example.com", " Teatro Kolybel ", " tickets@kolybel.example ", "verified", out)
	if m.From != "tickets@kolybel.example" || m.FromName != "Teatro Kolybel" || m.To != "buyer@example.com" {
		t.Fatalf("verified: %+v", m)
	}
	m = myTicketsLinkMessage("buyer@example.com", "Teatro Kolybel", "tickets@kolybel.example", "pending", out)
	if m.From != "" || m.FromName != "Teatro Kolybel" {
		t.Fatalf("pending: want empty From and the org name, got %+v", m)
	}
	if m = myTicketsLinkMessage("b@example.com", "X", "", "verified", out); m.From != "" {
		t.Fatalf("verified without address: %+v", m)
	}
}

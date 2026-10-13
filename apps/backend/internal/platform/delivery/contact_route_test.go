package delivery

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Owner rule 2026-10-13: personal addresses are never published. A hosted
// page without a public e-mail never falls back to the organization's own
// address; a ticket sold without a hosted page keeps today's behaviour.
func TestContactRouteFor(t *testing.T) {
	const base = "https://tickets.example/"
	for _, c := range []struct {
		name string
		row  gen.TicketContactRoute
		want ContactRoute
	}{
		{"public e-mail wins", gen.TicketContactRoute{PublicEmail: strPtr(" tickets@teatro.example "), HostedPage: true, OrgEmail: strPtr("owner@gmail.example"), PageSlug: strPtr("teatro")},
			ContactRoute{Email: "tickets@teatro.example"}},
		{"hosted page without a public e-mail links the form", gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example"), PageSlug: strPtr("teatro"), EventSlug: strPtr("hamlet")},
			ContactRoute{FormURL: "https://tickets.example/teatro/hamlet#contact"}},
		{"event without a slug links the page itself", gen.TicketContactRoute{HostedPage: true, PageSlug: strPtr("teatro")},
			ContactRoute{FormURL: "https://tickets.example/teatro#contact"}},
		{"no page slug: nothing, never the address", gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example")},
			ContactRoute{}},
		{"no hosted page keeps the organization's address", gen.TicketContactRoute{OrgEmail: strPtr("box@teatro.example"), PageSlug: strPtr("teatro")},
			ContactRoute{Email: "box@teatro.example"}},
		{"no hosted page and no address", gen.TicketContactRoute{PageSlug: strPtr("teatro")}, ContactRoute{}},
	} {
		if got := ContactRouteFor(c.row, base); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := ContactRouteFor(gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example"), PageSlug: strPtr("teatro")}, ""); got != (ContactRoute{}) {
		t.Errorf("without the tickets origin a hosted page gets no link and no address, got %+v", got)
	}
}

// A letter that points to the form goes from the platform, with no Reply-To,
// even when the organization verified a sender of its own.
func TestTicketEmailMessage_ViaFormHasNoOrganizerAddress(t *testing.T) {
	p := Payload{SenderEmail: "tickets@teatro-own.example", SenderVerificationStatus: "verified",
		ContactFormURL: "https://tickets.example/teatro#contact"}
	msg := ticketEmailMessage(p, "buyer@b.example", "s", "<p>h</p>", "t")
	if msg.From != "" || msg.ReplyTo != "" {
		t.Errorf("via the form: From=%q ReplyTo=%q, want both empty", msg.From, msg.ReplyTo)
	}
	p.OrgContactEmail = "tickets@teatro.example"
	p.ContactFormURL = ""
	msg = ticketEmailMessage(p, "buyer@b.example", "s", "<p>h</p>", "t")
	if msg.From != "tickets@teatro-own.example" || msg.ReplyTo != "tickets@teatro.example" {
		t.Errorf("with a public e-mail: From=%q ReplyTo=%q", msg.From, msg.ReplyTo)
	}
}

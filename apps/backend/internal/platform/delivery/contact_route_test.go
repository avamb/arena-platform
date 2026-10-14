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
		{"event without a slug links the page itself", gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example"), PageSlug: strPtr("teatro")},
			ContactRoute{FormURL: "https://tickets.example/teatro#contact"}},
		// The page offers no form when its messages would reach nobody.
		{"hosted page and no address at all: no dead link", gen.TicketContactRoute{HostedPage: true, PageSlug: strPtr("teatro"), EventSlug: strPtr("hamlet")},
			ContactRoute{}},
		// Owner decision 2026-10-13: an organization with only its owner's
		// account e-mail still gets the form, and the address is never printed.
		{"only the owner's account e-mail: the form", gen.TicketContactRoute{HostedPage: true, OwnerReachable: true, PageSlug: strPtr("teatro"), EventSlug: strPtr("hamlet")},
			ContactRoute{FormURL: "https://tickets.example/teatro/hamlet#contact"}},
		{"no hosted page: the owner's account e-mail is never printed", gen.TicketContactRoute{OwnerReachable: true, PageSlug: strPtr("teatro")},
			ContactRoute{}},
		{"no page slug: nothing, never the address", gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example")},
			ContactRoute{}},
		{"no hosted page keeps the organization's address", gen.TicketContactRoute{OrgEmail: strPtr("box@teatro.example"), PageSlug: strPtr("teatro")},
			ContactRoute{Email: "box@teatro.example"}},
		{"no hosted page and no address", gen.TicketContactRoute{PageSlug: strPtr("teatro")}, ContactRoute{}},
		// Owner decision 2026-10-14: a promoter's event takes the owner only,
		// so the organization's own address does not make the form available.
		{"promoter's event with an owner: the promoter page's form", gen.TicketContactRoute{HostedPage: true, ToOwner: true, OwnerReachable: true, OrgEmail: strPtr("box@teatro.example"), PageSlug: strPtr("lumen"), EventSlug: strPtr("hamlet")},
			ContactRoute{FormURL: "https://tickets.example/lumen/hamlet#contact"}},
		{"promoter's event without an owner: no link, never the address", gen.TicketContactRoute{HostedPage: true, ToOwner: true, OrgEmail: strPtr("box@teatro.example"), PageSlug: strPtr("lumen"), EventSlug: strPtr("hamlet")},
			ContactRoute{}},
	} {
		if got := ContactRouteFor(c.row, base, true); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := ContactRouteFor(gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example"), PageSlug: strPtr("teatro")}, "", true); got != (ContactRoute{}) {
		t.Errorf("without the tickets origin a hosted page gets no link and no address, got %+v", got)
	}
}

// When the page does not show its form (production without the Turnstile
// keys) a letter never links it: no dead link, and still no private address.
// A public e-mail and the address of a page-less channel are unaffected.
func TestContactRouteFor_FormNotShown(t *testing.T) {
	const base = "https://tickets.example"
	for _, c := range []struct {
		name string
		row  gen.TicketContactRoute
		want ContactRoute
	}{
		{"hosted page with an organization address: nothing", gen.TicketContactRoute{HostedPage: true, OrgEmail: strPtr("owner@gmail.example"), PageSlug: strPtr("teatro")},
			ContactRoute{}},
		{"hosted page with only an owner: nothing", gen.TicketContactRoute{HostedPage: true, OwnerReachable: true, PageSlug: strPtr("teatro")},
			ContactRoute{}},
		{"public e-mail is still printed", gen.TicketContactRoute{PublicEmail: strPtr("tickets@teatro.example"), HostedPage: true, PageSlug: strPtr("teatro")},
			ContactRoute{Email: "tickets@teatro.example"}},
		{"no hosted page keeps the organization's address", gen.TicketContactRoute{OrgEmail: strPtr("box@teatro.example")},
			ContactRoute{Email: "box@teatro.example"}},
	} {
		if got := ContactRouteFor(c.row, base, false); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
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

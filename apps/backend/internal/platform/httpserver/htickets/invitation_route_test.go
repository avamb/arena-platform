package htickets

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// The first delivery of a complimentary order uses the invitation template and
// honours the delivery mode (spec 46, section 3.4); the choice is by order
// source, so every other order keeps the ticket letter.
func TestInvitationRoute_ByDeliveryMode(t *testing.T) {
	cases := []struct {
		name string
		in   gen.InvitationDeliveryRow
		want deliveryRoute
	}{
		{"guest_email", gen.InvitationDeliveryRow{Source: "complimentary", Delivery: "guest_email", BuyerEmail: "g@x.es", IssuerEmail: "o@x.es"},
			deliveryRoute{template: delivery.TemplateInvitation}},
		{"my_email goes to the issuer", gen.InvitationDeliveryRow{Source: "complimentary", Delivery: "my_email", IssuerEmail: "o@x.es"},
			deliveryRoute{template: delivery.TemplateInvitation, recipient: "o@x.es"}},
		{"my_email without an issuer address sends nothing", gen.InvitationDeliveryRow{Source: "complimentary", Delivery: "my_email"},
			deliveryRoute{template: delivery.TemplateInvitation, skip: true}},
		{"pdf_only sends nothing", gen.InvitationDeliveryRow{Source: "complimentary", Delivery: "pdf_only", IssuerEmail: "o@x.es"},
			deliveryRoute{template: delivery.TemplateInvitation, skip: true}},
		{"a complimentary order issued another way keeps the holder", gen.InvitationDeliveryRow{Source: "complimentary"},
			deliveryRoute{template: delivery.TemplateInvitation}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := invitationRoute(c.in); got != c.want {
				t.Fatalf("route = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestRouteForOrder_OnlyComplimentaryLeavesTheTicketLetter(t *testing.T) {
	for _, source := range []string{"widget", "bil24_gateway", "box_office", "api", ""} {
		got := routeForOrder(gen.InvitationDeliveryRow{Source: source, Delivery: "pdf_only", IssuerEmail: "o@x.es"})
		if got != (deliveryRoute{}) {
			t.Errorf("source %q: route %+v, want the ticket letter", source, got)
		}
	}
	if got := routeForOrder(gen.InvitationDeliveryRow{Source: "complimentary"}); got.template != delivery.TemplateInvitation {
		t.Errorf("complimentary: template %q, want invitation", got.template)
	}
}

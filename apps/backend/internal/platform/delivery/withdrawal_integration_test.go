//go:build integration

// withdrawal_integration_test.go — live-DB proof that the real delivery worker
// prints the "no right of withdrawal" sentence in the buyer's ticket e-mail
// exactly when the SELLER organization is in the EU/EEA, and never for an
// invitation or a complimentary order.
//
// Same connection pattern as presentation_integration_test.go: DATABASE_URL,
// a fresh scratch database, skipped when unset.
package delivery

import (
	"context"
	"strings"
	"testing"
)

const withdrawalSentenceEN = "Tickets for an event on a specific date are not covered by the right of withdrawal."

func TestDeliveryWithdrawalNotice(t *testing.T) {
	// The fixture organization has country EE and no legal address.
	cases := []struct {
		name  string
		setup string // SQL run against the seeded event's organization, "" = none
		order string // SQL run against the seeded order, "" = none
		tmpl  string
		want  bool
	}{
		{name: "EU org country", want: true},
		{name: "EU legal address", setup: `UPDATE organizations SET country = 'GB', legal_address_country = 'ES' WHERE id = (SELECT org_id FROM events WHERE id = $1)`, want: true},
		{name: "Israeli legal address beats EU org country", setup: `UPDATE organizations SET legal_address_country = 'IL' WHERE id = (SELECT org_id FROM events WHERE id = $1)`, want: false},
		{name: "non-EU org country", setup: `UPDATE organizations SET country = 'GB' WHERE id = (SELECT org_id FROM events WHERE id = $1)`, want: false},
		{name: "complimentary order", order: `UPDATE orders SET source = 'complimentary', discount = subtotal, charge = 0, total = 0 WHERE id = $1`, want: false},
		{name: "invitation template", tmpl: TemplateInvitation, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := presentationPool(t)
			ctx := context.Background()
			seed, cleanup := seedPresentationTicket(ctx, t, pool)
			defer cleanup()
			if tc.setup != "" {
				if _, err := pool.Exec(ctx, tc.setup, seed.EventID); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			if tc.order != "" {
				if _, err := pool.Exec(ctx, tc.order, seed.OrderID); err != nil {
					t.Fatalf("order setup: %v", err)
				}
			}
			raw := runDeliveryWithPayload(ctx, t, pool, Payload{
				TicketID: seed.TicketID.String(),
				Locale:   "en",
				Template: tc.tmpl,
			})
			got := strings.Contains(emailTextBody(t, raw), withdrawalSentenceEN)
			if got != tc.want {
				t.Fatalf("withdrawal sentence printed = %v, want %v", got, tc.want)
			}
		})
	}
}

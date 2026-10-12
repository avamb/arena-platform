//go:build integration

// contact_footer_integration_test.go — live-DB proof that an organization
// WITHOUT a contact e-mail prints no contact line (and no placeholder address)
// in the ticket e-mail footer and the PDF footer, while an organization that
// has one keeps printing it exactly as before.
package delivery

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// captureSender records the message instead of sending it. It is not a
// DevOnlySender, so the handler treats the send as real.
type captureSender struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (c *captureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func deliverCaptured(ctx context.Context, t *testing.T, queries *gen.Queries, p Payload) email.Message {
	t.Helper()
	cs := &captureSender{}
	h := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             cs,
		Logger:             testLogger(),
	})
	if err := h(ctx, mustJSON(t, p)); err != nil {
		t.Fatalf("delivery handler: %v", err)
	}
	if len(cs.msgs) != 1 {
		t.Fatalf("captured %d messages, want 1", len(cs.msgs))
	}
	return cs.msgs[0]
}

func TestDeliveryContactFooter(t *testing.T) {
	const contact = "box-office@org-contact.example"
	cases := []struct {
		name        string
		orgContact  string
		wantContact bool
	}{
		{name: "no contact prints no placeholder", orgContact: "", wantContact: false},
		{name: "existing contact still prints", orgContact: contact, wantContact: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := presentationPool(t)
			ctx := context.Background()
			seed, cleanup := seedPresentationTicket(ctx, t, pool)
			defer cleanup()

			msg := deliverCaptured(ctx, t, gen.New(pool), Payload{
				TicketID:        seed.TicketID.String(),
				Locale:          "en",
				OrgContactEmail: tc.orgContact,
			})
			doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

			for part, body := range map[string]string{"html": msg.HTMLBody, "text": msg.TextBody} {
				if strings.Contains(body, templates.PlatformContactEmail) {
					t.Errorf("%s body prints the placeholder address", part)
				}
				if strings.Contains(body, "Contact:") != tc.wantContact {
					t.Errorf("%s body prints a contact line = %v, want %v", part, !tc.wantContact, tc.wantContact)
				}
				if tc.wantContact && !strings.Contains(body, contact) {
					t.Errorf("%s body lost the organization's contact", part)
				}
			}
			if pdfContainsText(doc, templates.PlatformContactEmail) {
				t.Error("PDF prints the placeholder address")
			}
			if got := pdfContainsText(doc, contact); got != tc.wantContact {
				t.Errorf("PDF prints the organization's contact = %v, want %v", got, tc.wantContact)
			}
		})
	}
}

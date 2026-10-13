//go:build integration

// seller_ids_integration_test.go — HP-4b on a live database: the ticket
// letter and the PDF print the seller's registration and tax numbers, and
// the contact e-mail is the public e-mail of the sales page the ticket was
// sold through, falling back to the organization's contact_email.

package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestDeliverySellerIDs_PublicContactAndNumbers(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	var orgID, channelID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT cs.org_id, cs.channel_id FROM tickets t JOIN checkout_sessions cs ON cs.id = t.checkout_session_id WHERE t.id = $1`,
		seed.TicketID).Scan(&orgID, &channelID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET legal_name = 'Teatro Prueba S.L.', contact_email = 'owner.personal@example.com',
	        registration_number = 'B12345678', tax_id = 'ESB12345678', tax_id_scheme = 'eu_vat' WHERE id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
	q := gen.New(pool)

	// Without a public e-mail on the sales page the organization's address
	// is kept (the old behaviour).
	b, err := q.GetOrgBrandingByTicketID(ctx, seed.TicketID)
	if err != nil {
		t.Fatal(err)
	}
	if b.ContactEmail == nil || *b.ContactEmail != "owner.personal@example.com" {
		t.Errorf("fallback contact = %v", b.ContactEmail)
	}
	if b.RegistrationNumber == nil || *b.RegistrationNumber != "B12345678" || b.TaxID == nil || *b.TaxID != "ESB12345678" ||
		b.TaxIDScheme == nil || *b.TaxIDScheme != "eu_vat" {
		t.Errorf("numbers = %v %v %v", b.RegistrationNumber, b.TaxID, b.TaxIDScheme)
	}

	// With one, the letter names the public address instead.
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = jsonb_build_object('hosted_page',
	        jsonb_build_object('enabled', true, 'public_contact', jsonb_build_object('email', ' tickets@teatro.example '))) WHERE id = $1`, channelID); err != nil {
		t.Fatal(err)
	}
	b, err = q.GetOrgBrandingByTicketID(ctx, seed.TicketID)
	if err != nil {
		t.Fatal(err)
	}
	if b.ContactEmail == nil || *b.ContactEmail != "tickets@teatro.example" {
		t.Fatalf("public contact = %v", b.ContactEmail)
	}

	p := Payload{TicketID: seed.TicketID.String(), Locale: "en", OrgName: b.Name}
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	p.OrgLegalName = str(b.LegalName)
	p.OrgContactEmail = str(b.ContactEmail)
	p.OrgRegistrationNumber = str(b.RegistrationNumber)
	p.OrgTaxID = str(b.TaxID)
	p.OrgTaxIDScheme = str(b.TaxIDScheme)

	raw := runDeliveryWithPayload(ctx, t, pool, p)
	text := emailTextBody(t, raw)
	for _, want := range []string{"Registration no. B12345678 · VAT ID ESB12345678", "tickets@teatro.example"} {
		if !strings.Contains(text, want) {
			t.Errorf("letter misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(raw, "owner.personal@example.com") {
		t.Error("the letter must not carry the organization's own address when the page has a public one")
	}
	doc := pdfDrawnText(storedTicketPDF(ctx, t, pool, seed.TicketID))
	for _, want := range []string{"B12345678", "ESB12345678", "tickets@teatro.example"} {
		if !strings.Contains(doc, want) {
			t.Errorf("PDF misses %q", want)
		}
	}
}

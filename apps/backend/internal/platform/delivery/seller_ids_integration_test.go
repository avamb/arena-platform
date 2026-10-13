//go:build integration

// seller_ids_integration_test.go — HP-4b on a live database: the ticket
// letter and the PDF print the seller's registration and tax numbers, and the
// contact follows the owner rule "personal addresses are never published"
// (ContactRoute): the public e-mail of the sales page the ticket was sold
// through; for a hosted page without one, the page's contact form and no
// Reply-To; without a hosted page, the organization's contact_email as before.

package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const personalEmail = "owner.personal@example.com"

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
	if _, err := pool.Exec(ctx, `UPDATE organizations SET legal_name = 'Teatro Prueba S.L.', contact_email = $2,
	        sender_email = 'tickets@teatro-own.example', sender_verification_status = 'verified',
	        registration_number = 'B12345678', tax_id = 'ESB12345678', tax_id_scheme = 'eu_vat' WHERE id = $1`, orgID, personalEmail); err != nil {
		t.Fatal(err)
	}
	setChannel := func(settings string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = $2::jsonb WHERE id = $1`, channelID, settings); err != nil {
			t.Fatal(err)
		}
	}
	q := gen.New(pool)

	// ── no hosted page: the organization's address is kept (unchanged) ───────
	setChannel(`{}`)
	b, err := q.GetOrgBrandingByTicketID(ctx, seed.TicketID)
	if err != nil {
		t.Fatal(err)
	}
	if b.ContactEmail == nil || *b.ContactEmail != personalEmail {
		t.Errorf("without a hosted page the contact = %v, want the organization's", b.ContactEmail)
	}
	if b.RegistrationNumber == nil || *b.RegistrationNumber != "B12345678" || b.TaxID == nil || *b.TaxID != "ESB12345678" ||
		b.TaxIDScheme == nil || *b.TaxIDScheme != "eu_vat" {
		t.Errorf("numbers = %v %v %v", b.RegistrationNumber, b.TaxID, b.TaxIDScheme)
	}
	route, err := q.GetTicketContactRoute(ctx, seed.TicketID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ContactRouteFor(route, testTicketsBaseURL, true); got.Email != personalEmail || got.FormURL != "" {
		t.Errorf("route without a hosted page = %+v", got)
	}

	// ── a hosted page without a public e-mail: the form, never the address ───
	setChannel(`{"hosted_page": {"enabled": true}}`)
	b, err = q.GetOrgBrandingByTicketID(ctx, seed.TicketID)
	if err != nil {
		t.Fatal(err)
	}
	if b.ContactEmail != nil {
		t.Errorf("a hosted page without a public e-mail must not get the organization's address, got %q", *b.ContactEmail)
	}
	formURL := expectedFormURL(ctx, t, pool, seed.TicketID)

	// The payload still carries the personal address, as a job queued before
	// this rule would: the handler corrects it when the letter is rendered.
	p := sellerPayload(b, seed.TicketID)
	p.OrgContactEmail = personalEmail
	raw := runDeliveryWithPayload(ctx, t, pool, p)
	if strings.Contains(raw, personalEmail) {
		t.Error("the letter of a hosted page without a public e-mail carries the organization's own address")
	}
	if strings.Contains(raw, "tickets@teatro-own.example") {
		t.Error("a letter that points to the contact form must go from the platform address")
	}
	if hasHeader(raw, "Reply-To") {
		t.Error("a letter that points to the contact form must have no Reply-To")
	}
	text := emailTextBody(t, raw)
	for _, want := range []string{"Registration no. B12345678 · VAT ID ESB12345678", formURL} {
		if !strings.Contains(text, want) {
			t.Errorf("letter misses %q:\n%s", want, text)
		}
	}
	doc := pdfDrawnText(storedTicketPDF(ctx, t, pool, seed.TicketID))
	if strings.Contains(doc, personalEmail) || !strings.Contains(doc, "#contact") {
		t.Errorf("PDF contact: want the form link, never the personal address:\n%s", doc)
	}

	// ── with a public e-mail the letter names it, and replies go there ────────
	setChannel(`{"hosted_page": {"enabled": true, "public_contact": {"email": " tickets@teatro.example "}}}`)
	b, err = q.GetOrgBrandingByTicketID(ctx, seed.TicketID)
	if err != nil {
		t.Fatal(err)
	}
	if b.ContactEmail == nil || *b.ContactEmail != "tickets@teatro.example" {
		t.Fatalf("public contact = %v", b.ContactEmail)
	}
	resetDelivery(ctx, t, pool, seed.TicketID)
	raw = runDeliveryWithPayload(ctx, t, pool, sellerPayload(b, seed.TicketID))
	text = emailTextBody(t, raw)
	if !strings.Contains(text, "tickets@teatro.example") || strings.Contains(text, formURL) {
		t.Errorf("letter contact:\n%s", text)
	}
	if !hasHeader(raw, "Reply-To") {
		t.Error("replies must go to the public e-mail")
	}
	if strings.Contains(raw, personalEmail) {
		t.Error("the letter must not carry the organization's own address when the page has a public one")
	}
	doc = pdfDrawnText(storedTicketPDF(ctx, t, pool, seed.TicketID))
	for _, want := range []string{"B12345678", "ESB12345678", "tickets@teatro.example"} {
		if !strings.Contains(doc, want) {
			t.Errorf("PDF misses %q", want)
		}
	}
}

// sellerPayload copies the branding row onto a delivery payload, as the
// enqueuers do.
func sellerPayload(b gen.OrgBrandingRow, ticketID uuid.UUID) Payload {
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	return Payload{
		TicketID: ticketID.String(), Locale: "en", OrgName: b.Name,
		OrgLegalName:             str(b.LegalName),
		OrgContactEmail:          str(b.ContactEmail),
		OrgRegistrationNumber:    str(b.RegistrationNumber),
		OrgTaxID:                 str(b.TaxID),
		OrgTaxIDScheme:           str(b.TaxIDScheme),
		SenderEmail:              str(b.SenderEmail),
		SenderVerificationStatus: b.SenderVerificationStatus,
	}
}

// expectedFormURL is the hosted page of the ticket's event with the form
// anchor, built from the rows the way ContactRouteFor must build it.
func expectedFormURL(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) string {
	t.Helper()
	var orgSlug string
	var eventSlug *string
	if err := pool.QueryRow(ctx, `SELECT o.slug, e.slug FROM tickets t JOIN sessions s ON s.id = t.session_id
	        JOIN events e ON e.id = s.event_id JOIN organizations o ON o.id = e.org_id WHERE t.id = $1`, ticketID).Scan(&orgSlug, &eventSlug); err != nil {
		t.Fatal(err)
	}
	u := testTicketsBaseURL + "/" + orgSlug
	if eventSlug != nil && *eventSlug != "" {
		u += "/" + *eventSlug
	}
	return u + "#contact"
}

// resetDelivery lets the same ticket be delivered again: its job pending and
// its stored PDF gone, so the next run renders the new contact.
func resetDelivery(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'pending', attempts = 0, sent_at = NULL, processing_at = NULL WHERE ticket_id = $1`, ticketID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ticket_credentials WHERE ticket_id = $1 AND type = 'pdf'`, ticketID); err != nil {
		t.Fatal(err)
	}
}

// hasHeader reports whether the raw message carries the header (case-insensitive name).
func hasHeader(raw, name string) bool {
	head := raw
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		head = raw[:i]
	}
	for _, line := range strings.Split(head, "\r\n") {
		if k, _, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return true
		}
	}
	return false
}

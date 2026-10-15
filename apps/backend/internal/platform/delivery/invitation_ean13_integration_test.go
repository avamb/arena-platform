//go:build integration

package delivery

import (
	"bytes"
	"context"
	"testing"
)

// pdfDrawsRun reports whether the document draws exactly the text run s (a
// whole show-text operand, UTF-16BE like every run set in the embedded font).
// pdfContainsText is not used here: it scans every run in one pass and the
// three short digit runs of the barcode do not survive that scan intact.
func pdfDrawsRun(doc []byte, s string) bool {
	run := append([]byte("("), utf16beEscapedPDF(s)...)
	run = append(run, ')')
	return bytes.Contains(doc, run)
}

// U-3: the invitation e-mail attaches the same PDF layout as a bought ticket,
// so a ticket that carries an ean13 credential prints the EAN-13 symbol's
// digits (lead digit, left half, right half) and one without it prints none.
func TestDeliveryInvitation_PrintsTheEAN13WhenTheTicketHasACredential(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
		Template: TemplateInvitation,
	})
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	// gofpdf draws the symbol's digits as three runs: 1 + 6 + 6.
	for _, run := range []string{seed.EAN13[:1], seed.EAN13[1:7], seed.EAN13[7:]} {
		if !pdfDrawsRun(doc, run) {
			t.Errorf("the invitation PDF does not print the EAN-13 digits %q of %s", run, seed.EAN13)
		}
	}
	// The Price cell carries the word, not an amount.
	if !pdfDrawsRun(doc, "Invitation") {
		t.Error("the invitation PDF does not print the Invitation word in the Price cell")
	}
}

// The control that makes the test above mean something: with no credential
// (what every invitation was before U-3) the PDF has no barcode digits.
func TestDeliveryInvitation_WithoutACredentialPrintsNoBarcode(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx,
		`DELETE FROM ticket_credentials WHERE ticket_id = $1 AND type = 'ean13'`, seed.TicketID,
	); err != nil {
		t.Fatalf("drop the ean13 credential: %v", err)
	}
	runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
		Template: TemplateInvitation,
	})
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	if pdfDrawsRun(doc, seed.EAN13[1:7]) || pdfDrawsRun(doc, seed.EAN13[7:]) {
		t.Error("a ticket with no ean13 credential printed barcode digits")
	}
}

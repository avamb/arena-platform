package pdf

import (
	"bytes"
	"context"
	"testing"
)

// U-3: an invitation is the SAME ticket as a bought one - EAN-13 symbol and QR
// included - and only the Price cell differs: the locale's "Invitation" word
// stands where the amount would be.

func invitationTicket(t *testing.T) Ticket {
	t.Helper()
	tk := validTicket(t)
	tk.PriceMinor = nil
	tk.Currency = ""
	tk.Invitation = true
	// No logo and no poster, so the only image on the page is the QR.
	tk.OrgLogo = nil
	tk.PosterImage = nil
	return tk
}

func TestInvitation_PrintsTheWordInEveryLocaleAndTheSameCodesAsATicket(t *testing.T) {
	for _, loc := range SupportedLocales {
		t.Run(loc, func(t *testing.T) {
			tk := invitationTicket(t)
			tk.Locale = loc
			word := stringsFor(loc).Invitation
			if word == "" {
				t.Fatalf("locale %s has no Invitation word", loc)
			}
			out, err := Render(context.Background(), tk)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !bytes.Contains(out, pdfText(word)) {
				t.Errorf("the invitation PDF does not print %q in the Price cell", word)
			}
			// The barcode's human-readable digit groups: lead digit, then 6 + 6.
			for _, want := range []string{tk.EAN13[:1], tk.EAN13[1:7], tk.EAN13[7:13]} {
				if !bytes.Contains(out, pdfText(want)) {
					t.Errorf("the invitation PDF does not print the EAN-13 digits %q", want)
				}
			}
			// And the QR: the one raster on the page.
			if n := len(drawnImages(t, out)); n != 1 {
				t.Errorf("the invitation PDF draws %d images, want exactly the QR", n)
			}
		})
	}
}

// A bought ticket never prints the word, and an invitation never prints a
// price, even one the caller left on the struct.
func TestInvitation_WordOnlyOnAnInvitation(t *testing.T) {
	bought := validTicket(t)
	out, err := Render(context.Background(), bought)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, pdfText(stringsFor("en").Invitation)) {
		t.Error("a bought ticket printed the Invitation word")
	}

	inv := validTicket(t) // still carries PriceMinor 600 CZK
	inv.Invitation = true
	out, err = Render(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, pdfText(stringsFor("en").Invitation)) {
		t.Error("the invitation did not print its word")
	}
	if bytes.Contains(out, pdfText("600 CZK")) {
		t.Error("the invitation printed a price")
	}
}

func TestInfoRows_InvitationReplacesThePriceAndExplicitLabelStillWins(t *testing.T) {
	en := stringsFor("en")
	rows := infoRows(Ticket{TierName: "VIP", Invitation: true}, en)
	if len(rows) != 1 || len(rows[0]) != 2 || rows[0][1].label != en.Price || rows[0][1].value != en.Invitation {
		t.Fatalf("rows = %+v, want Category + Price(Invitation)", rows)
	}
	rows = infoRows(Ticket{TierName: "VIP", Invitation: true, PriceLabel: "Gift"}, en)
	if rows[0][1].value != "Gift" {
		t.Fatalf("PriceLabel must win, got %q", rows[0][1].value)
	}
}

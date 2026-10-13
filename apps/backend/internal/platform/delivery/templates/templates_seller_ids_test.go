package templates

import (
	"strings"
	"testing"
)

// HP-4b: the seller's registration and tax numbers are printed in the footer
// of every buyer letter, in the letter's language, and nothing is printed
// when neither is given.
func TestRender_SellerIDsInEveryBuyerLetter(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	withIDs := func(d Data) Data {
		d.Branding.LegalName = "Teatro Ejemplo S.L."
		d.Branding.RegistrationNumber = "B12345678"
		d.Branding.TaxID = "ESB12345678"
		d.Branding.TaxIDScheme = "eu_vat"
		return d
	}
	base := Data{TicketID: "id", EventName: "E", RecipientEmail: "x@y.z"}
	kinds := map[string]Data{
		TemplateKindTicket:     base,
		TemplateKindInvitation: base,
		TemplateKindChange:     changeTestData(),
		TemplateKindCancel:     changeTestData(),
		TemplateKindRefund:     refundData(),
	}
	want := map[string]string{
		"en": "Registration no. B12345678 · VAT ID ESB12345678",
		"ru": "Регистрационный номер B12345678 · Номер плательщика НДС ESB12345678",
		"es": "N.º de registro B12345678 · ID de IVA ESB12345678",
		"he": "מספר רישום B12345678",
	}
	for kind, d := range kinds {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, withIDs(d))
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for _, body := range []string{out.TextBody, out.HTMLBody} {
				if !strings.Contains(body, "B12345678") || !strings.Contains(body, "ESB12345678") {
					t.Errorf("%s/%s: seller numbers missing", kind, loc)
				}
			}
			if w, ok := want[loc]; ok && !strings.Contains(out.TextBody, w) {
				t.Errorf("%s/%s: want %q in the text body", kind, loc, w)
			}
			plain, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(plain.TextBody, "Registration no.") || strings.Contains(plain.TextBody, " · VAT") {
				t.Errorf("%s/%s: an IDs line without numbers", kind, loc)
			}
		}
	}
}

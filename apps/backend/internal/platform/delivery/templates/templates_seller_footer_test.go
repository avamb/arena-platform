package templates

import (
	"html"
	"strings"
	"testing"
)

// Spec 51 section 2: every buyer letter names the organization that received
// the money as the seller, and the payment provider that processed it, in the
// letter's language. A letter without a provider (an invitation, a free
// order) prints no payment line.
func TestRender_SellerAndPaymentProviderInEveryBuyerLetter(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	base := Data{TicketID: "id", EventName: "E", RecipientEmail: "x@y.z"}
	kinds := map[string]Data{
		TemplateKindTicket:            base,
		TemplateKindInvitation:        base,
		TemplateKindChange:            changeTestData(),
		TemplateKindCancel:            changeTestData(),
		TemplateKindRefund:            refundData(),
		TemplateKindShopOrderReceived: shopTestData(),
		TemplateKindShopOrderShipped:  shopTestData(),
		TemplateKindShopOrderRefunded: shopRefundTestData(),
	}
	if len(sellerFooterWords) != len(SupportedLocales) {
		t.Fatalf("sellerFooterWords has %d locales, SupportedLocales %d", len(sellerFooterWords), len(SupportedLocales))
	}
	for kind, d := range kinds {
		for _, loc := range SupportedLocales {
			words, ok := sellerFooterWords[loc]
			if !ok {
				t.Fatalf("no footer words for %s", loc)
			}
			withProvider := d
			withProvider.Branding.LegalName = "Teatro Ejemplo S.L."
			withProvider.Branding.PaymentProvider = "Stripe"
			out, err := r.Render(kind, loc, withProvider)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			payment := strings.Replace(words.payment, "%s", "Stripe", 1)
			for name, body := range map[string]string{"text": out.TextBody, "html": html.UnescapeString(out.HTMLBody)} {
				if !strings.Contains(body, words.seller) || !strings.Contains(body, "Teatro Ejemplo S.L.") {
					t.Errorf("%s/%s %s: seller %q missing", kind, loc, name, words.seller)
				}
				if !strings.Contains(body, payment) {
					t.Errorf("%s/%s %s: payment line %q missing", kind, loc, name, payment)
				}
			}
			plain := d
			plain.Branding.LegalName = "Teatro Ejemplo S.L."
			out, err = r.Render(kind, loc, plain)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.TextBody, "Stripe") || strings.Contains(out.HTMLBody, "Stripe") {
				t.Errorf("%s/%s: a payment line without a provider", kind, loc)
			}
		}
	}
}

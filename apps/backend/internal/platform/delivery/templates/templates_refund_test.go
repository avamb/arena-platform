package templates

import (
	"strings"
	"testing"
)

func refundData() Data {
	return Data{
		RecipientEmail: "buyer@example.com",
		HolderName:     "Ana",
		EventName:      "Gala <Night>",
		SessionStart:   "2026-12-19 20:00 (Europe/Madrid)",
		Branding:       Branding{OrgName: "Org", LegalName: "Org SL", ContactEmail: "info@org.example"},
		Accent:         DefaultAccentColor,
		Tickets: []TicketLine{
			{Number: "1001", Tier: "Stalls", Seat: "Row 3 · Seat 5", Price: "25.00 EUR"},
			{Number: "1002", Price: "10.00 EUR"},
		},
		Refund: &RefundData{Total: "35.00 EUR", TicketsCancelled: true},
	}
}

func TestRender_Refund_EveryLocaleStatesAmountTicketsAndTheBankDelay(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindRefund, loc, refundData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		for name, body := range map[string]string{"subject": out.Subject, "html": out.HTMLBody, "text": out.TextBody} {
			if strings.TrimSpace(body) == "" {
				t.Errorf("%s: empty %s", loc, name)
			}
		}
		for _, want := range []string{"35.00 EUR", "25.00 EUR", "10.00 EUR", "1001", "1002", "Ana"} {
			if !strings.Contains(out.HTMLBody, want) || !strings.Contains(out.TextBody, want) {
				t.Errorf("%s: letter lacks %q in html or text", loc, want)
			}
		}
		// The bank-delay sentence appears in every language in its own words:
		// the range 5–10 or, in Hebrew, "5 עד 10"; check the digits.
		if !strings.Contains(out.TextBody, "5") || !strings.Contains(out.TextBody, "10") {
			t.Errorf("%s: no bank delay in the text body", loc)
		}
		if strings.Contains(out.HTMLBody, "<Night>") {
			t.Errorf("%s: event name is not escaped in the html body", loc)
		}
		if loc == "he" && !strings.Contains(out.HTMLBody, `dir="rtl"`) {
			t.Error("he: the html body must be right-to-left")
		}
	}
}

func TestRender_Refund_MoneyOnlyRefundDoesNotSayTheTicketsAreVoid(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d := refundData()
	d.Refund.TicketsCancelled = false
	cancelled, _ := r.Render(TemplateKindRefund, "en", refundData())
	money, err := r.Render(TemplateKindRefund, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	const phrase = "no longer valid for entry"
	if !strings.Contains(cancelled.TextBody, phrase) {
		t.Errorf("a cancelling refund must say the tickets are void:\n%s", cancelled.TextBody)
	}
	if strings.Contains(money.TextBody, phrase) || strings.Contains(money.HTMLBody, phrase) {
		t.Error("a money-only refund must not say the tickets are void")
	}
}

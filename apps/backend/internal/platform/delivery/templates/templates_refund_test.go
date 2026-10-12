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

// Round-2 review R2-5: one letter can cover a ticket that is void and one
// that stays valid; each line says it for itself.
func TestRender_Refund_MarksOnlyTheVoidTickets(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]string{"en": "no longer valid", "ru": "больше не действует", "cs": "již neplatí",
		"de": "nicht mehr gültig", "es": "ya no es válida", "fr": "n'est plus valable", "he": "אינו תקף עוד"}
	for _, loc := range SupportedLocales {
		mark, ok := marks[loc]
		if !ok {
			t.Fatalf("%s: no marker known to the test", loc)
		}
		d := refundData()
		d.Tickets[0].Void = true
		d.Refund.TicketsCancelled, d.Refund.SomeTicketsCancelled = false, true
		out, err := r.Render(TemplateKindRefund, loc, d)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		var line1, line2 string
		for _, l := range strings.Split(out.TextBody, "\n") {
			if strings.Contains(l, "1001") {
				line1 = l
			}
			if strings.Contains(l, "1002") {
				line2 = l
			}
		}
		if !strings.Contains(line1, mark) || strings.Contains(line2, mark) {
			t.Errorf("%s: void marker on the wrong line:\n%q\n%q", loc, line1, line2)
		}
		all, _ := r.Render(TemplateKindRefund, loc, refundData())
		allSentence := ""
		for _, l := range strings.Split(all.TextBody, "\n") {
			if strings.Contains(l, "1001") || strings.Contains(l, "1002") {
				continue
			}
			if strings.Contains(strings.ToLower(l), strings.ToLower(mark)) || (loc == "en" && strings.Contains(l, "no longer valid for entry")) {
				allSentence = l
			}
		}
		if allSentence != "" && strings.Contains(out.TextBody, allSentence) {
			t.Errorf("%s: a mixed letter repeats the all-tickets sentence %q", loc, allSentence)
		}
	}
}

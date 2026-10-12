package templates

import (
	"strings"
	"testing"
)

// withdrawalSentences is the exact sentence each locale prints. It is spelled
// out here on purpose: the test must fail when a locale loses or rewords it.
var withdrawalSentences = map[string]string{
	"en": "Tickets for an event on a specific date are not covered by the right of withdrawal. Your order became binding when you paid.",
	"cs": "Na vstupenky na akci v konkrétní den se nevztahuje právo na odstoupení od smlouvy. Vaše objednávka se stala závaznou okamžikem zaplacení.",
	"de": "Für Eintrittskarten zu einer Veranstaltung an einem bestimmten Termin besteht kein Widerrufsrecht. Ihre Bestellung wurde mit der Zahlung verbindlich.",
	"es": "Las entradas para un evento en una fecha concreta no están sujetas al derecho de desistimiento. Su pedido pasó a ser vinculante en el momento del pago.",
	"fr": "Les billets pour un événement à une date déterminée ne sont pas soumis au droit de rétractation. Votre commande est devenue ferme au moment du paiement.",
	"he": "על כרטיסים לאירוע במועד קבוע לא חלה זכות הביטול. ההזמנה שלכם הפכה למחייבת עם התשלום.",
	"ru": "На билеты на мероприятие в конкретную дату право на отказ от договора не распространяется. Ваш заказ стал обязательным в момент оплаты.",
}

func TestWithdrawalSentences_CoverEverySupportedLocale(t *testing.T) {
	for _, loc := range SupportedLocales {
		if withdrawalSentences[loc] == "" {
			t.Errorf("no withdrawal sentence for supported locale %q", loc)
		}
	}
}

// The sentence is printed, in every language and in both bodies, when the
// seller is in the EU/EEA (Data.WithdrawalNotice), and never otherwise.
func TestRender_WithdrawalNotice(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		data := orderData()
		data.WithdrawalNotice = true
		out, err := r.Render(TemplateKindTicket, loc, data)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		want := withdrawalSentences[loc]
		if !strings.Contains(out.HTMLBody, want) {
			t.Errorf("%s html: withdrawal sentence missing", loc)
		}
		if !strings.Contains(out.TextBody, want) {
			t.Errorf("%s text: withdrawal sentence missing", loc)
		}

		data.WithdrawalNotice = false
		out, err = r.Render(TemplateKindTicket, loc, data)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if strings.Contains(out.HTMLBody, want) || strings.Contains(out.TextBody, want) {
			t.Errorf("%s: withdrawal sentence printed with the flag off", loc)
		}
	}
}

// The sentence appears once per body, also for a letter without a Payment
// block, and the Hebrew row keeps the right-to-left alignment.
func TestRender_WithdrawalNotice_WithoutPaymentAndRTL(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	data := orderData()
	data.Payment = nil
	data.WithdrawalNotice = true
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindTicket, loc, data)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		want := withdrawalSentences[loc]
		if n := strings.Count(out.HTMLBody, want); n != 1 {
			t.Errorf("%s html: sentence printed %d times, want 1", loc, n)
		}
		if n := strings.Count(out.TextBody, want); n != 1 {
			t.Errorf("%s text: sentence printed %d times, want 1", loc, n)
		}
	}
	out, err := r.Render(TemplateKindTicket, "he", data)
	if err != nil {
		t.Fatal(err)
	}
	row := out.HTMLBody[strings.LastIndex(out.HTMLBody[:strings.Index(out.HTMLBody, withdrawalSentences["he"])], "<tr>"):]
	row = row[:strings.Index(row, "</tr>")]
	if !strings.Contains(row, "text-align:right") {
		t.Errorf("hebrew withdrawal row is not right-aligned: %s", row)
	}
}

// An invitation never carries the sentence, whatever the flag says.
func TestRender_InvitationNeverPrintsWithdrawalNotice(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		data := orderData()
		data.WithdrawalNotice = true
		out, err := r.Render(TemplateKindInvitation, loc, data)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if strings.Contains(out.HTMLBody, withdrawalSentences[loc]) || strings.Contains(out.TextBody, withdrawalSentences[loc]) {
			t.Errorf("%s: invitation printed the withdrawal sentence", loc)
		}
	}
}

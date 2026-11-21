package templates

import (
	"strings"
	"testing"
)

// contactWords are the two visible phrases of the shared block, per locale.
var contactWords = map[string][2]string{
	"en": {"Please do not reply to this e-mail. For any questions write to", "Contact the organizer"},
	"ru": {"Пожалуйста, не отвечайте на это письмо. По любым вопросам пишите на", "Написать организатору"},
	"cs": {"Na tento e-mail prosím neodpovídejte. S jakýmikoli dotazy pište na", "Kontaktovat pořadatele"},
	"de": {"Bitte antworten Sie nicht auf diese E-Mail. Bei Fragen schreiben Sie an", "Veranstalter kontaktieren"},
	"es": {"Por favor, no respondas a este correo. Para cualquier duda escribe a", "Contactar con el organizador"},
	"fr": {"Merci de ne pas répondre à cet e-mail. Pour toute question, écrivez à", "Contacter l’organisateur"},
	"he": {"נא לא להשיב למייל זה. לכל שאלה אפשר לכתוב אל", "יצירת קשר עם המארגן"},
}

// buyerKinds are the letters a buyer receives; every one carries the block.
// The letters to the organizer (alerts, referral payouts and reversals) do not.
var buyerKinds = []string{
	TemplateKindTicket, TemplateKindInvitation, TemplateKindChange, TemplateKindCancel,
	TemplateKindRefund, TemplateKindCheckoutReminder, TemplateKindMyTicketsLink,
	TemplateKindLatePayment, TemplateKindMarketingConfirm,
	TemplateKindSaleSubscribeConfirm, TemplateKindSaleOpen,
	TemplateKindShopOrderReceived, TemplateKindShopOrderShipped,
	TemplateKindShopOrderDelivered, TemplateKindShopOrderRefunded,
	TemplateKindReferralCashbackSent, TemplateKindReferralCashbackUnavailable,
}

var organizerKinds = []string{
	TemplateKindRefundBlocked, TemplateKindScanException, TemplateKindSalesAlert,
	TemplateKindReferralReversalOrganizer, TemplateKindReferralPayoutOrganizer,
}

func contactData() Data {
	d := orderData()
	d.Branding.ContactEmail = ""
	d.Refund = &RefundData{Total: "35.00 EUR", TicketsCancelled: true}
	d.Referral = &ReferralData{Code: "ANA-7K2P", Amount: "5.00 EUR", Order: "1042"}
	d.MyTickets = MyTicketsLinkData{URL: "https://tickets.example.com/my?t=x"}
	d.CartReminder = CartReminderData{URL: "https://api.example.com/resume", Tickets: 1, Total: "10 EUR", OrderNumber: "777"}
	d.LatePayment = LatePaymentData{Total: "10 EUR", OrderNumber: "778"}
	d.Marketing = MarketingConfirmData{ConfirmURL: "https://x.example/c", WithdrawURL: "https://x.example/w"}
	d.SaleNotice = SaleNoticeData{URL: "https://x.example/s"}
	d.Shop = &ShopOrderData{OrderNumber: "S-12", StatusURL: "https://shop.example/o/1"}
	return d
}

// Every buyer letter, in every locale, carries the "do not reply" sentence and
// the mailto button when an address resolved, and nothing when it did not.
func TestRender_ContactBlock_EveryBuyerLetterEveryLocale(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range buyerKinds {
		for _, loc := range SupportedLocales {
			words := contactWords[loc]
			d := contactData()
			d.Contact.Email = "marketing@einatwinery.com"
			out, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for name, body := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
				if !strings.Contains(body, words[0]) || !strings.Contains(body, words[1]) {
					t.Errorf("%s/%s %s: the do-not-reply block is missing", kind, loc, name)
				}
				if !strings.Contains(body, "mailto:marketing@einatwinery.com") {
					t.Errorf("%s/%s %s: the mailto link is missing", kind, loc, name)
				}
			}
			if !strings.Contains(out.HTMLBody, `href="mailto:marketing@einatwinery.com`) {
				t.Errorf("%s/%s: the button is not a mailto link", kind, loc)
			}

			d.Contact.Email = ""
			none, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if strings.Contains(none.HTMLBody, words[0]) || strings.Contains(none.TextBody, words[0]) ||
				strings.Contains(none.HTMLBody, words[1]) || strings.Contains(none.TextBody, "mailto:") {
				t.Errorf("%s/%s: a contact block is printed without an address", kind, loc)
			}
		}
	}
}

func TestRender_ContactBlock_NotInOrganizerLetters(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range organizerKinds {
		d := contactData()
		d.Contact.Email = "marketing@einatwinery.com"
		out, err := r.Render(kind, "en", d)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if strings.Contains(out.HTMLBody, contactWords["en"][0]) || strings.Contains(out.TextBody, contactWords["en"][0]) {
			t.Errorf("%s: an organizer letter carries the buyer block", kind)
		}
	}
}

// The footer's contact e-mail stands in when the sender set no address, so the
// block and the footer never disagree.
func TestRender_ContactBlock_FallsBackToTheBrandingAddress(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d := contactData()
	d.Branding.ContactEmail = "footer@org.example"
	out, err := r.Render(TemplateKindTicket, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.TextBody, "write to footer@org.example.") {
		t.Errorf("the block does not use the footer address:\n%s", out.TextBody)
	}
}

func TestRender_ContactBlock_SubjectCarriesTheOrderNumber(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// An explicit order number wins; a ticket letter takes the payment block's.
	d := contactData()
	d.Contact = BuyerContact{Email: "a@b.example", OrderNumber: "9096"}
	out, _ := r.Render(TemplateKindChange, "en", d)
	if !strings.Contains(out.TextBody, "mailto:a@b.example?subject=Order%209096") {
		t.Errorf("explicit order number not in the subject:\n%s", out.TextBody)
	}
	d = contactData()
	d.Contact.Email = "a@b.example"
	out, _ = r.Render(TemplateKindTicket, "ru", d)
	if !strings.Contains(out.TextBody, "mailto:a@b.example?subject=%D0%97%D0%B0%D0%BA%D0%B0%D0%B7%201001900724") {
		t.Errorf("payment order number not in the subject:\n%s", out.TextBody)
	}
	d.Payment = nil
	out, _ = r.Render(TemplateKindCheckoutReminder, "en", d)
	if !strings.Contains(out.TextBody, "?subject=Order%20777") {
		t.Errorf("reminder order number not in the subject:\n%s", out.TextBody)
	}
	// No order, no subject.
	d = contactData()
	d.Payment = nil
	d.CartReminder, d.LatePayment, d.Shop = CartReminderData{}, LatePaymentData{}, nil
	d.Contact.Email = "a@b.example"
	out, _ = r.Render(TemplateKindMyTicketsLink, "en", d)
	if strings.Contains(out.TextBody, "?subject=") || !strings.Contains(out.TextBody, "mailto:a@b.example") {
		t.Errorf("a letter without an order has a subject:\n%s", out.TextBody)
	}
}

// The old "just reply to this e-mail" sentences are gone from every letter.
func TestRender_NoLetterTellsTheBuyerToReply(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	old := []string{"just reply to this email", "Reply to this e-mail", "просто ответьте", "Ответьте на это", "odpovězte na tento", "Antworten Sie auf diese", "responde a este correo", "Répondez à cet", "השב להודעה", "אפשר פשוט להשיב"}
	for _, kind := range buyerKinds {
		for _, loc := range SupportedLocales {
			d := contactData()
			d.Branding.ContactEmail = "x@y.example"
			out, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range old {
				if strings.Contains(out.HTMLBody, w) || strings.Contains(out.TextBody, w) {
					t.Errorf("%s/%s still says %q", kind, loc, w)
				}
			}
		}
	}
}

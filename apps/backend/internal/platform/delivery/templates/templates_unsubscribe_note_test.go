package templates

import (
	"strings"
	"testing"
)

// unsubscribeNotes is the sentence pair every buyer letter ends with
// (owner decision 2026-10-15, task U-2). Brevo's relay adds a
// List-Unsubscribe header to the letters and arena cannot remove it, so the
// footer says in plain words what pressing "Unsubscribe" does and that arena
// sends no marketing mail without consent. The table is the single source of
// truth for the wording: a locale or a kind that drifts from it fails below.
// The note is plain text, never a link.
var unsubscribeNotes = map[string]string{
	"en": "If you click “Unsubscribe”, the letters with your tickets and the important letters about your order will no longer reach you. We do not send marketing mailings without your consent.",
	"ru": "Если вы нажмёте «Отписаться», письма с билетами и важные письма о вашем заказе к вам больше не придут. Рекламных рассылок без вашего согласия мы не отправляем.",
	"es": "Si pulsas «Cancelar suscripción», dejarás de recibir los correos con tus entradas y los correos importantes sobre tu pedido. No enviamos correos publicitarios sin tu consentimiento.",
	"cs": "Pokud kliknete na „Odhlásit odběr“, přestanou vám chodit e-maily s vašimi vstupenkami i důležité e-maily o vaší objednávce. Reklamní e-maily bez vašeho souhlasu neposíláme.",
	"de": "Wenn Sie auf „Abbestellen“ klicken, erhalten Sie keine E-Mails mit Ihren Tickets und keine wichtigen E-Mails zu Ihrer Bestellung mehr. Werbe-Mailings senden wir nicht ohne Ihre Einwilligung.",
	"fr": "Si vous cliquez sur « Se désabonner », vous ne recevrez plus les e-mails contenant vos billets ni les e-mails importants concernant votre commande. Nous n’envoyons pas de communications commerciales sans votre consentement.",
	"he": "אם תלחצו על ״ביטול הרשמה״, לא תקבלו יותר את המיילים עם הכרטיסים שלכם ואת המיילים החשובים על ההזמנה שלכם. אנחנו לא שולחים דיוורים שיווקיים ללא הסכמתכם.",
}

// buyerLetterKinds are the kinds a buyer receives. The two organizer letters
// (refund_blocked, refund_scan_exception) go to an organization's owners and
// carry no note.
func buyerLetterKinds() map[string]Data {
	base := Data{TicketID: "id", EventName: "E", RecipientEmail: "x@y.z"}
	return map[string]Data{
		TemplateKindTicket:            base,
		TemplateKindInvitation:        base,
		TemplateKindChange:            changeTestData(),
		TemplateKindCancel:            changeTestData(),
		TemplateKindRefund:            refundData(),
		TemplateKindShopOrderReceived: shopTestData(),
		TemplateKindShopOrderShipped:  shopTestData(),
		TemplateKindShopOrderRefunded: shopRefundTestData(),
	}
}

func TestBuyerLetters_EndWithTheUnsubscribeNote(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(unsubscribeNotes) != len(SupportedLocales) {
		t.Fatalf("the table has %d locales, the renderer %d", len(unsubscribeNotes), len(SupportedLocales))
	}
	for _, loc := range SupportedLocales {
		if unsubscribeNotes[loc] == "" {
			t.Fatalf("no note in the table for locale %s", loc)
		}
	}
	for kind, d := range buyerLetterKinds() {
		for _, loc := range SupportedLocales {
			note := unsubscribeNotes[loc]
			out, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if got := strings.Count(out.TextBody, note); got != 1 {
				t.Errorf("%s/%s: the text part carries the note %d times, want 1", kind, loc, got)
			} else if !strings.HasSuffix(strings.TrimSpace(out.TextBody), note) {
				t.Errorf("%s/%s: the note is not the last line of the text part", kind, loc)
			}
			if got := strings.Count(out.HTMLBody, note); got != 1 {
				t.Errorf("%s/%s: the HTML part carries the note %d times, want 1", kind, loc, got)
				continue
			}
			// The last footer line: after the platform line, and no link
			// follows it (the note explains the relay's link, it is not one).
			at := strings.Index(out.HTMLBody, note)
			if i := strings.Index(out.HTMLBody, "Arena Platform"); i < 0 || i > at {
				t.Errorf("%s/%s: the note does not come after the platform line", kind, loc)
			}
			if strings.Contains(out.HTMLBody[at:], "<a ") || strings.Contains(out.HTMLBody[at:], "href") {
				t.Errorf("%s/%s: a link follows the note", kind, loc)
			}
			if strings.Count(out.HTMLBody, "</body>") != 1 {
				t.Errorf("%s/%s: the HTML part must keep exactly one </body> (Brevo injects its pixel there)", kind, loc)
			}
		}
	}
}

// The organizer letters are not for buyers and carry no note.
func TestOrganizerLetters_HaveNoUnsubscribeNote(t *testing.T) {
	for _, kind := range []string{TemplateKindRefundBlocked, TemplateKindScanException} {
		for _, loc := range SupportedLocales {
			raw, err := templateFS.ReadFile(kind + "." + loc + ".tmpl")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), unsubscribeNotes[loc]) {
				t.Errorf("%s.%s is an organizer letter and must not carry the buyer note", kind, loc)
			}
		}
	}
}

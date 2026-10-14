package templates

import (
	"strings"
	"testing"
)

// replyHints is the "just reply to this e-mail" sentence of each letter.
var replyHints = map[string]map[string]string{
	TemplateKindTicket: {
		"en": "If you have any questions, just reply to this email.",
		"ru": "Если у вас есть вопросы, просто ответьте на это письмо.",
		"es": "Si tienes alguna pregunta, responde a este correo.",
		"cs": "V případě dotazů jednoduše odpovězte na tento e-mail.",
		"de": "Bei Fragen antworten Sie einfach auf diese E-Mail.",
		"fr": "Pour toute question, il vous suffit de répondre à cet e-mail.",
		"he": "לכל שאלה אפשר פשוט להשיב למייל זה.",
	},
	TemplateKindRefund: {
		"en": "Questions? Reply to this e-mail.",
		"ru": "Есть вопросы? Ответьте на это письмо.",
		"es": "¿Tienes preguntas? Responde a este correo.",
		"cs": "Máte dotazy? Odpovězte na tento e-mail.",
		"de": "Fragen? Antworten Sie auf diese E-Mail.",
		"fr": "Des questions ? Répondez à cet e-mail.",
		"he": "שאלות? השב להודעה זו.",
	},
}

// A letter tells the buyer to reply only when a reply reaches somebody: the
// organizer's contact address is the letter's Reply-To. Without it the reply
// would land in the platform's no-reply mailbox (found on a test purchase,
// 2026-10-14), so the sentence is left out and the footer's contact form link,
// when there is one, is the way to write.
func TestRender_ReplyHintOnlyWithAContactAddress(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for kind, hints := range replyHints {
		for _, loc := range SupportedLocales {
			hint, ok := hints[loc]
			if !ok {
				t.Fatalf("%s/%s: no reply hint listed in the test", kind, loc)
			}
			d := orderData()
			if kind == TemplateKindRefund {
				d = refundData()
			}
			with, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if !strings.Contains(with.HTMLBody, hint) || !strings.Contains(with.TextBody, hint) {
				t.Errorf("%s/%s: reply hint missing although the letter has a Reply-To", kind, loc)
			}

			d.Branding.ContactEmail = ""
			d.Branding.ContactFormURL = "https://tickets.example.com/abh#contact"
			without, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if strings.Contains(without.HTMLBody, hint) || strings.Contains(without.TextBody, hint) {
				t.Errorf("%s/%s: letter says to reply but has no Reply-To", kind, loc)
			}
		}
	}
}

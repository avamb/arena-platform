package disputes

// messages.go — the texts of the dispute notices, in the organization's
// language (English, Russian, Spanish; any other falls back to English).
// Telegram texts are HTML; every caller-supplied value is escaped. The
// operator's text carries no buyer data.

import (
	"fmt"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

type noticeTexts struct {
	opened, inquiry, due, expired, won, lost, withdrawn string
	order, amount, reason, deadline, lowValue           string
	packageReady, packageBuilding, nextAPI, nextEmail   string
	open, expiredBody, closedBody                       string
	reasons                                             map[string]string
}

var noticeSets = map[string]noticeTexts{
	"en": {
		opened:          "⚠️ <b>Payment dispute opened</b>",
		inquiry:         "⚠️ <b>The bank asks about a payment</b>",
		due:             "⏰ <b>Dispute: %d day(s) left to answer</b>",
		expired:         "❌ <b>The dispute deadline passed without an answer</b>",
		won:             "✅ <b>Dispute won</b>",
		lost:            "❌ <b>Dispute lost</b>",
		withdrawn:       "✅ <b>The dispute was withdrawn</b>",
		order:           "Order %d",
		amount:          "Amount: %s",
		reason:          "Reason: %s",
		deadline:        "Answer by: %s",
		lowValue:        "The amount is below %s. Contesting costs about €20 and is refunded only if you win. We suggest accepting the dispute.",
		packageReady:    "The evidence package is ready.",
		packageBuilding: "The evidence package is being prepared.",
		nextAPI:         "Check the package and press «Submit». Nothing is sent without you.",
		nextEmail:       "Check the package and the letter, then press «Send». Nothing is sent without you.",
		open:            "Open the dispute: %s",
		expiredBody:     "The provider decides the dispute without your evidence.",
		closedBody:      "The provider closed the dispute.",
		reasons: map[string]string{
			"fraud": "the buyer says they did not make the payment", "not_received": "the buyer says they did not get the tickets",
			"not_recognized": "the buyer does not recognise the payment", "refund": "the buyer expected a refund",
			"quality": "the buyer is unhappy with the event", "duplicate": "the buyer says they paid twice", "other": "other",
		},
	},
	"ru": {
		opened:          "⚠️ <b>Открыт спор по платежу</b>",
		inquiry:         "⚠️ <b>Банк запрашивает информацию по платежу</b>",
		due:             "⏰ <b>Спор: до срока ответа осталось дней: %d</b>",
		expired:         "❌ <b>Срок ответа на спор прошёл без ответа</b>",
		won:             "✅ <b>Спор выигран</b>",
		lost:            "❌ <b>Спор проигран</b>",
		withdrawn:       "✅ <b>Спор отозван</b>",
		order:           "Заказ %d",
		amount:          "Сумма: %s",
		reason:          "Причина: %s",
		deadline:        "Ответить до: %s",
		lowValue:        "Сумма меньше %s. Оспаривание стоит около 20 € и возвращается только при выигрыше. Мы советуем принять спор.",
		packageReady:    "Пакет доказательств готов.",
		packageBuilding: "Пакет доказательств готовится.",
		nextAPI:         "Проверьте пакет и нажмите «Отправить». Без вас ничего не уйдёт.",
		nextEmail:       "Проверьте пакет и письмо, потом нажмите «Отправить». Без вас ничего не уйдёт.",
		open:            "Открыть спор: %s",
		expiredBody:     "Провайдер решит спор без ваших доказательств.",
		closedBody:      "Провайдер закрыл спор.",
		reasons: map[string]string{
			"fraud": "покупатель говорит, что не платил", "not_received": "покупатель говорит, что не получил билеты",
			"not_recognized": "покупатель не узнал платёж", "refund": "покупатель ждал возврата",
			"quality": "покупатель недоволен мероприятием", "duplicate": "покупатель говорит, что заплатил дважды", "other": "другая",
		},
	},
	"es": {
		opened:          "⚠️ <b>Se abrió una disputa de pago</b>",
		inquiry:         "⚠️ <b>El banco pregunta por un pago</b>",
		due:             "⏰ <b>Disputa: quedan %d día(s) para responder</b>",
		expired:         "❌ <b>Pasó el plazo de la disputa sin respuesta</b>",
		won:             "✅ <b>Disputa ganada</b>",
		lost:            "❌ <b>Disputa perdida</b>",
		withdrawn:       "✅ <b>La disputa se retiró</b>",
		order:           "Pedido %d",
		amount:          "Importe: %s",
		reason:          "Motivo: %s",
		deadline:        "Responder antes de: %s",
		lowValue:        "El importe es menor que %s. Disputar cuesta unos 20 € y solo se devuelve si ganas. Te recomendamos aceptar la disputa.",
		packageReady:    "El paquete de pruebas está listo.",
		packageBuilding: "El paquete de pruebas se está preparando.",
		nextAPI:         "Revisa el paquete y pulsa «Enviar». Nada se envía sin ti.",
		nextEmail:       "Revisa el paquete y el correo, luego pulsa «Enviar». Nada se envía sin ti.",
		open:            "Abrir la disputa: %s",
		expiredBody:     "El proveedor decidirá la disputa sin tus pruebas.",
		closedBody:      "El proveedor cerró la disputa.",
		reasons: map[string]string{
			"fraud": "el comprador dice que no hizo el pago", "not_received": "el comprador dice que no recibió las entradas",
			"not_recognized": "el comprador no reconoce el pago", "refund": "el comprador esperaba una devolución",
			"quality": "el comprador no está contento con el evento", "duplicate": "el comprador dice que pagó dos veces", "other": "otro",
		},
	},
}

func noticeSet(locale string) noticeTexts {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if t, ok := noticeSets[l]; ok {
		return t
	}
	return noticeSets["en"]
}

func esc(s string) string { return opsalert.EscapeHTML(s) }

// deadlineText shows the deadline in Madrid time and, when it differs, in
// the venue's zone.
func deadlineText(t *time.Time, venueTZ string) string {
	if t == nil {
		return ""
	}
	out := t.In(loadLoc("Europe/Madrid")).Format("02.01.2006 15:04") + " (Madrid)" // allow:timeformat
	if venueTZ != "" && venueTZ != "Europe/Madrid" {
		out += ", " + t.In(loadLoc(venueTZ)).Format("02.01 15:04") + " (" + venueTZ + ")" // allow:timeformat
	}
	return out
}

func noticeText(key string, d Dispute, nf noticeFacts, base string) string {
	t := noticeSet(nf.Locale)
	var b strings.Builder
	switch {
	case key == "opened" && d.IsInquiry:
		b.WriteString(t.inquiry)
	case key == "opened":
		b.WriteString(t.opened)
	case strings.HasPrefix(key, "due_"):
		var days int
		_, _ = fmt.Sscanf(key, "due_%d", &days)
		fmt.Fprintf(&b, t.due, days)
	case key == "expired":
		b.WriteString(t.expired)
	case key == "closed":
		switch deref(d.Outcome) {
		case OutcomeWon:
			b.WriteString(t.won)
		case OutcomeWithdrawn:
			b.WriteString(t.withdrawn)
		default:
			b.WriteString(t.lost)
		}
	}
	b.WriteString("\n")
	if nf.EventName != "" {
		fmt.Fprintf(&b, "\n<b>%s</b>", esc(nf.EventName))
	}
	if nf.OrderNumber > 0 {
		b.WriteString("\n" + fmt.Sprintf(t.order, nf.OrderNumber))
	}
	b.WriteString("\n" + fmt.Sprintf(t.amount, esc(money(d.AmountMinor, d.Currency))))
	if r := t.reasons[d.ReasonGroup]; r != "" {
		b.WriteString("\n" + fmt.Sprintf(t.reason, esc(r)))
	}
	if d.Open() && d.DueBy != nil {
		b.WriteString("\n" + fmt.Sprintf(t.deadline, esc(deadlineText(d.DueBy, nf.Timezone))))
	}
	b.WriteString("\n\n")
	switch key {
	case "expired":
		b.WriteString(t.expiredBody)
	case "closed":
		b.WriteString(t.closedBody)
	default:
		if nf.HasPackage {
			b.WriteString(t.packageReady)
		} else {
			b.WriteString(t.packageBuilding)
		}
		b.WriteString(" ")
		if d.Channel == ChannelAPI {
			b.WriteString(t.nextAPI)
		} else {
			b.WriteString(t.nextEmail)
		}
		if d.LowValue && nf.Threshold > 0 {
			b.WriteString("\n\n" + fmt.Sprintf(t.lowValue, esc(money(nf.Threshold, d.Currency))))
		}
	}
	if link := cardLink(base, d); link != "" {
		b.WriteString("\n\n" + fmt.Sprintf(t.open, esc(link)))
	}
	return b.String()
}

// noticeEmail is the plain-text e-mail of a notice.
func noticeEmail(key string, d Dispute, nf noticeFacts, base string) (string, string) {
	html := noticeText(key, d, nf, base)
	text := strings.NewReplacer("<b>", "", "</b>", "", "&lt;", "<", "&gt;", ">", "&amp;", "&", "&quot;", "\"", "&#39;", "'").Replace(html)
	subject := strings.SplitN(text, "\n", 2)[0]
	subject = strings.TrimSpace(strings.TrimLeft(subject, "⚠️⏰❌✅ "))
	if nf.OrderNumber > 0 {
		subject += fmt.Sprintf(" — %d", nf.OrderNumber)
	}
	return subject, text
}

// opsText is the operator's notice: ids and amounts only.
func opsText(key string, d Dispute, nf noticeFacts) string {
	what := "Dispute opened"
	if key == "expired" {
		what = "Dispute expired without an answer"
	}
	return fmt.Sprintf("%s: %s, %s, provider %s, reason %s, order %d, dispute %s",
		what, esc(nf.OrgName), esc(money(d.AmountMinor, d.Currency)), esc(d.Provider), esc(d.ReasonGroup), nf.OrderNumber, d.ID)
}

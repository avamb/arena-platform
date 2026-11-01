package salesalert

// messages.go — the Telegram texts (HTML) posted to the organization's own
// sales chats, in the organization's language: English, Russian or Spanish,
// the languages of the event-center bot; any other falls back to English.

import (
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

type chatTexts struct {
	soldOut, soldOutManual, places, setBy, freeLeft string
	siteStale, siteStaleBody                        string
	states                                          map[string]string
}

var texts = map[string]chatTexts{
	"en": {
		soldOut:       "🔴 <b>Sold out</b>",
		soldOutManual: "🔴 <b>Sales closed: «Sold out»</b>",
		places:        "All %d places are sold.",
		setBy:         "Set by hand: %s.",
		freeLeft:      "Free places left in the system: %d. Buyers see «Sold out» everywhere.",
		siteStale:     "⚠️ <b>The site has not updated</b>",
		siteStaleBody: "The sales state changed to «%s» %d minutes ago, but the site has not taken the change yet. Check the event page on the site. If it still shows the old state, write to support.",
		states:        map[string]string{"open": "Open", "soon": "Coming soon", "sold_out": "Sold out"},
	},
	"ru": {
		soldOut:       "🔴 <b>Распродано</b>",
		soldOutManual: "🔴 <b>Продажи закрыты: «Распродано»</b>",
		places:        "Проданы все места: %d.",
		setBy:         "Поставлено вручную: %s.",
		freeLeft:      "Свободных мест в системе: %d. Покупатели везде видят «Распродано».",
		siteStale:     "⚠️ <b>Сайт не обновился</b>",
		siteStaleBody: "Статус продаж сменили на «%s» %d мин. назад, но сайт ещё не принял изменение. Проверьте страницу мероприятия на сайте. Если там старый статус, напишите в поддержку.",
		states:        map[string]string{"open": "Продажи идут", "soon": "Скоро в продаже", "sold_out": "Распродано"},
	},
	"es": {
		soldOut:       "🔴 <b>Agotado</b>",
		soldOutManual: "🔴 <b>Venta cerrada: «Agotado»</b>",
		places:        "Se vendieron todas las plazas: %d.",
		setBy:         "Puesto a mano: %s.",
		freeLeft:      "Plazas libres en el sistema: %d. Los compradores ven «Agotado» en todas partes.",
		siteStale:     "⚠️ <b>El sitio no se ha actualizado</b>",
		siteStaleBody: "El estado de la venta cambió a «%s» hace %d minutos, pero el sitio aún no ha aceptado el cambio. Revisa la página del evento en el sitio. Si sigue con el estado anterior, escribe a soporte.",
		states:        map[string]string{"open": "Abiertas", "soon": "Próximamente", "sold_out": "Agotado"},
	},
}

func textsFor(locale string) chatTexts {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if t, ok := texts[l]; ok {
		return t
	}
	return texts["en"]
}

func esc(s string) string { return opsalert.EscapeHTML(s) }

func writeSession(b *strings.Builder, in SessionInfo) {
	fmt.Fprintf(b, "\n<b>%s</b>\n📅 %s", esc(in.EventName), esc(in.When()))
	if in.Venue != "" {
		fmt.Fprintf(b, "\n📍 %s", esc(in.Venue))
	}
	b.WriteString("\n\n")
}

// SoldOutText is the chat message of a sell-out. actor is "" for a sell-out
// the last sale made.
func SoldOutText(in SessionInfo, manual bool, actor string) string {
	t := textsFor(in.OrgLocale)
	var b strings.Builder
	if !manual {
		b.WriteString(t.soldOut)
		writeSession(&b, in)
		fmt.Fprintf(&b, t.places, in.TotalPlaces)
		return b.String()
	}
	b.WriteString(t.soldOutManual)
	writeSession(&b, in)
	if actor != "" {
		fmt.Fprintf(&b, t.setBy, esc(actor))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, t.freeLeft, in.FreePlaces)
	return b.String()
}

// SiteStaleText is the chat message of a change the sites did not take.
func SiteStaleText(in SessionInfo, state string, minutes int) string {
	t := textsFor(in.OrgLocale)
	name := t.states[state]
	if name == "" {
		name = state
	}
	var b strings.Builder
	b.WriteString(t.siteStale)
	writeSession(&b, in)
	fmt.Fprintf(&b, t.siteStaleBody, esc(name), minutes)
	return b.String()
}

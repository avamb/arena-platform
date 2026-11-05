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
	// addonLeft follows a sell-out when add-on places are still free.
	addonLeft string
	// A category sold out: its line, the rest of the session, and the line
	// when nothing else is left.
	catSoldOut, catLine, catRest, catNone string
	// The sales end passed: title and the tally.
	ended, endedTally string
	// A state set or lifted by hand (or by a site): titles and who.
	manualSoon, manualOpen, manualBy string
	// via names the client a change came through.
	via map[string]string
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
		addonLeft:     "Add-on places still free: %d. They are not sold without a main ticket.",
		catSoldOut:    "🟠 <b>Category sold out</b>",
		catLine:       "«%s»: all %d places are sold.",
		catRest:       "Still on sale for this date:",
		catNone:       "Nothing else is on sale for this date.",
		ended:         "⏹ <b>Sales closed: the sales end time has passed</b>",
		endedTally:    "Sold %d of %d places.",
		manualSoon:    "⏸ <b>Sales stopped: «Coming soon»</b>",
		manualOpen:    "🟢 <b>Sales reopened</b>",
		manualBy:      "Who: %s",
		via:           map[string]string{"telegram_bot": "Telegram bot", "event_center_web": "event center", "admin_web": "admin console", "site_plugin": "the website", "api": "API"},
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
		addonLeft:     "Свободно дополнительных мест: %d. Без основного билета они не продаются.",
		catSoldOut:    "🟠 <b>Категория распродана</b>",
		catLine:       "«%s»: проданы все места (%d).",
		catRest:       "На эту дату ещё продаются:",
		catNone:       "Больше на эту дату ничего не продаётся.",
		ended:         "⏹ <b>Продажи закрыты: наступило время окончания продаж</b>",
		endedTally:    "Продано мест: %d из %d.",
		manualSoon:    "⏸ <b>Продажи остановлены: «Скоро в продаже»</b>",
		manualOpen:    "🟢 <b>Продажи снова открыты</b>",
		manualBy:      "Кто: %s",
		via:           map[string]string{"telegram_bot": "Telegram-бот", "event_center_web": "ивент-центр", "admin_web": "админка", "site_plugin": "сайт", "api": "API"},
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
		addonLeft:     "Plazas adicionales aún libres: %d. No se venden sin una entrada principal.",
		catSoldOut:    "🟠 <b>Categoría agotada</b>",
		catLine:       "«%s»: se vendieron todas las plazas (%d).",
		catRest:       "Para esta fecha aún se vende:",
		catNone:       "Para esta fecha ya no se vende nada más.",
		ended:         "⏹ <b>Venta cerrada: llegó la hora de fin de venta</b>",
		endedTally:    "Vendidas %d de %d plazas.",
		manualSoon:    "⏸ <b>Venta detenida: «Próximamente»</b>",
		manualOpen:    "🟢 <b>Venta abierta de nuevo</b>",
		manualBy:      "Quién: %s",
		via:           map[string]string{"telegram_bot": "bot de Telegram", "event_center_web": "centro de eventos", "admin_web": "consola de administración", "site_plugin": "el sitio web", "api": "API"},
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
		b.WriteString(addonNote(t, in))
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

// addonNote is the sell-out's line about add-on places still free.
func addonNote(t chatTexts, in SessionInfo) string {
	if in.AddonFree <= 0 {
		return ""
	}
	return "\n" + fmt.Sprintf(t.addonLeft, in.AddonFree)
}

// CategoryLeft is a category of the session that still has free places.
type CategoryLeft struct {
	Name string
	Free int64
}

// CategorySoldOutText is the chat message of one category whose last place
// sold; rest are the session's categories that still have free places.
func CategorySoldOutText(in SessionInfo, category string, places int64, rest []CategoryLeft) string {
	t := textsFor(in.OrgLocale)
	var b strings.Builder
	b.WriteString(t.catSoldOut)
	writeSession(&b, in)
	fmt.Fprintf(&b, t.catLine, esc(category), places)
	b.WriteString("\n\n")
	if len(rest) == 0 {
		b.WriteString(t.catNone)
		return b.String()
	}
	b.WriteString(t.catRest)
	for _, c := range rest {
		fmt.Fprintf(&b, "\n• %s — %d", esc(c.Name), c.Free)
	}
	return b.String()
}

// SalesEndedText is the chat message of a session whose sales end passed.
func SalesEndedText(in SessionInfo) string {
	t := textsFor(in.OrgLocale)
	var b strings.Builder
	b.WriteString(t.ended)
	writeSession(&b, in)
	fmt.Fprintf(&b, t.endedTally, in.AllTotal-in.AllFree, in.AllTotal)
	return b.String()
}

// ManualChangeText is the chat message of a sales state set to "coming soon"
// or lifted ("open") by a person or a site; source is the client it came
// through (session_sales_state_changes.source).
func ManualChangeText(in SessionInfo, state, actor, source string) string {
	t := textsFor(in.OrgLocale)
	var b strings.Builder
	if state == "open" {
		b.WriteString(t.manualOpen)
	} else {
		b.WriteString(t.manualSoon)
	}
	writeSession(&b, in)
	who := esc(actor)
	if via := t.via[source]; via != "" {
		if who != "" {
			who += " · "
		}
		who += esc(via)
	}
	if who != "" {
		fmt.Fprintf(&b, t.manualBy, who)
	}
	return strings.TrimRight(b.String(), "\n")
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

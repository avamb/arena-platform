package pdf

import (
	"fmt"
	"strings"
	"time"
)

// ticketLabels holds the localized field-label strings printed on the
// e-ticket PDF. Only the LABELS are localized (e.g. "Sector" vs "Sektor")
// — the printed content values (event name, venue, holder name, tier
// name, ...) always come from the Ticket struct verbatim and are never
// translated, since they are organizer-authored data, not UI chrome.
type ticketLabels struct {
	Session  string // labels the venue-local session date/time row
	Venue    string
	Tier     string
	Sector   string
	Row      string
	Seat     string
	Holder   string
	TicketID string // prefix before the raw ticket UUID line, no trailing colon
	EAN13    string // prefix before a plain-text EAN-13 caption, no trailing colon (the symbol itself now carries its own human-readable digits — kept for any caller that still wants a plain caption)
	Contact  string // prefix before the footer's contact-email line, no trailing colon
}

var labelsEN = ticketLabels{
	Session: "Session", Venue: "Venue", Tier: "Tier",
	Sector: "Sector", Row: "Row", Seat: "Seat", Holder: "Holder",
	TicketID: "Ticket ID", EAN13: "EAN-13", Contact: "Contact",
}

var labelsRU = ticketLabels{
	Session: "Сеанс", Venue: "Площадка", Tier: "Категория",
	Sector: "Сектор", Row: "Ряд", Seat: "Место", Holder: "Владелец",
	TicketID: "Номер билета", EAN13: "EAN-13", Contact: "Контакт",
}

var labelsCS = ticketLabels{
	Session: "Termín", Venue: "Místo konání", Tier: "Kategorie",
	Sector: "Sektor", Row: "Řada", Seat: "Místo", Holder: "Držitel",
	TicketID: "Číslo vstupenky", EAN13: "EAN-13", Contact: "Kontakt",
}

// normalizeLocale lowercases, trims, and strips any region/script subtag
// ("ru-RU" -> "ru", "cs_CZ" not handled — BCP-47 hyphen form only, which is
// what delivery.Payload.Locale carries).
//
// This intentionally duplicates delivery/templates' unexported normalize()
// helper rather than importing that package: templates.Renderer.ResolveLocale
// needs a template "kind" and only recognises the AllPay-market locale set
// (en/de/es/he) that this package has no reason to depend on, and the
// underlying normalize() is not exported for reuse on its own.
func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if dash := strings.IndexByte(locale, '-'); dash > 0 {
		locale = locale[:dash]
	}
	return locale
}

// labelsFor returns the label dictionary for locale. An empty or
// unsupported locale (anything other than "ru"/"cs") falls back to
// English — the renderer never refuses to print a ticket over a locale it
// doesn't have a dictionary for.
func labelsFor(locale string) ticketLabels {
	switch normalizeLocale(locale) {
	case "ru":
		return labelsRU
	case "cs":
		return labelsCS
	default:
		return labelsEN
	}
}

// ── Localized default fine print ────────────────────────────────────────
//
// Used only when Ticket.FinePrint is empty; an organizer-supplied
// FinePrint always prints verbatim, in whatever language the organizer
// wrote it, independent of Ticket.Locale. Each translation keeps to the
// same three facts as the English DefaultFinePrint (pdf.go): the QR code
// is the only proof of admission, resale outside official channels may
// invalidate the ticket, and the document is not a fiscal receipt.

// DefaultFinePrintRU is the Russian fallback disclaimer.
const DefaultFinePrintRU = "Этот электронный билет действителен только при успешном сканировании QR-кода выше на входе. " +
	"Наличие этого PDF-файла без успешного сканирования QR-кода не даёт права на вход. " +
	"Перепродажа билета вне официальных каналов организатора может привести к аннулированию билета. " +
	"Этот документ не является фискальным чеком."

// DefaultFinePrintCS is the Czech fallback disclaimer.
const DefaultFinePrintCS = "Tato e-vstupenka je platná pouze při úspěšném naskenování QR kódu výše u vstupu. " +
	"Držení tohoto PDF souboru bez úspěšného naskenování QR kódu neopravňuje ke vstupu. " +
	"Další prodej vstupenky mimo oficiální kanály pořadatele může vstupenku znehodnotit. " +
	"Tento dokument není daňovým dokladem."

// defaultFinePrintFor returns the fallback fine-print disclaimer for
// locale — used only when Ticket.FinePrint is empty. Falls back to the
// English DefaultFinePrint for an unsupported/empty locale, same as
// labelsFor.
func defaultFinePrintFor(locale string) string {
	switch normalizeLocale(locale) {
	case "ru":
		return DefaultFinePrintRU
	case "cs":
		return DefaultFinePrintCS
	default:
		return DefaultFinePrint
	}
}

// ── Localized session date/time formatting ──────────────────────────────
//
// formatSessionInVenueTZ (pdf.go) renders the venue-local date/time; the
// DATE portion's weekday/month names and ordering are locale-specific:
//
//	en: "Sat, 3 Oct 2026"     — weekday abbr, day, month abbr, year
//	ru: "сб, 3 октября 2026"  — weekday abbr, day, month in the genitive
//	                            case (required by Russian date grammar:
//	                            "3 октября" reads as "the 3rd OF
//	                            October"), year
//	cs: "so 3. 10. 2026"      — weekday abbr (no comma after it, unlike
//	                            en/ru), day, numeric month, year, each
//	                            followed by a period per Czech convention
//
// An unsupported/empty locale falls back to the English form, same as
// labelsFor. No new dependency: all name tables are plain Go data, no
// locale/i18n library.

var weekdayAbbrEN = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
var weekdayAbbrRU = [7]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
var weekdayAbbrCS = [7]string{"ne", "po", "út", "st", "čt", "pá", "so"}

var monthAbbrEN = [12]string{
	"Jan", "Feb", "Mar", "Apr", "May", "Jun",
	"Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
}

// monthGenitiveRU holds the Russian month names in the genitive case, the
// form Russian date grammar requires after a day number ("3 октября", not
// the nominative "октябрь").
var monthGenitiveRU = [12]string{
	"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря",
}

// formatDatePart renders just the date portion (no time, no zone) of t in
// locale's conventional form. t must already be in the venue-local zone
// (formatSessionInVenueTZ does that conversion before calling this).
func formatDatePart(t time.Time, locale string) string {
	wd := int(t.Weekday()) // time.Sunday == 0, matching the *AbbrXX table order
	switch normalizeLocale(locale) {
	case "ru":
		return fmt.Sprintf("%s, %d %s %d", weekdayAbbrRU[wd], t.Day(), monthGenitiveRU[t.Month()-1], t.Year())
	case "cs":
		return fmt.Sprintf("%s %d. %d. %d", weekdayAbbrCS[wd], t.Day(), int(t.Month()), t.Year())
	default:
		return fmt.Sprintf("%s, %d %s %d", weekdayAbbrEN[wd], t.Day(), monthAbbrEN[t.Month()-1], t.Year())
	}
}

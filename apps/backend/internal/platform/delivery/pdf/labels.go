package pdf

import "strings"

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
	EAN13    string // prefix before the raw EAN-13 code line, no trailing colon
}

var labelsEN = ticketLabels{
	Session: "Session", Venue: "Venue", Tier: "Tier",
	Sector: "Sector", Row: "Row", Seat: "Seat", Holder: "Holder",
	TicketID: "Ticket ID", EAN13: "EAN-13",
}

var labelsRU = ticketLabels{
	Session: "Сеанс", Venue: "Площадка", Tier: "Категория",
	Sector: "Сектор", Row: "Ряд", Seat: "Место", Holder: "Владелец",
	TicketID: "Номер билета", EAN13: "EAN-13",
}

var labelsCS = ticketLabels{
	Session: "Termín", Venue: "Místo konání", Tier: "Kategorie",
	Sector: "Sektor", Row: "Řada", Seat: "Místo", Holder: "Držitel",
	TicketID: "Číslo vstupenky", EAN13: "EAN-13",
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

// labels.go — the localized CHROME of the e-ticket: the field labels, the
// footer wording, and the month/weekday name tables the printed date is
// built from.
//
// Every table below is a port of the production PHP renderer the owner has
// been shipping from the Lampyris / Vino&Co WordPress sites
// (bil24-ticket-mailer: includes/class-btm-renderer.php, Btm_Renderer::strings()
// and ::format_show_time()). It is deliberately a VERBATIM port — those
// strings have been read by real buyers for months, so this package does not
// get to "improve" them.
//
// Only the CHROME is localized. Content values (event name, venue, holder
// name, category name, address) are organizer/buyer data and are always
// printed exactly as supplied, in whatever script they arrive in — which is
// why the embedded font (fonts.go) needs broad Unicode coverage independently
// of which locale is selected here.
//
// Arena additions to the ported table: Holder (the ticket-holder row, which
// the WordPress design has no equivalent of), Contact (the footer's contact
// e-mail prefix, part of the EU identification block) and NotFiscal (the
// "this is not a fiscal receipt" disclosure that the pre-port arena layout
// carried and that there is no reason to drop).
package pdf

import (
	"fmt"
	"strings"
	"time"
)

// ticketStrings is the localized string table for one locale.
type ticketStrings struct {
	// ── ported verbatim from Btm_Renderer::strings() ──────────────────
	Category  string // info-cell label above the category/tier name
	Seat      string // info-cell label above the seat coordinates
	Price     string // info-cell label above the price
	RowWord   string // lowercase word used INSIDE a seat value ("row 3")
	SeatWord  string // lowercase word used INSIDE a seat value ("seat 12")
	TicketNo  string // "Ticket #" — prefixed to the number under the codes
	Order     string // "Order" — prefixed to the order number in the footer
	Organizer string // "Organizer" — prefixed to the organizer name
	KeepNote  string // the closing footer note

	// ── arena additions ───────────────────────────────────────────────
	Holder    string // info-cell label above the ticket holder's name
	Contact   string // footer prefix before the contact e-mail
	NotFiscal string // the "not a fiscal receipt" disclosure sentence
}

// ticketStringsByLocale holds one entry per locale the ported table covers.
// The ported nine are the languages the WordPress plugin shipped; arena
// serves en/ru/cs/es today (two Spanish clients, one Czech, Russian-language
// content), and the remaining five come along for free because they are pure
// data.
var ticketStringsByLocale = map[string]ticketStrings{
	"en": {
		Category: "Category", Seat: "Seat", Price: "Price",
		RowWord: "row", SeatWord: "seat",
		TicketNo: "Ticket #", Order: "Order", Organizer: "Organizer",
		KeepNote:  "Show the QR code at the entrance. Do not share your ticket publicly.",
		Holder:    "Holder",
		Contact:   "Contact",
		NotFiscal: "This document is not a fiscal receipt.",
	},
	"ru": {
		Category: "Категория", Seat: "Место", Price: "Цена",
		RowWord: "ряд", SeatWord: "место",
		TicketNo: "Билет №", Order: "Заказ", Organizer: "Организатор",
		KeepNote:  "Предъявите QR-код на входе. Не публикуйте билет в открытом доступе.",
		Holder:    "Владелец",
		Contact:   "Контакт",
		NotFiscal: "Этот документ не является фискальным чеком.",
	},
	"cs": {
		Category: "Kategorie", Seat: "Místo", Price: "Cena",
		RowWord: "řada", SeatWord: "místo",
		TicketNo: "Vstupenka č.", Order: "Objednávka", Organizer: "Pořadatel",
		KeepNote:  "U vstupu předložte QR kód. Vstupenku nikde nezveřejňujte.",
		Holder:    "Držitel",
		Contact:   "Kontakt",
		NotFiscal: "Tento dokument není daňovým dokladem.",
	},
	"es": {
		Category: "Categoría", Seat: "Asiento", Price: "Precio",
		RowWord: "fila", SeatWord: "asiento",
		TicketNo: "Entrada n.º", Order: "Pedido", Organizer: "Organizador",
		KeepNote:  "Muestra el código QR en la entrada. No compartas tu entrada públicamente.",
		Holder:    "Titular",
		Contact:   "Contacto",
		NotFiscal: "Este documento no es un recibo fiscal.",
	},
	"de": {
		Category: "Kategorie", Seat: "Platz", Price: "Preis",
		RowWord: "Reihe", SeatWord: "Platz",
		TicketNo: "Ticket Nr.", Order: "Bestellung", Organizer: "Veranstalter",
		KeepNote:  "Zeigen Sie den QR-Code am Eingang. Teilen Sie Ihr Ticket nicht öffentlich.",
		Holder:    "Inhaber",
		Contact:   "Kontakt",
		NotFiscal: "Dieses Dokument ist kein Steuerbeleg.",
	},
	"fr": {
		Category: "Catégorie", Seat: "Place", Price: "Prix",
		RowWord: "rang", SeatWord: "place",
		TicketNo: "Billet n°", Order: "Commande", Organizer: "Organisateur",
		KeepNote:  "Présentez le code QR à l’entrée. Ne partagez pas votre billet publiquement.",
		Holder:    "Titulaire",
		Contact:   "Contact",
		NotFiscal: "Ce document n’est pas un reçu fiscal.",
	},
	"it": {
		Category: "Categoria", Seat: "Posto", Price: "Prezzo",
		RowWord: "fila", SeatWord: "posto",
		TicketNo: "Biglietto n.", Order: "Ordine", Organizer: "Organizzatore",
		KeepNote:  "Mostra il codice QR all’ingresso. Non condividere il biglietto pubblicamente.",
		Holder:    "Intestatario",
		Contact:   "Contatto",
		NotFiscal: "Questo documento non è una ricevuta fiscale.",
	},
	"pl": {
		Category: "Kategoria", Seat: "Miejsce", Price: "Cena",
		RowWord: "rząd", SeatWord: "miejsce",
		TicketNo: "Bilet nr", Order: "Zamówienie", Organizer: "Organizator",
		KeepNote:  "Przy wejściu okaż kod QR. Nie udostępniaj biletu publicznie.",
		Holder:    "Posiadacz",
		Contact:   "Kontakt",
		NotFiscal: "Ten dokument nie jest paragonem fiskalnym.",
	},
	"uk": {
		Category: "Категорія", Seat: "Місце", Price: "Ціна",
		RowWord: "ряд", SeatWord: "місце",
		TicketNo: "Квиток №", Order: "Замовлення", Organizer: "Організатор",
		KeepNote:  "Пред’явіть QR-код на вході. Не публікуйте квиток у відкритому доступі.",
		Holder:    "Власник",
		Contact:   "Контакт",
		NotFiscal: "Цей документ не є фіскальним чеком.",
	},
}

// DefaultLocale is the label language used when Ticket.Locale is empty or
// names a locale this package has no table for. The renderer NEVER refuses
// to print a ticket over an unknown locale — a cosmetic label mismatch must
// not cost a sale.
const DefaultLocale = "en"

// SupportedLocales lists the locales with their own chrome/date tables, in a
// fixed order so callers (and tests) can iterate deterministically.
//
// Deliberately NOT unified with delivery/templates' own locale set: that
// package localizes the e-mail BODY and supports a different list with
// different fallback rules. A locale valid for one is not automatically
// valid for the other, and pretending otherwise has bitten this code before.
var SupportedLocales = []string{"en", "ru", "cs", "es", "de", "fr", "it", "pl", "uk"}

// normalizeLocale lowercases, trims, and strips any region/script subtag
// ("ru-RU" -> "ru"). BCP-47 hyphen form only, which is what
// delivery.Payload.Locale and checkout_sessions.buyer_locale carry.
func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if dash := strings.IndexByte(locale, '-'); dash > 0 {
		locale = locale[:dash]
	}
	return locale
}

// stringsFor returns the chrome table for locale, falling back to
// DefaultLocale for anything unknown or empty.
func stringsFor(locale string) ticketStrings {
	if s, ok := ticketStringsByLocale[normalizeLocale(locale)]; ok {
		return s
	}
	return ticketStringsByLocale[DefaultLocale]
}

// defaultNoteFor is the footer note printed when Ticket.FinePrint is empty:
// the ported KeepNote plus the arena "not a fiscal receipt" disclosure. An
// organizer-supplied FinePrint replaces it wholesale and prints verbatim, in
// whatever language the organizer wrote it, independent of Ticket.Locale.
func defaultNoteFor(locale string) string {
	s := stringsFor(locale)
	return s.KeepNote + " " + s.NotFiscal
}

// ── Localized date formatting ───────────────────────────────────────────
//
// Ported from Btm_Renderer::format_show_time(). Three separate strings come
// out — the date, the 24-hour clock time, and the weekday — because the
// layout prints them as three differently-styled lines, not one run.

// monthNames holds the month names in the form a DATE uses, which is the
// genitive in the Slavic languages ("8 września", not "wrzesień") — index 0
// is January. Ported verbatim; do not "correct" a Slavic entry to the
// nominative.
var monthNames = map[string][12]string{
	"en": {"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
	"ru": {"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"},
	"cs": {"ledna", "února", "března", "dubna", "května", "června", "července", "srpna", "září", "října", "listopadu", "prosince"},
	"es": {"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
	"de": {"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
	"fr": {"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
	"it": {"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
	"pl": {"stycznia", "lutego", "marca", "kwietnia", "maja", "czerwca", "lipca", "sierpnia", "września", "października", "listopada", "grudnia"},
	"uk": {"січня", "лютого", "березня", "квітня", "травня", "червня", "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"},
}

// weekdayNames is indexed by time.Weekday (Sunday == 0), matching both the
// PHP source's date('w') indexing and Go's own time.Weekday constants.
var weekdayNames = map[string][7]string{
	"en": {"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
	"ru": {"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"},
	"cs": {"neděle", "pondělí", "úterý", "středa", "čtvrtek", "pátek", "sobota"},
	"es": {"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
	"de": {"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
	"fr": {"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
	"it": {"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"},
	"pl": {"niedziela", "poniedziałek", "wtorek", "środa", "czwartek", "piątek", "sobota"},
	"uk": {"неділя", "понеділок", "вівторок", "середа", "четвер", "п’ятниця", "субота"},
}

// dateJoinPatterns overrides how day / month / year join for the locales
// that do not use the bare "<day> <month> <year>" form: Czech and German put
// a period after the day number, Spanish links all three with "de". The
// arguments are always (day int, month string, year int) in that order, so a
// plain Go format string is enough — the PHP original needed positional
// specifiers only because it reused one sprintf call site.
var dateJoinPatterns = map[string]string{
	"cs": "%d. %s %d",
	"de": "%d. %s %d",
	"es": "%d de %s de %d",
}

const defaultDateJoinPattern = "%d %s %d"

// formatShowTime converts the UTC session start into the venue's local clock
// time and returns the three localized pieces the layout prints:
//
//	date    "5 December 2026" / "5. prosince 2026" / "5 de diciembre de 2026"
//	clock   "19:30" — 24-hour in every locale, as in the ported original
//	weekday "Saturday" / "sobota" / "sábado"
//
// If tz is empty or time.LoadLocation fails, the time is rendered in UTC —
// the renderer never panics or refuses over a bad zone name.
//
// NOTE: unlike the pre-port arena layout, NO timezone label is printed. The
// ticket is consumed at the venue, where the venue's local wall clock is the
// only time that means anything, and the production design the owner has
// been shipping prints none.
func formatShowTime(t time.Time, tz, locale string) (date, clock, weekday string) {
	loc := time.UTC
	if strings.TrimSpace(tz) != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	local := t.In(loc)

	key := normalizeLocale(locale)
	months, ok := monthNames[key]
	if !ok {
		key = DefaultLocale
		months = monthNames[DefaultLocale]
	}
	days := weekdayNames[key]

	pattern, ok := dateJoinPatterns[key]
	if !ok {
		pattern = defaultDateJoinPattern
	}
	date = fmt.Sprintf(pattern, local.Day(), months[int(local.Month())-1], local.Year())
	// Human-facing wall-clock time on a printed ticket;
	// allow:timeformat: deliberately not an RFC3339 API timestamp.
	clock = fmt.Sprintf("%02d:%02d", local.Hour(), local.Minute())
	weekday = days[int(local.Weekday())]
	return date, clock, weekday
}

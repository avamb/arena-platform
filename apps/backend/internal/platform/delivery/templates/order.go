package templates

import "strings"

// seatWords are the words of a seat position in each shipped locale: sector,
// row, seat. They are the same words the single-ticket templates have always
// used for their "Sector / Row / Seat" rows.
var seatWords = map[string][3]string{
	"cs": {"Sektor", "Řada", "Místo"},
	"de": {"Sektor", "Reihe", "Platz"},
	"en": {"Sector", "Row", "Seat"},
	"es": {"Sector", "Fila", "Asiento"},
	"fr": {"Secteur", "Rang", "Place"},
	"he": {"אזור", "שורה", "מקום"},
	"ru": {"Сектор", "Ряд", "Место"},
}

// FormatSeat puts a ticket's seat coordinates into one line in the buyer's
// language ("Sector A · Row 3 · Seat 5"), for the ticket list of the e-mail
// that carries several tickets. Empty parts are left out; a general-admission
// ticket (all three empty) gives "". An unknown locale falls back to English.
func FormatSeat(locale, sector, row, number string) string {
	words, ok := seatWords[normalize(locale)]
	if !ok {
		words = seatWords[DefaultLocale]
	}
	var parts []string
	if s := strings.TrimSpace(sector); s != "" {
		parts = append(parts, words[0]+" "+s)
	}
	if r := strings.TrimSpace(row); r != "" {
		parts = append(parts, words[1]+" "+r)
	}
	if n := strings.TrimSpace(number); n != "" {
		parts = append(parts, words[2]+" "+n)
	}
	return strings.Join(parts, " · ")
}

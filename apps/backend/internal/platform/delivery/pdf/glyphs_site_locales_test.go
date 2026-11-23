package pdf

import (
	"testing"

	"golang.org/x/image/font/sfnt"
)

// The e-ticket prints its own chrome and the buyer's data in DejaVu. Every
// letter the labels, month names and weekday names of EVERY supported language
// need must have a glyph in the embedded font, or the buyer gets an empty box
// instead of a letter (spec 63, L-4: ů ő ą ė š õ є ї and the rest).
func TestEveryChromeRuneHasADejaVuGlyph(t *testing.T) {
	f, err := sfnt.Parse(dejaVuRegularTTF)
	if err != nil {
		t.Fatal(err)
	}
	var buf sfnt.Buffer
	check := func(where, s string) {
		for _, r := range s {
			if r < 0x20 {
				continue
			}
			idx, err := f.GlyphIndex(&buf, r)
			if err != nil || idx == 0 {
				t.Errorf("%s: %q (U+%04X) has no glyph in DejaVu Sans Condensed", where, r, r)
			}
		}
	}
	for _, loc := range SupportedLocales {
		s, ok := ticketStringsByLocale[loc]
		if !ok {
			t.Errorf("%s: no ticket strings", loc)
			continue
		}
		for name, v := range map[string]string{
			"Category": s.Category, "Seat": s.Seat, "Price": s.Price, "RowWord": s.RowWord,
			"SeatWord": s.SeatWord, "TicketNo": s.TicketNo, "Order": s.Order,
			"Organizer": s.Organizer, "KeepNote": s.KeepNote, "Holder": s.Holder,
			"Contact": s.Contact, "NotFiscal": s.NotFiscal, "Doors": s.Doors,
			"Directions": s.Directions, "Invitation": s.Invitation,
		} {
			if v == "" {
				t.Errorf("%s: empty %s", loc, name)
			}
			check(loc+" "+name, v)
		}
		for _, m := range monthNames[loc] {
			if m == "" {
				t.Errorf("%s: empty month name", loc)
			}
			check(loc+" month", m)
		}
		for _, d := range weekdayNames[loc] {
			if d == "" {
				t.Errorf("%s: empty weekday name", loc)
			}
			check(loc+" weekday", d)
		}
	}
	// Letters named in the task, whatever the tables say today.
	check("named letters", "ůŐőąĄėĖšŠõÕєЄїЇžŽčČęĘųŪūĪīĒēĶķĻļŅņŖŗĢģäöüÄÖÜ")
}

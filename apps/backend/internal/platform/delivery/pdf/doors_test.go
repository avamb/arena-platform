package pdf

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// The doors-open time (migration 0128) prints under the weekday on the
// venue's wall clock, in the ticket's language; a ticket without one prints
// no such line.
func TestRender_DoorsOpenLine(t *testing.T) {
	tk := validTicket(t)
	tk.Locale = "ru"
	doors := tk.SessionStart.Add(-30 * time.Minute) // 18:00 UTC = 21:00 Moscow
	tk.DoorsOpenAt = &doors
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, pdfText("Вход с 21:00")) {
		t.Error("the PDF lacks the doors line \"Вход с 21:00\"")
	}

	tk.DoorsOpenAt = nil
	out, err = Render(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, pdfTextPrefix("Вход с")) {
		t.Error("a ticket without a doors time must not print the doors line")
	}
}

// Every locale of the string table names the doors line.
func TestStrings_EveryLocaleHasDoors(t *testing.T) {
	for loc, s := range ticketStringsByLocale {
		if s.Doors == "" {
			t.Errorf("%s: empty Doors label", loc)
		}
	}
}

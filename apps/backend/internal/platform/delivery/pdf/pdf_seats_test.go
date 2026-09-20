// pdf_seats_test.go — the seat coordinates on the page.
//
// The ported design prints the seat as ONE info cell ("A, row 3, seat 12")
// rather than the three separate labelled rows the pre-port layout used, and
// composes it from whichever of sector/row/number are actually present — the
// old all-or-nothing rule silently dropped a seat that knew its row but not
// its sector.
//
// The renderer's SetCompression(false) means these assertions can search the
// raw content stream directly; pdfText reproduces gofpdf's UTF-16BE encoding
// so a specific value token can still be found.
package pdf

import (
	"bytes"
	"testing"
)

func seatedTicket(t *testing.T) Ticket {
	t.Helper()
	tk := validTicket(t)
	tk.SeatSector = "A"
	tk.SeatRow = "3"
	tk.SeatNumber = "12"
	return tk
}

func gaTicket(t *testing.T) Ticket {
	t.Helper()
	tk := validTicket(t)
	tk.SeatSector, tk.SeatRow, tk.SeatNumber = "", "", ""
	return tk
}

// TestSeat_RendersOneComposedCell pins the seated contract: the composed
// value prints, under an uppercase SEAT label.
func TestSeat_RendersOneComposedCell(t *testing.T) {
	tk := seatedTicket(t)
	if got := seatValue(tk, stringsFor("en")); got != "A, row 3, seat 12" {
		t.Fatalf("seatValue = %q", got)
	}
	out := render(t, tk)
	// Three cells share the content width, so the composed value wraps per
	// word — match the start of the first wrapped line.
	if !bytes.Contains(out, pdfTextPrefix("A, row 3,")) {
		t.Error("seated PDF missing the composed seat value")
	}
	if !pdfHasTracked(out, "SEAT") {
		t.Error("seated PDF missing the SEAT label")
	}
}

// TestSeat_GATicketOmitsTheCell pins the negative case: a general-admission
// ticket has no seat cell at all.
func TestSeat_GATicketOmitsTheCell(t *testing.T) {
	out := render(t, gaTicket(t))
	if bytes.Contains(out, pdfTextPrefix("A, row 3,")) {
		t.Error("GA PDF should not contain a seat value")
	}
	cells := infoCells(gaTicket(t), stringsFor("en"))
	for _, c := range cells {
		if c.label == stringsFor("en").Seat {
			t.Error("GA ticket should have no Seat info cell")
		}
	}
}

// TestSeat_PartialCoordinatesStillPrint is the behaviour change from the
// pre-port layout, which required all three fields before drawing any of
// them and therefore printed nothing for a ticket that knew only its row.
func TestSeat_PartialCoordinatesStillPrint(t *testing.T) {
	tk := gaTicket(t)
	tk.SeatRow = "3"
	out := render(t, tk)
	if !bytes.Contains(out, pdfText("row 3")) {
		t.Error("a ticket that knows only its row should still print it")
	}
}

// TestSeat_SeatedVsGA_DifferByteOutput guards against a regression where the
// seat cell stops being conditional and both variants collapse to the same
// page.
func TestSeat_SeatedVsGA_DifferByteOutput(t *testing.T) {
	if bytes.Equal(render(t, seatedTicket(t)), render(t, gaTicket(t))) {
		t.Fatal("seated and GA PDFs are byte-identical (expected difference)")
	}
}

// TestSeat_SeatedRenderIsDeterministic — determinism is a hard contract of
// Render (tests and audit hashing rely on it).
func TestSeat_SeatedRenderIsDeterministic(t *testing.T) {
	tk := seatedTicket(t)
	if !bytes.Equal(render(t, tk), render(t, tk)) {
		t.Fatal("seated Render is not deterministic")
	}
}

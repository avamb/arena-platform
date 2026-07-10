// pdf_seats_test.go — SEAT-C3 (feature #311) PDF renderer contract tests.
//
// The renderer draws PDF operators as text streams; the resulting bytes
// contain the labelled detail-block strings verbatim inside the
// content-stream operators (BT...ET blocks). This lets us assert that:
//
//  1. A seated ticket (SeatSector/SeatRow/SeatNumber all populated)
//     causes the renderer to emit the "Sector:", "Row:", and "Seat:"
//     label rows alongside the seat values.
//  2. A GA ticket (all three empty) omits the seat block entirely: no
//     "Sector:" / "Row:" / "Seat:" labels appear anywhere in the PDF.
//  3. The seated and GA outputs are byte-different (regression guard —
//     if the drawDetails switch is ever removed by accident, both
//     variants would produce identical PDFs).
//
// The renderer's SetCompression(false) + SetCatalogSort(true) knobs mean
// this test does not need to decompress the content stream; the raw
// bytes carry the parenthesised operator arguments plainly.
package pdf

import (
	"bytes"
	"context"
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

// TestSeatC3_RendersSeatBlock pins the seated-ticket contract: the
// rendered PDF bytes contain literal Sector / Row / Seat labels (with
// the trailing colon drawn by drawDetails) plus the actual seat values.
func TestSeatC3_RendersSeatBlock(t *testing.T) {
	out, err := Render(context.Background(), seatedTicket(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"(Sector:)", "(Row:)", "(Seat:)",
		"(A)", "(3)", "(12)",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("seated PDF missing token %q", want)
		}
	}
}

// TestSeatC3_GATicketOmitsSeatBlock pins the negative case: a
// general-admission ticket (all three seat fields empty) must not draw
// the seat block. Since "Row" / "Seat" could plausibly appear inside
// the default fine-print disclaimer as prose, we probe the exact
// operator token drawDetails emits — "(Sector:)" etc. — which only
// appears when the label cell is drawn.
func TestSeatC3_GATicketOmitsSeatBlock(t *testing.T) {
	out, err := Render(context.Background(), validTicket(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, banned := range []string{"(Sector:)", "(Row:)", "(Seat:)"} {
		if bytes.Contains(out, []byte(banned)) {
			t.Errorf("GA PDF should not contain %q", banned)
		}
	}
}

// TestSeatC3_SeatedVsGA_DifferByteOutput guards against a regression
// where the seat-block switch is removed and both variants collapse to
// the same PDF. Two Renders of the same seed with different SeatSector
// values MUST produce different bytes.
func TestSeatC3_SeatedVsGA_DifferByteOutput(t *testing.T) {
	seated, err := Render(context.Background(), seatedTicket(t))
	if err != nil {
		t.Fatalf("Render seated: %v", err)
	}
	ga, err := Render(context.Background(), validTicket(t))
	if err != nil {
		t.Fatalf("Render GA: %v", err)
	}
	if bytes.Equal(seated, ga) {
		t.Fatalf("seated and GA PDFs are byte-identical (expected difference)")
	}
}

// TestSeatC3_SeatedRenderIsDeterministic re-runs the seated render
// twice against the same input and asserts byte-identical output.
// Determinism is a hard contract of pdf.Render (used by tests + audit
// hashing) — new seat block must not accidentally introduce non-determinism.
func TestSeatC3_SeatedRenderIsDeterministic(t *testing.T) {
	tk := seatedTicket(t)
	a, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render a: %v", err)
	}
	b, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("seated Render is not deterministic (%d vs %d bytes differ)",
			len(a), len(b))
	}
}

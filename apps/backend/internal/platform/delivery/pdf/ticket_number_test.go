package pdf

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// ticket_number_test.go — the buyer-facing ticket number replaced the raw
// UUID on the printed page (production defect found 2026-09-20 on the
// first live client: the ticket read
// "Номер билета: 01a0bbfd-5e3e-792c-bf64-8aab0a821380").
//
// These tests assert through pdfText: once a layout calls
// pdf.SetFont(fontFamily, ...) gofpdf writes that text as UTF-16BE in the
// content stream, so a raw []byte("literal") search silently matches
// nothing. The one deliberate exception is the "UUID must be absent"
// check, which looks for BOTH encodings.

// TestRender_PrintsSystemTicketNumberNotUUID proves the resolved
// TicketNumber reaches the page and the ticket UUID does not.
func TestRender_PrintsSystemTicketNumberNotUUID(t *testing.T) {
	tk := validTicket(t)
	tk.TicketNumber = "1000000517"

	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !bytes.Contains(out, pdfText(stringsFor("en").TicketNo+" "+tk.TicketNumber)) {
		t.Errorf("PDF does not print the ticket number line %q", stringsFor("en").TicketNo+" "+tk.TicketNumber)
	}
	assertNoUUID(t, out, tk.TicketID)
}

// TestRender_NoSystemID_FallsBackToShortRefNotUUID covers a legacy ticket
// with no system_ticket_id: the line must still carry something short and
// quotable, and still not the UUID.
func TestRender_NoSystemID_FallsBackToShortRefNotUUID(t *testing.T) {
	tk := validTicket(t)
	tk.TicketNumber = "" // pre-migration-0088 ticket

	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := shortTicketRef(tk.TicketID)
	if len(want) != shortTicketRefLen {
		t.Fatalf("shortTicketRef(%q) = %q; want %d characters", tk.TicketID, want, shortTicketRefLen)
	}
	if !bytes.Contains(out, pdfText(stringsFor("en").TicketNo+" "+want)) {
		t.Errorf("PDF does not print the short fallback reference %q", want)
	}
	assertNoUUID(t, out, tk.TicketID)
}

// TestRender_DocumentTitleCarriesNumberNotUUID guards the PDF Info
// dictionary too — a title is visible in the reader's window chrome and
// travels with anything the buyer forwards.
func TestRender_DocumentTitleCarriesNumberNotUUID(t *testing.T) {
	tk := validTicket(t)
	tk.TicketNumber = "1000000518"

	for _, format := range []Format{FormatMobile, FormatA4Print} {
		out, err := RenderFormat(context.Background(), tk, format)
		if err != nil {
			t.Fatalf("RenderFormat(%s): %v", format, err)
		}
		// gofpdf writes /Title through utf8toutf16 with its default
		// withBOM=true, so the metadata string is U+FEFF followed by the
		// same UTF-16BE code units page text uses (SetFont text calls the
		// no-BOM variant — hence pdfText alone does not match here).
		wantTitle := append([]byte{0xFE, 0xFF}, utf16beEscaped("Ticket "+tk.TicketNumber)...)
		if !bytes.Contains(out, wantTitle) {
			t.Errorf("%s: PDF title does not carry the ticket number", format)
		}
		assertNoUUID(t, out, tk.TicketID)
	}
}

// TestDisplayNumber_PrefersResolvedNumber pins the shared rule the e-mail
// body and the PDF both use, so the two can never disagree about what the
// buyer's ticket number is.
func TestDisplayNumber_PrefersResolvedNumber(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	cases := []struct {
		name   string
		number string
		want   string
	}{
		{"resolved system id wins", "1000000519", "1000000519"},
		{"blank number falls back", "", "11111111"},
		{"whitespace-only number falls back", "   ", "11111111"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayNumber(c.number, id); got != c.want {
				t.Errorf("DisplayNumber(%q, id) = %q; want %q", c.number, got, c.want)
			}
		})
	}
}

// assertNoUUID fails when the ticket UUID appears anywhere in the rendered
// bytes, in either the plain ASCII form (metadata / raw operators) or the
// UTF-16BE form the UTF-8 font writes page text in.
func assertNoUUID(t *testing.T, out []byte, ticketID string) {
	t.Helper()
	if strings.TrimSpace(ticketID) == "" {
		t.Fatal("assertNoUUID called with an empty ticket id")
	}
	if bytes.Contains(out, []byte(ticketID)) {
		t.Errorf("rendered PDF contains the raw ticket UUID %q", ticketID)
	}
	if bytes.Contains(out, utf16beEscaped(ticketID)) {
		t.Errorf("rendered PDF contains the UTF-16BE-encoded ticket UUID %q", ticketID)
	}
}

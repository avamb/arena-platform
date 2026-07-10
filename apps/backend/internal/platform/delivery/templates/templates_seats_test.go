// templates_seats_test.go — SEAT-C3 (feature #311) contract tests.
//
// Verifies that when a ticket carries denormalized seat coordinates
// (SeatSector / SeatRow / SeatNumber all populated), each locale's
// ticket and invitation templates render Sector / Row / Seat lines in
// both the HTML and text bodies; and that GA tickets (all three empty)
// omit the seat block entirely.
package templates

import (
	"strings"
	"testing"
)

// seatLabels maps a template file's locale to the (sector, row, seat)
// labels expected in the rendered output. Both HTML and plain-text
// blocks share the same labels — the templates render the same strings
// with different surrounding markup.
var seatLabels = map[string][3]string{
	"en": {"Sector", "Row", "Seat"},
	"de": {"Sektor", "Reihe", "Platz"},
	"es": {"Sector", "Fila", "Asiento"},
	"he": {"אזור", "שורה", "מקום"},
}

func seatedData() Data {
	return Data{
		TicketID:       "11111111-2222-3333-4444-555555555555",
		RecipientEmail: "buyer@example.test",
		HolderName:     "Test Holder",
		EventName:      "SEAT-C3 Contract Event",
		SessionStart:   "2026-06-01 20:00 (Europe/Moscow)",
		VenueName:      "Contract Hall",
		TierName:       "VIP",
		SeatSector:     "A",
		SeatRow:        "3",
		SeatNumber:     "12",
		Branding: Branding{
			OrgName:      PlatformOrgName,
			LogoURL:      PlatformLogoURL,
			LegalName:    PlatformLegalName,
			ContactEmail: PlatformContactEmail,
		},
	}
}

func gaData() Data {
	d := seatedData()
	d.SeatSector = ""
	d.SeatRow = ""
	d.SeatNumber = ""
	return d
}

// TestSeatC3_TicketTemplates_RenderSeatLines pins that every locale's
// ticket template renders the Sector / Row / Seat labels plus the
// resolved seat values into both HTML and plain-text bodies when the
// data carries seat coordinates.
func TestSeatC3_TicketTemplates_RenderSeatLines(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := seatedData()
	for loc, labels := range seatLabels {
		loc, labels := loc, labels
		t.Run("ticket."+loc, func(t *testing.T) {
			out, err := r.Render(TemplateKindTicket, loc, data)
			if err != nil {
				t.Fatalf("Render(ticket,%s): %v", loc, err)
			}
			for _, body := range []struct{ name, body string }{
				{"html", out.HTMLBody},
				{"text", out.TextBody},
			} {
				for _, want := range []string{
					labels[0], labels[1], labels[2],
					data.SeatSector, data.SeatRow, data.SeatNumber,
				} {
					if !strings.Contains(body.body, want) {
						t.Errorf("ticket.%s %s body missing %q\n---\n%s",
							loc, body.name, want, body.body)
					}
				}
			}
		})
	}
}

// TestSeatC3_InvitationTemplates_RenderSeatLines mirrors the ticket
// contract for complimentary invitations — they must render the same
// Sector / Row / Seat labels + values when the data carries seat
// coordinates, so seated complimentary issuances (future SEAT-D) are
// wire-compatible.
func TestSeatC3_InvitationTemplates_RenderSeatLines(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := seatedData()
	for loc, labels := range seatLabels {
		loc, labels := loc, labels
		t.Run("invitation."+loc, func(t *testing.T) {
			out, err := r.Render(TemplateKindInvitation, loc, data)
			if err != nil {
				t.Fatalf("Render(invitation,%s): %v", loc, err)
			}
			for _, body := range []struct{ name, body string }{
				{"html", out.HTMLBody},
				{"text", out.TextBody},
			} {
				for _, want := range []string{
					labels[0], labels[1], labels[2],
					data.SeatSector, data.SeatRow, data.SeatNumber,
				} {
					if !strings.Contains(body.body, want) {
						t.Errorf("invitation.%s %s body missing %q\n---\n%s",
							loc, body.name, want, body.body)
					}
				}
			}
		})
	}
}

// TestSeatC3_GATickets_OmitSeatLines verifies the negative case:
// general-admission tickets (all three seat fields empty) must not emit
// the Sector / Row / Seat labels in any locale. This guards against a
// template regression that would print empty "Sector: " rows for GA.
func TestSeatC3_GATickets_OmitSeatLines(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := gaData()
	for loc, labels := range seatLabels {
		loc, labels := loc, labels
		t.Run("ticket."+loc, func(t *testing.T) {
			out, err := r.Render(TemplateKindTicket, loc, data)
			if err != nil {
				t.Fatalf("Render(ticket,%s): %v", loc, err)
			}
			// The labels themselves are the only reliable marker: the
			// row markup ("Sector:", "Sektor:") never appears when the
			// {{with}} guard suppresses the row. The English "Row"
			// label is a common English noun that could plausibly
			// appear elsewhere, so probe the exact label + separator
			// used by the ticket template.
			for _, lbl := range []string{
				labels[0] + ":",                      // text body form: "Sector: 12"
				"<strong>" + labels[0] + "</strong>", // html body row header
			} {
				if strings.Contains(out.HTMLBody, lbl) {
					t.Errorf("GA ticket.%s html body must not contain %q", loc, lbl)
				}
				if strings.Contains(out.TextBody, lbl) {
					t.Errorf("GA ticket.%s text body must not contain %q", loc, lbl)
				}
			}
		})
	}
}

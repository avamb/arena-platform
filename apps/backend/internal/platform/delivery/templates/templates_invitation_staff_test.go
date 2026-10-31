package templates

import (
	"strings"
	"testing"
)

// Staff invitations (spec 46, section 3.4): one invitation letter may carry
// several tickets, and a withdrawn invitation gets its own wording of the
// cancel letter.

func TestInvitation_ListsEveryTicketOfAnOrderLetterInEveryLocale(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, loc := range SupportedLocales {
		t.Run(loc, func(t *testing.T) {
			d := changeTestData()
			d.Tickets = []TicketLine{
				{Number: "9001", Seat: "S1 · 7 · 12"},
				{Number: "9002", Seat: "S1 · 7 · 13"},
			}
			out, err := r.Render(TemplateKindInvitation, loc, d)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, body := range []string{out.HTMLBody, out.TextBody} {
				for _, want := range []string{"9001", "9002", "S1 · 7 · 12", "S1 · 7 · 13"} {
					if !strings.Contains(body, want) {
						t.Errorf("%s: the letter lacks %q", loc, want)
					}
				}
			}
			// One ticket keeps the old single-ticket block.
			d.Tickets = nil
			single, err := r.Render(TemplateKindInvitation, loc, d)
			if err != nil {
				t.Fatalf("Render single: %v", err)
			}
			if !strings.Contains(single.TextBody, "TN-1042") {
				t.Errorf("%s: a single-ticket letter lost the ticket id", loc)
			}
		})
	}
}

func TestCancel_InvitationWordingDiffersFromSessionCancellation(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, loc := range SupportedLocales {
		t.Run(loc, func(t *testing.T) {
			session, err := r.Render(TemplateKindCancel, loc, changeTestData())
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			d := changeTestData()
			d.Change.Invitation = true
			inv, err := r.Render(TemplateKindCancel, loc, d)
			if err != nil {
				t.Fatalf("Render invitation: %v", err)
			}
			if inv.Subject == session.Subject || inv.TextBody == session.TextBody || inv.HTMLBody == session.HTMLBody {
				t.Fatalf("%s: the invitation cancel letter reads like the session cancellation", loc)
			}
			if !strings.Contains(inv.Subject, d.EventName) || !strings.Contains(inv.TextBody, d.EventName) {
				t.Errorf("%s: the letter does not name the event", loc)
			}
		})
	}
}

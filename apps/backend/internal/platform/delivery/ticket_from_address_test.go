package delivery

import "testing"

func TestApplyTicketFromAddress(t *testing.T) {
	for _, tmpl := range []string{"", TemplateInvitation} {
		msg := ticketEmailMessage(Payload{Template: tmpl}, "buyer@example.test", "s", "<p>h</p>", "t")
		applyTicketFromAddress(&msg, " tickets@arena.example ")
		if msg.From != "tickets@arena.example" {
			t.Errorf("template %q: From = %q, want the ticket address", tmpl, msg.From)
		}
	}
}

func TestApplyTicketFromAddress_VerifiedOrgSenderWins(t *testing.T) {
	p := Payload{SenderEmail: "box@organizer.example", SenderVerificationStatus: "verified", OrgContactEmail: "box@organizer.example"}
	msg := ticketEmailMessage(p, "buyer@example.test", "s", "<p>h</p>", "t")
	applyTicketFromAddress(&msg, "tickets@arena.example")
	if msg.From != "box@organizer.example" {
		t.Fatalf("From = %q, want the organization's own sender", msg.From)
	}
}

func TestApplyTicketFromAddress_EmptyKeepsDefault(t *testing.T) {
	msg := ticketEmailMessage(Payload{}, "buyer@example.test", "s", "<p>h</p>", "t")
	applyTicketFromAddress(&msg, "")
	if msg.From != "" {
		t.Fatalf("From = %q, want empty (platform default)", msg.From)
	}
}

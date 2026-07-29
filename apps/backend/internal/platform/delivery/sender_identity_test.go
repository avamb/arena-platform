package delivery

import "testing"

func TestTicketEmailUsesOnlyVerifiedOrganizerSender(t *testing.T) {
	verified := ticketEmailMessage(Payload{SenderEmail: "tickets@organizer.example", SenderVerificationStatus: "verified"}, "buyer@example.test", "Ticket", "<p>Ticket</p>", "Ticket")
	if verified.From != "tickets@organizer.example" {
		t.Fatalf("From = %q", verified.From)
	}
	pending := ticketEmailMessage(Payload{SenderEmail: "tickets@organizer.example", SenderVerificationStatus: "pending"}, "buyer@example.test", "Ticket", "<p>Ticket</p>", "Ticket")
	if pending.From != "" {
		t.Fatalf("unverified From = %q", pending.From)
	}
}

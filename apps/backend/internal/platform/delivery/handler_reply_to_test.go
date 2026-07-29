package delivery

import (
	"testing"
)

func TestHandlerSetsReplyToFromOrganizerContactEmail(t *testing.T) {
	msg := ticketEmailMessage(Payload{OrgContactEmail: " organizer@example.test "}, "buyer@example.test", "Your ticket", "<p>Ticket</p>", "Ticket")
	if got, want := msg.ReplyTo, "organizer@example.test"; got != want {
		t.Errorf("ReplyTo = %q, want %q", got, want)
	}
}

func TestHandlerOmitsReplyToWithoutOrganizerContactEmail(t *testing.T) {
	msg := ticketEmailMessage(Payload{}, "buyer@example.test", "Your ticket", "<p>Ticket</p>", "Ticket")
	if msg.ReplyTo != "" {
		t.Errorf("ReplyTo = %q, want empty", msg.ReplyTo)
	}
}

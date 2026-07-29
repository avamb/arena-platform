package email

import (
	"strings"
	"testing"
)

func TestBuildMIMEMessageIncludesReplyToWhenPresent(t *testing.T) {
	raw, err := buildMIMEMessage("tickets@arena.example", Message{
		To:       "buyer@example.test",
		Subject:  "Your ticket",
		ReplyTo:  "organizer@example.test",
		HTMLBody: "<p>Ticket</p>",
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}
	if !strings.Contains(string(raw), "\r\nReply-To: organizer@example.test\r\n") {
		t.Fatalf("MIME message missing Reply-To header:\n%s", raw)
	}
}

func TestBuildMIMEMessageOmitsReplyToWhenEmpty(t *testing.T) {
	raw, err := buildMIMEMessage("tickets@arena.example", Message{
		To:       "buyer@example.test",
		Subject:  "Your ticket",
		HTMLBody: "<p>Ticket</p>",
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}
	if strings.Contains(string(raw), "Reply-To:") {
		t.Fatalf("MIME message unexpectedly contains Reply-To header:\n%s", raw)
	}
}

func TestSMTPSenderRejectsInvalidSenderOverride(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{From: "tickets@arena.example"})
	if err := s.Send(t.Context(), Message{From: "not an email", To: "buyer@example.test"}); err == nil {
		t.Fatal("expected invalid From error")
	}
}

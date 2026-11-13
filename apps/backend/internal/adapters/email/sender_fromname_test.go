package email

import (
	"context"
	"net"
	"strings"
	"testing"
)

func sendAndCapture(t *testing.T, cfg SMTPConfig, msg Message) string {
	t.Helper()
	addr, got := fakeSMTP(t, "250 OK")
	host, port, _ := net.SplitHostPort(addr)
	cfg.Host, cfg.Port = host, port
	if err := NewSMTPSender(cfg).Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	return <-got
}

func TestSMTPSender_DefaultFromNameApplied(t *testing.T) {
	cfg := SMTPConfig{From: "info@arena.example", FromName: "ArenaSoldOut", PlatformFroms: []string{"tickets@arena.example"}}
	raw := sendAndCapture(t, cfg, Message{To: "b@example.com", Subject: "x", HTMLBody: "x"})
	if !strings.Contains(raw, "From: \"ArenaSoldOut\" <info@arena.example>") && !strings.Contains(raw, "From: ArenaSoldOut <info@arena.example>") {
		t.Fatalf("default name missing:\n%s", raw)
	}
	raw = sendAndCapture(t, cfg, Message{From: "Tickets@Arena.example", To: "b@example.com", Subject: "x", HTMLBody: "x"})
	if !strings.Contains(raw, "ArenaSoldOut") {
		t.Fatalf("ticket address (case-insensitive) must get the name:\n%s", raw)
	}
}

func TestSMTPSender_DefaultFromNameNotAppliedForOrgSenderOrOwnName(t *testing.T) {
	cfg := SMTPConfig{From: "info@arena.example", FromName: "ArenaSoldOut", PlatformFroms: []string{"tickets@arena.example"}}
	raw := sendAndCapture(t, cfg, Message{From: "box@organizer.example", To: "b@example.com", Subject: "x", HTMLBody: "x"})
	if strings.Contains(raw, "ArenaSoldOut") || !strings.Contains(raw, "From: box@organizer.example") {
		t.Fatalf("org sender must stay bare:\n%s", raw)
	}
	raw = sendAndCapture(t, cfg, Message{FromName: "Vino", To: "b@example.com", Subject: "x", HTMLBody: "x"})
	if strings.Contains(raw, "ArenaSoldOut") || !strings.Contains(raw, "Vino") {
		t.Fatalf("own FromName must win:\n%s", raw)
	}
}

func TestSMTPSender_HeaderFromFor(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{From: "info@arena.example", FromName: "ArenaSoldOut", PlatformFroms: []string{"tickets@arena.example"}})
	if got := s.HeaderFromFor(Message{}); !strings.Contains(got, "ArenaSoldOut") || !strings.Contains(got, "<info@arena.example>") {
		t.Fatalf("default = %q", got)
	}
	if got := s.HeaderFromFor(Message{From: "tickets@arena.example"}); !strings.Contains(got, "<tickets@arena.example>") || !strings.Contains(got, "ArenaSoldOut") {
		t.Fatalf("ticket = %q", got)
	}
	if got := s.HeaderFromFor(Message{From: "box@organizer.example"}); got != "box@organizer.example" {
		t.Fatalf("org = %q", got)
	}
}

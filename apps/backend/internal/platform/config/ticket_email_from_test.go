package config

import (
	"strings"
	"testing"
)

func TestValidate_TicketEmailFrom(t *testing.T) {
	cfg := validBase()
	cfg.TicketEmailFrom = "tickets@example.com"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("bare address must pass: %v", err)
	}
	for _, bad := range []string{"not an email", "Arena <tickets@example.com>"} {
		cfg := validBase()
		cfg.TicketEmailFrom = bad
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "TICKET_EMAIL_FROM") {
			t.Errorf("%q: want a TICKET_EMAIL_FROM error, got %v", bad, err)
		}
	}
	cfg = validBase()
	cfg.TicketEmailFrom = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty must pass: %v", err)
	}
}

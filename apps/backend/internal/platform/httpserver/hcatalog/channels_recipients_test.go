package hcatalog

import (
	"fmt"
	"strings"
	"testing"
)

// A channel's settings may not map a contact-form topic to more than ten
// addresses; anything else in the settings is untouched.
func TestNormalizeChannelSettings_CapsContactFormRecipients(t *testing.T) {
	list := make([]string, 11)
	for i := range list {
		list[i] = fmt.Sprintf("%q", fmt.Sprintf("o%d@example.com", i))
	}
	eleven := `{"contact_form":{"recipients":{"contact":[` + strings.Join(list, ",") + `]}}}`
	if _, msg := NormalizeChannelSettings([]byte(eleven)); !strings.Contains(msg, "at most 10") {
		t.Fatalf("eleven recipients: %q, want a refusal", msg)
	}
	ten := `{"contact_form":{"recipients":{"contact":[` + strings.Join(list[:10], ",") + `]}},"site":{"origins":["https://a.example"]}}`
	if out, msg := NormalizeChannelSettings([]byte(ten)); msg != "" || string(out) != ten {
		t.Fatalf("ten recipients: %q %s", msg, out)
	}
}

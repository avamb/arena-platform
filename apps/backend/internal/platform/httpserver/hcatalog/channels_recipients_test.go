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

// settings.site.outbound_params (spec 63 section 19.5) is checked when written.
func TestNormalizeChannelSettings_OutboundParams(t *testing.T) {
	good := `{"site":{"outbound_params":{"utm_source":"lampyrisevents","sellers":{"bilesuparadize.lv":{"utm":false}}}}}`
	if _, msg := NormalizeChannelSettings([]byte(good)); msg != "" {
		t.Fatalf("good rule refused: %s", msg)
	}
	for _, bad := range []string{
		`{"site":{"outbound_params":{"utm_medium":"x"}}}`,
		`{"site":{"outbound_params":{"utm_source":"x","sellers":{"WWW.A.com":{}}}}}`,
		`{"site":{"outbound_params":[]}}`,
	} {
		if _, msg := NormalizeChannelSettings([]byte(bad)); msg == "" {
			t.Errorf("accepted %s", bad)
		}
	}
}

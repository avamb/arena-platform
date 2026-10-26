package authemail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func sendApproved(t *testing.T, h *Handler, sender *captureSender, locale string) captureMessage {
	t.Helper()
	raw, err := json.Marshal(OnboardingEmailPayload{
		Kind: OnboardingKindApproved, ApplicationID: uuid.NewString(), Email: "anna@example.test",
		UserID: uuid.NewString(), Locale: locale, OrgName: "Teatr",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.HandleOnboardingEmail(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	return captureMessage{html: sender.message.HTMLBody, text: sender.message.TextBody}
}

type captureMessage struct{ html, text string }

func TestApprovedEmail_ButtonFollowsTheRecipient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		organizer   bool
		eventCenter string
		locale      string
		wantLink    string
		wantButton  string
		forbidden   string
	}{
		{"organizer en", true, testEventCenter, "en", testEventCenter, "Open the event center", testConsole},
		{"organizer ru", true, testEventCenter, "ru", testEventCenter, "Открыть ивент-центр", testConsole},
		{"organizer es", true, testEventCenter, "es", testEventCenter, "Abrir el centro de eventos", testConsole},
		{"staff keeps the console", false, testEventCenter, "en", testConsole, "Sign in to Arena", testEventCenter},
		{"organizer without event center", true, "", "en", testConsole, "Sign in to Arena", "events.arena"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecipients{rec: PasswordRecipient{Organizer: tc.organizer}}
			h, sender := organizerHandler(t, rec, tc.eventCenter)
			m := sendApproved(t, h, sender, tc.locale)
			if !strings.Contains(m.text, tc.wantButton+": "+tc.wantLink) {
				t.Fatalf("want button %q -> %q, got:\n%s", tc.wantButton, tc.wantLink, m.text)
			}
			if !strings.Contains(m.html, `href="`+tc.wantLink+`"`) {
				t.Fatalf("html has no link %q:\n%s", tc.wantLink, m.html)
			}
			if strings.Contains(m.text, tc.forbidden) {
				t.Fatalf("letter must not mention %q:\n%s", tc.forbidden, m.text)
			}
		})
	}
}

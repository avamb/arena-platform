package authemail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func sendOrgAdded(t *testing.T, h *Handler, p OrgAddedEmailPayload) error {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return h.HandleOrgAddedEmail(context.Background(), raw)
}

func TestOrgAddedLetter_RendersInEveryLanguageWithTheEventCenterAndNoToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		locale, greeting, org string
	}{
		{"en", "Hello!", "Theatre Nova"},
		{"ru", "Здравствуйте!", "Театр Нова"},
		{"es", "¡Hola!", "Teatro Nova"},
		{"cs-CZ", "Hello!", "Divadlo Nova"}, // not a letter language: falls back to en
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			t.Parallel()
			h, sender := organizerHandler(t, &fakeRecipients{}, testEventCenter)
			if err := sendOrgAdded(t, h, OrgAddedEmailPayload{
				UserID: uuid.NewString(), Email: "anna@example.test", OrgName: tc.org, Locale: tc.locale,
			}); err != nil {
				t.Fatal(err)
			}
			m := sender.message
			if m.To != "anna@example.test" {
				t.Fatalf("to = %q", m.To)
			}
			if !strings.Contains(m.Subject, tc.org) {
				t.Fatalf("subject %q does not name the organization", m.Subject)
			}
			for name, body := range map[string]string{"text": m.TextBody, "html": m.HTMLBody} {
				if !strings.Contains(body, testEventCenter+"/login") {
					t.Fatalf("%s has no sign-in address:\n%s", name, body)
				}
				if !strings.Contains(body, testEventCenter+"/forgot-password") {
					t.Fatalf("%s has no way to a new password:\n%s", name, body)
				}
				if strings.Contains(body, "token=") || strings.Contains(body, "accept-invite") {
					t.Fatalf("%s carries a setup link for an existing account:\n%s", name, body)
				}
				if strings.Contains(body, testConsole) {
					t.Fatalf("%s points at the console:\n%s", name, body)
				}
			}
			if !strings.Contains(m.TextBody, tc.greeting) {
				t.Fatalf("greeting %q missing:\n%s", tc.greeting, m.TextBody)
			}
			if !strings.Contains(m.TextBody, "anna@example.test") {
				t.Fatalf("the sign-in e-mail is not named:\n%s", m.TextBody)
			}
		})
	}
}

func TestOrgAddedLetter_WithoutAnOrganizationNameReadsGenerically(t *testing.T) {
	t.Parallel()
	subject, _, text := renderOrgAddedEmail("en", "", "anna@example.test", testEventCenter)
	if strings.Contains(subject, "  ") || strings.HasPrefix(subject, " ") || strings.HasPrefix(subject, "%") {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.Contains(text, "You were added to an organization") {
		t.Fatalf("generic intro missing:\n%s", text)
	}
}

func TestOrgAddedLetter_NeedsTheEventCenterAddress(t *testing.T) {
	t.Parallel()
	h, sender := organizerHandler(t, &fakeRecipients{}, "")
	h.eventCenter = ""
	err := sendOrgAdded(t, h, OrgAddedEmailPayload{UserID: uuid.NewString(), Email: "anna@example.test"})
	if err == nil {
		t.Fatal("a letter without the event center address must fail so the job retries")
	}
	if sender.message.To != "" {
		t.Fatal("nothing may be sent without the address")
	}
}

func TestOrgAddedLetter_RejectsAnEmptyRecipient(t *testing.T) {
	t.Parallel()
	h, _ := organizerHandler(t, &fakeRecipients{}, testEventCenter)
	if err := sendOrgAdded(t, h, OrgAddedEmailPayload{UserID: uuid.NewString()}); err == nil {
		t.Fatal("a payload without an e-mail must fail")
	}
}

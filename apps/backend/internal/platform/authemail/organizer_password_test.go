package authemail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeRecipients struct {
	rec   PasswordRecipient
	err   error
	calls int
}

func (f *fakeRecipients) PasswordRecipient(context.Context, uuid.UUID) (PasswordRecipient, error) {
	f.calls++
	return f.rec, f.err
}

const (
	testConsole     = "https://app.arena.example"
	testEventCenter = "https://events.arena.example"
)

var letterNow = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func organizerHandler(t *testing.T, rec *fakeRecipients, eventCenter string) (*Handler, *captureSender) {
	t.Helper()
	sender := &captureSender{}
	h := NewHandler(HandlerOptions{
		Sender:         sender,
		AppPublicURL:   testConsole,
		EventCenterURL: eventCenter + "/",
		Recipients:     rec,
		Now:            func() time.Time { return letterNow },
	})
	return h, sender
}

func sendPasswordLetter(t *testing.T, h *Handler, p PasswordResetEmailPayload) error {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return h.HandlePasswordResetEmail(context.Background(), raw)
}

func TestPasswordLetter_OrganizerGetsTheEventCenterLink(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, purpose, want string
	}{
		{"reset", PurposePasswordReset, testEventCenter + "/reset-password?token=tok"},
		{"account setup", PurposeAccountSetup, testEventCenter + "/accept-invite?token=tok&email=anna%40example.test"},
		{"invitation", PurposeOrgInvitation, testEventCenter + "/accept-invite?token=tok&email=anna%40example.test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecipients{rec: PasswordRecipient{Organizer: true, Locale: "ru"}}
			h, sender := organizerHandler(t, rec, testEventCenter)
			err := sendPasswordLetter(t, h, PasswordResetEmailPayload{
				UserID: uuid.NewString(), Email: "anna@example.test", Token: "tok",
				ExpiresAt: letterNow.Add(time.Hour), Purpose: tc.purpose, OrgName: "Театр",
			})
			if err != nil {
				t.Fatal(err)
			}
			m := sender.message
			if !strings.Contains(m.TextBody, tc.want) {
				t.Fatalf("text has no %q:\n%s", tc.want, m.TextBody)
			}
			if strings.Contains(m.TextBody, testConsole) || strings.Contains(m.HTMLBody, testConsole) {
				t.Fatal("an organizer's letter must not point at the console")
			}
			if !strings.Contains(m.HTMLBody, `lang="ru"`) || !strings.Contains(m.TextBody, "Здравствуйте!") {
				t.Fatalf("the letter must follow the stored locale:\n%s", m.TextBody)
			}
			// The reader can always get a fresh link from the event center.
			if !strings.Contains(m.TextBody, testEventCenter+"/forgot-password") {
				t.Fatalf("no way to a new link:\n%s", m.TextBody)
			}
		})
	}
}

func TestPasswordLetter_PlatformUserKeepsTheConsoleLink(t *testing.T) {
	t.Parallel()
	rec := &fakeRecipients{rec: PasswordRecipient{Organizer: false, Locale: "ru"}}
	h, sender := organizerHandler(t, rec, testEventCenter)
	if err := sendPasswordLetter(t, h, PasswordResetEmailPayload{
		UserID: uuid.NewString(), Email: "op@example.test", Token: "tok", ExpiresAt: letterNow.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.message.TextBody, testConsole+"/reset-password?token=tok") {
		t.Fatalf("platform user must get the console link:\n%s", sender.message.TextBody)
	}
	if strings.Contains(sender.message.TextBody, testEventCenter) {
		t.Fatal("platform user must not be sent to the event center")
	}
}

func TestPasswordLetter_NoEventCenterAddressKeepsTheConsole(t *testing.T) {
	t.Parallel()
	rec := &fakeRecipients{rec: PasswordRecipient{Organizer: true}}
	h, sender := organizerHandler(t, rec, "")
	if err := sendPasswordLetter(t, h, PasswordResetEmailPayload{
		UserID: uuid.NewString(), Email: "a@example.test", Token: "tok", ExpiresAt: letterNow,
	}); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 0 {
		t.Fatalf("lookup ran %d times without an event center address", rec.calls)
	}
	if !strings.Contains(sender.message.TextBody, testConsole+"/reset-password") {
		t.Fatalf("want the console link:\n%s", sender.message.TextBody)
	}
}

func TestPasswordLetter_PayloadWithoutUserIDKeepsTheConsole(t *testing.T) {
	t.Parallel()
	rec := &fakeRecipients{rec: PasswordRecipient{Organizer: true}}
	h, sender := organizerHandler(t, rec, testEventCenter)
	if err := sendPasswordLetter(t, h, PasswordResetEmailPayload{
		Email: "a@example.test", Token: "tok", ExpiresAt: letterNow,
	}); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 0 || !strings.Contains(sender.message.TextBody, testConsole) {
		t.Fatalf("calls=%d text=%s", rec.calls, sender.message.TextBody)
	}
}

func TestPasswordLetter_LookupFailureRetriesAndSendsNothing(t *testing.T) {
	t.Parallel()
	rec := &fakeRecipients{err: errors.New("db down")}
	h, sender := organizerHandler(t, rec, testEventCenter)
	err := sendPasswordLetter(t, h, PasswordResetEmailPayload{
		UserID: uuid.NewString(), Email: "a@example.test", Token: "tok", ExpiresAt: letterNow,
	})
	if err == nil {
		t.Fatal("a failed lookup must fail the job so the worker retries")
	}
	if sender.message.To != "" {
		t.Fatal("nothing may be sent when the recipient is unknown")
	}
}

func TestOrganizerLetter_ExplainsEveryStepInEachLanguage(t *testing.T) {
	t.Parallel()
	expires := letterNow.Add(7 * 24 * time.Hour)
	link := testEventCenter + "/accept-invite?token=tok&email=anna%40example.test"
	cases := []struct {
		locale   string
		subject  string
		contains []string
	}{
		{"en", "Teatro <Sol> invites you to the Arena event center", []string{
			"1. Press the \u201cChoose a password\u201d button below.",
			"4. Sign in. Your e-mail is anna@example.test.",
			"It is valid for 7 days, until October 21, 2026, 10:00 UTC.",
		}},
		{"ru-RU", "Teatro <Sol> приглашает вас в ивент-центр Arena", []string{
			"1. Нажмите кнопку «Задать пароль» ниже.",
			"4. Войдите. Ваш адрес: anna@example.test.",
			"Она действует 7 дней, до 21 октября 2026, 10:00 по UTC.",
		}},
		{"es", "Teatro <Sol> te invita al centro de eventos de Arena", []string{
			"1. Pulsa el botón «Elegir contraseña» de abajo.",
			"4. Entra. Tu correo es anna@example.test.",
			"Es válido durante 7 días, hasta el 21 de octubre de 2026, 10:00 UTC.",
		}},
		{"he", "Teatro <Sol> invites you to the Arena event center", nil}, // falls back to English
	}
	for _, tc := range cases {
		subject, htmlBody, text := renderOrganizerPasswordEmail(tc.locale, PurposeOrgInvitation,
			"Teatro\r\n <Sol>", "anna@example.test", testEventCenter, link, expires, letterNow)
		if subject != tc.subject {
			t.Errorf("%s: subject %q, want %q", tc.locale, subject, tc.subject)
		}
		for _, want := range tc.contains {
			if !strings.Contains(text, want) {
				t.Errorf("%s: text has no %q:\n%s", tc.locale, want, text)
			}
		}
		if strings.Contains(htmlBody, "<Sol>") || !strings.Contains(htmlBody, "&lt;Sol&gt;") {
			t.Errorf("%s: organization name must be escaped in HTML", tc.locale)
		}
		if !strings.Contains(htmlBody, `href="`+strings.ReplaceAll(link, "&", "&amp;")+`"`) {
			t.Errorf("%s: button link missing", tc.locale)
		}
	}
}

func TestOrganizerLetter_ResetSaysHowLongTheLinkLives(t *testing.T) {
	t.Parallel()
	_, _, text := renderOrganizerPasswordEmail("ru", PurposePasswordReset, "", "a@example.test",
		testEventCenter, testEventCenter+"/reset-password?token=t", letterNow.Add(59*time.Minute), letterNow)
	for _, want := range []string{
		"Новый пароль",
		"Запрос пришёл для адреса a@example.test.",
		"Она действует 1 час, до 14 октября 2026, 10:59 по UTC.",
		"Если вы не просили новый пароль, ничего не делайте. Ваш пароль останется прежним.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
}

func TestPlural_RussianAndTwoFormRules(t *testing.T) {
	t.Parallel()
	ru := organizerTexts["ru"]
	en := organizerTexts["en"]
	for n, want := range map[int]string{1: "1 день", 2: "2 дня", 5: "5 дней", 11: "11 дней", 21: "21 день", 22: "22 дня"} {
		if got := ru.days(n); got != want {
			t.Errorf("ru %d: %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{1: "1 hour", 2: "2 hours", 21: "21 hours"} {
		if got := en.hours(n); got != want {
			t.Errorf("en %d: %q, want %q", n, got, want)
		}
	}
}

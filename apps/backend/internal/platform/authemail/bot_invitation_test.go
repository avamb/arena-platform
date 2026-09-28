package authemail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBotDeepLink(t *testing.T) {
	t.Parallel()
	want := "https://t.me/ArenaEventsCentrBot?start=inv_abc-DEF_123"
	for _, username := range []string{"ArenaEventsCentrBot", "@ArenaEventsCentrBot", " @ArenaEventsCentrBot "} {
		if got := BotDeepLink(username, "abc-DEF_123"); got != want {
			t.Errorf("BotDeepLink(%q) = %q; want %q", username, got, want)
		}
	}
}

func TestHandleBotInvitationEmail_MailsTheDeepLink(t *testing.T) {
	t.Parallel()
	sender := &captureSender{}
	h := NewHandler(HandlerOptions{Sender: sender, BotUsername: "@ArenaEventsCentrBot"})
	expires := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(BotInvitationEmailPayload{
		UserID:    "u1",
		Email:     "owner@example.test",
		Code:      "c0de_-X",
		ExpiresAt: expires,
		OrgName:   "Lampyris <s.r.o.>",
		Role:      "owner",
		Locale:    "ru",
	})
	if err := h.HandleBotInvitationEmail(context.Background(), payload); err != nil {
		t.Fatalf("HandleBotInvitationEmail: %v", err)
	}
	msg := sender.message
	if msg.To != "owner@example.test" {
		t.Errorf("To = %q", msg.To)
	}
	link := "https://t.me/ArenaEventsCentrBot?start=inv_c0de_-X"
	if !strings.Contains(msg.TextBody, link) {
		t.Errorf("text body lacks the deep link %q:\n%s", link, msg.TextBody)
	}
	if !strings.Contains(msg.HTMLBody, `href="`+link+`"`) {
		t.Errorf("html body lacks the deep link href:\n%s", msg.HTMLBody)
	}
	if !strings.Contains(msg.Subject, "Lampyris <s.r.o.>") {
		t.Errorf("subject should name the organization: %q", msg.Subject)
	}
	if strings.Contains(msg.HTMLBody, "<s.r.o.>") {
		t.Errorf("organization name must be HTML-escaped in the body")
	}
	if !strings.Contains(msg.HTMLBody, "владельца") {
		t.Errorf("ru body should name the owner role: %s", msg.HTMLBody)
	}
	if !strings.Contains(msg.TextBody, "2026-10-06T12:00:00Z") {
		t.Errorf("text body should state the expiry: %s", msg.TextBody)
	}
}

func TestHandleBotInvitationEmail_DefaultsToEnglish(t *testing.T) {
	t.Parallel()
	sender := &captureSender{}
	h := NewHandler(HandlerOptions{Sender: sender, BotUsername: "ArenaEventsCentrBot"})
	payload, _ := json.Marshal(BotInvitationEmailPayload{
		Email: "m@example.test", Code: "c", ExpiresAt: time.Now().Add(time.Hour), Role: "manager", Locale: "he",
	})
	if err := h.HandleBotInvitationEmail(context.Background(), payload); err != nil {
		t.Fatalf("HandleBotInvitationEmail: %v", err)
	}
	if !strings.Contains(sender.message.Subject, "invitation to the Arena events bot") {
		t.Errorf("unknown locale should fall back to English: %q", sender.message.Subject)
	}
	if !strings.Contains(sender.message.TextBody, "role: manager") {
		t.Errorf("en body should name the manager role: %s", sender.message.TextBody)
	}
}

func TestHandleBotInvitationEmail_RefusesWithoutBotUsername(t *testing.T) {
	t.Parallel()
	sender := &captureSender{}
	h := NewHandler(HandlerOptions{Sender: sender})
	payload, _ := json.Marshal(BotInvitationEmailPayload{Email: "m@example.test", Code: "c", ExpiresAt: time.Now()})
	err := h.HandleBotInvitationEmail(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "EVENTS_TELEGRAM_BOT_USERNAME") {
		t.Fatalf("expected a configuration error, got %v", err)
	}
	if sender.message.To != "" {
		t.Fatalf("nothing must be sent without a username; sent to %q", sender.message.To)
	}
}

func TestHandleBotInvitationEmail_RejectsIncompletePayload(t *testing.T) {
	t.Parallel()
	h := NewHandler(HandlerOptions{Sender: &captureSender{}, BotUsername: "b"})
	for _, raw := range []string{`{"email":"a@b.c"}`, `{"code":"x"}`, `not json`} {
		if err := h.HandleBotInvitationEmail(context.Background(), []byte(raw)); err == nil {
			t.Errorf("payload %s must be rejected", raw)
		}
	}
}

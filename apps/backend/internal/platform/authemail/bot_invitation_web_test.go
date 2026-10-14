package authemail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The bot invitation names the web event center when it is configured, and
// stays as it was without it (spec 37, owner decision 2026-10-14).
func TestHandleBotInvitationEmail_NamesTheWebEventCenter(t *testing.T) {
	t.Parallel()
	payload, _ := json.Marshal(BotInvitationEmailPayload{
		UserID: "u1", Email: "manager@example.test", Code: "c0de",
		ExpiresAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), Role: "manager", Locale: "es",
	})
	for _, tc := range []struct {
		name, eventCenter string
		want              bool
	}{
		{"configured", "https://events.example.test/", true},
		{"not configured", "", false},
	} {
		sender := &captureSender{}
		h := NewHandler(HandlerOptions{Sender: sender, BotUsername: "ArenaBot", EventCenterURL: tc.eventCenter})
		if err := h.HandleBotInvitationEmail(context.Background(), payload); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		msg := sender.message
		hasText := strings.Contains(msg.TextBody, "https://events.example.test") && strings.Contains(msg.TextBody, "Abrir en el navegador")
		hasHTML := strings.Contains(msg.HTMLBody, `href="https://events.example.test"`)
		if hasText != tc.want || hasHTML != tc.want {
			t.Errorf("%s: web line text=%v html=%v, want %v\n%s", tc.name, hasText, hasHTML, tc.want, msg.TextBody)
		}
		if !strings.Contains(msg.TextBody, "https://t.me/ArenaBot?start=inv_c0de") {
			t.Errorf("%s: the deep link is gone", tc.name)
		}
		if !strings.HasSuffix(strings.TrimSpace(msg.TextBody), "Arena Platform — mensaje automático") {
			t.Errorf("%s: the footer must stay last:\n%s", tc.name, msg.TextBody)
		}
	}
}

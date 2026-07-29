package authemail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
)

type captureSender struct{ message emailadapter.Message }

func (s *captureSender) Send(_ context.Context, message emailadapter.Message) error {
	s.message = message
	return nil
}

func TestEmailLinksLandOnSPA(t *testing.T) {
	t.Parallel()

	const appURL = "https://app.arenasoldout.example/"
	const token = "reset-token"
	tests := []struct {
		name    string
		handle  func(*Handler, []byte) error
		payload any
		wantURL string
	}{
		{
			name: "password reset",
			handle: func(h *Handler, payload []byte) error {
				return h.HandlePasswordResetEmail(context.Background(), payload)
			},
			payload: PasswordResetEmailPayload{Email: "user@example.test", Token: token, ExpiresAt: time.Now()},
			wantURL: "https://app.arenasoldout.example/reset-password?token=" + token,
		},
		{
			name: "email verification",
			handle: func(h *Handler, payload []byte) error {
				return h.HandleEmailVerification(context.Background(), payload)
			},
			payload: VerificationEmailPayload{Email: "user@example.test", Token: token, ExpiresAt: time.Now()},
			wantURL: "https://app.arenasoldout.example/verify-email?token=" + token,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &captureSender{}
			handler := NewHandler(HandlerOptions{Sender: sender, AppPublicURL: appURL})
			payload, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if err := tt.handle(handler, payload); err != nil {
				t.Fatalf("handle email: %v", err)
			}
			if !strings.Contains(sender.message.HTMLBody, tt.wantURL) || !strings.Contains(sender.message.TextBody, tt.wantURL) {
				t.Fatalf("email does not contain SPA URL %q", tt.wantURL)
			}
			if strings.Contains(sender.message.TextBody, "/v1/auth/") {
				t.Fatalf("email unexpectedly links directly to API: %s", sender.message.TextBody)
			}
		})
	}
}

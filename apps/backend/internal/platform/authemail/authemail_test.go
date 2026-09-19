package authemail

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"log/slog"
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

	tests = append(tests,
		struct {
			name    string
			handle  func(*Handler, []byte) error
			payload any
			wantURL string
		}{
			name: "admin-created account setup",
			handle: func(h *Handler, payload []byte) error {
				return h.HandlePasswordResetEmail(context.Background(), payload)
			},
			payload: PasswordResetEmailPayload{Email: "new+op@example.test", Token: token, ExpiresAt: time.Now(), Purpose: PurposeAccountSetup},
			wantURL: "https://app.arenasoldout.example/accept-invite?token=" + token + "&email=new%2Bop%40example.test",
		},
		struct {
			name    string
			handle  func(*Handler, []byte) error
			payload any
			wantURL string
		}{
			name: "organization invitation",
			handle: func(h *Handler, payload []byte) error {
				return h.HandlePasswordResetEmail(context.Background(), payload)
			},
			payload: PasswordResetEmailPayload{Email: "invitee@example.test", Token: token, ExpiresAt: time.Now(), Purpose: PurposeOrgInvitation, OrgName: "Lampyris"},
			wantURL: "https://app.arenasoldout.example/accept-invite?token=" + token + "&email=invitee%40example.test",
		},
	)

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
			if !strings.Contains(sender.message.HTMLBody, html.EscapeString(tt.wantURL)) || !strings.Contains(sender.message.TextBody, tt.wantURL) {
				t.Fatalf("email does not contain SPA URL %q", tt.wantURL)
			}
			if strings.Contains(sender.message.TextBody, "/v1/auth/") {
				t.Fatalf("email unexpectedly links directly to API: %s", sender.message.TextBody)
			}
		})
	}
}

func TestPasswordEmailPurposes_SubjectAndNoSecretsInLogs(t *testing.T) {
	t.Parallel()

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name        string
		payload     PasswordResetEmailPayload
		wantSubject string
		wantBody    string
		wantPath    string
	}{
		{
			name:        "empty purpose stays the reset email",
			payload:     PasswordResetEmailPayload{UserID: "u1", Email: "a@example.test", Token: token, ExpiresAt: time.Now()},
			wantSubject: "Reset your Arena Platform password",
			wantBody:    "We received a request to reset",
			wantPath:    "/reset-password?token=",
		},
		{
			name:        "unknown purpose falls back to a working reset link",
			payload:     PasswordResetEmailPayload{UserID: "u1", Email: "a@example.test", Token: token, ExpiresAt: time.Now(), Purpose: "from_the_future"},
			wantSubject: "Reset your Arena Platform password",
			wantBody:    "We received a request to reset",
			wantPath:    "/reset-password?token=",
		},
		{
			name:        "account setup",
			payload:     PasswordResetEmailPayload{UserID: "u1", Email: "a@example.test", Token: token, ExpiresAt: time.Now(), Purpose: PurposeAccountSetup},
			wantSubject: "Set up your Arena Platform account",
			wantBody:    "An administrator has created an Arena Platform account for you.",
			wantPath:    "/accept-invite?token=",
		},
		{
			name:        "invitation names the organization, whitespace collapsed",
			payload:     PasswordResetEmailPayload{UserID: "u1", Email: "a@example.test", Token: token, ExpiresAt: time.Now(), Purpose: PurposeOrgInvitation, OrgName: "Vino\r\n&  Co"},
			wantSubject: "You are invited to join Vino & Co on Arena Platform",
			wantBody:    "You have been invited to join Vino & Co on Arena Platform.",
			wantPath:    "/accept-invite?token=",
		},
		{
			name:        "invitation without organization name",
			payload:     PasswordResetEmailPayload{UserID: "u1", Email: "a@example.test", Token: token, ExpiresAt: time.Now(), Purpose: PurposeOrgInvitation},
			wantSubject: "You are invited to Arena Platform",
			wantBody:    "You have been invited to join an organization on Arena Platform.",
			wantPath:    "/accept-invite?token=",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			sender := &captureSender{}
			h := NewHandler(HandlerOptions{
				Sender:       sender,
				AppPublicURL: "https://app.example.test",
				Logger:       slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			})
			raw, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := h.HandlePasswordResetEmail(context.Background(), raw); err != nil {
				t.Fatalf("handle: %v", err)
			}
			if sender.message.To != tt.payload.Email {
				t.Errorf("To = %q; want %q", sender.message.To, tt.payload.Email)
			}
			if sender.message.Subject != tt.wantSubject {
				t.Errorf("Subject = %q; want %q", sender.message.Subject, tt.wantSubject)
			}
			if !strings.Contains(sender.message.TextBody, tt.wantBody) {
				t.Errorf("text body lacks %q:\n%s", tt.wantBody, sender.message.TextBody)
			}
			if !strings.Contains(sender.message.HTMLBody, html.EscapeString(tt.wantBody)) {
				t.Errorf("HTML body lacks escaped %q", tt.wantBody)
			}
			if !strings.Contains(sender.message.TextBody, "https://app.example.test"+tt.wantPath+token) {
				t.Errorf("text body lacks the SPA link %s", tt.wantPath)
			}
			if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "token=") {
				t.Errorf("logs leak the token or link:\n%s", logs.String())
			}
		})
	}
}

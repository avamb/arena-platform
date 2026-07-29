// Package authemail implements durable worker job handlers for auth-related
// transactional email: account email verification (registration) and
// password-reset links.
//
// # Design
//
// Handlers consume jobs from the worker_jobs queue. Job payloads contain the
// user identifier, email address, one-time token, locale, and expiry time.
// The handlers build the clickable link from the validated AppPublicURL
// configuration value — never from untrusted forwarded HTTP headers.
//
// Logs contain job/user identifiers and locale; they never contain the raw
// token or the complete signed URL.
//
// # Locales
//
// The verification email is rendered in the requested locale (currently "en"
// and "ru"); all other locales fall back to English.  The password-reset email
// is always rendered in English because the locale of the original request is
// not reliably available at reset time.
//
// # Failure behaviour
//
// A non-nil error from either handler causes the worker to retry the job
// (up to max_attempts). On exhaustion the job is moved to worker_dead_letter.
// The token is NOT consumed by the email delivery step — it remains valid in
// the database until the user clicks the link or it expires.
package authemail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
)

// ─── job-type constants ────────────────────────────────────────────────────────

// JobTypeEmailVerification is the worker job_type for account-verification emails.
const JobTypeEmailVerification = "auth.email_verification"

// JobTypePasswordResetEmail is the worker job_type for password-reset emails.
const JobTypePasswordResetEmail = "auth.password_reset_email"

// ─── payload types ────────────────────────────────────────────────────────────

// VerificationEmailPayload is the JSON payload stored in worker_jobs.payload
// for auth.email_verification jobs.
//
// The payload is committed inside the same database transaction that creates the
// email_verification_tokens row, so a failed commit rolls back both atomically.
type VerificationEmailPayload struct {
	// UserID is the UUID string of the newly registered user. Included in logs
	// to correlate job execution with the registration event.
	UserID string `json:"user_id"`
	// Email is the recipient address.
	Email string `json:"email"`
	// Token is the 64-character hex verification token stored in the DB. The
	// worker appends it to AppPublicURL to build the clickable link. It is NOT
	// logged by the handler.
	Token string `json:"token"`
	// Locale is the user's requested locale (e.g. "en", "ru"). Determines which
	// template variant is rendered.
	Locale string `json:"locale"`
	// ExpiresAt is the token expiry time; rendered as human-readable text in the
	// email so the user knows how long the link is valid.
	ExpiresAt time.Time `json:"expires_at"`
}

// PasswordResetEmailPayload is the JSON payload stored in worker_jobs.payload
// for auth.password_reset_email jobs.
type PasswordResetEmailPayload struct {
	// UserID is included in logs only; the actual reset is keyed by Token.
	UserID string `json:"user_id"`
	// Email is the recipient address.
	Email string `json:"email"`
	// Token is the 64-character hex reset token. NOT logged.
	Token string `json:"token"`
	// ExpiresAt is rendered in the email body.
	ExpiresAt time.Time `json:"expires_at"`
}

// ─── handler ──────────────────────────────────────────────────────────────────

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	// Sender delivers email messages. Required. Use email.SMTPSender in
	// production and email.LogSender in development / CI.
	Sender emailadapter.Sender
	// AppPublicURL is the canonical SPA origin (e.g. "https://app.example.com").
	// Links land on a SPA route, which calls its separately configured API base
	// URL. Never derive this value from request headers. It defaults to
	// "http://localhost:8080" when empty for local development.
	AppPublicURL string
	// FromAddress is the envelope sender. Passed to the Sender when non-empty;
	// Sender implementations may use their own configured from-address otherwise.
	FromAddress string
	// Logger receives structured log output. Defaults to slog.Default().
	Logger *slog.Logger
}

// Handler provides worker.HandlerFunc implementations for auth email jobs.
// Construct one with NewHandler and register each method with the worker
// registry under the corresponding JobType constant.
type Handler struct {
	sender       emailadapter.Sender
	appPublicURL string // pre-trimmed base URL
	fromAddress  string
	logger       *slog.Logger
}

// NewHandler constructs a Handler from the given options.
func NewHandler(opts HandlerOptions) *Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	base := strings.TrimRight(opts.AppPublicURL, "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	return &Handler{
		sender:       opts.Sender,
		appPublicURL: base,
		fromAddress:  opts.FromAddress,
		logger:       logger,
	}
}

// HandleEmailVerification is the worker.HandlerFunc for auth.email_verification.
//
// It decodes the payload, builds the verification URL from AppPublicURL,
// renders a locale-aware email template, and delivers via the configured Sender.
// The token and complete URL are never written to structured logs.
func (h *Handler) HandleEmailVerification(ctx context.Context, payload []byte) error {
	var p VerificationEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("authemail: decode verification payload: %w", err)
	}
	if p.Email == "" || p.Token == "" {
		return fmt.Errorf("authemail: verification payload missing email or token")
	}

	// Send recipients to the SPA, not an API path. The SPA owns the success
	// state and calls GET /v1/auth/verify with its configured API URL.
	verifyURL := h.appPublicURL + "/verify-email?token=" + p.Token

	h.logger.Info("authemail: sending email verification",
		slog.String("user_id", p.UserID),
		slog.String("locale", p.Locale),
		// token and verifyURL intentionally omitted
	)

	subject, htmlBody, textBody := renderVerificationEmail(p.Locale, verifyURL, p.ExpiresAt)

	if err := h.sender.Send(ctx, emailadapter.Message{
		To:       p.Email,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
	}); err != nil {
		return fmt.Errorf("authemail: send verification email to user %s: %w", p.UserID, err)
	}

	h.logger.Info("authemail: verification email delivered",
		slog.String("user_id", p.UserID),
	)
	return nil
}

// HandlePasswordResetEmail is the worker.HandlerFunc for auth.password_reset_email.
//
// It decodes the payload, builds the reset URL from AppPublicURL, renders the
// email template, and delivers via the configured Sender. The token and complete
// URL are never written to structured logs.
func (h *Handler) HandlePasswordResetEmail(ctx context.Context, payload []byte) error {
	var p PasswordResetEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("authemail: decode password reset payload: %w", err)
	}
	if p.Email == "" || p.Token == "" {
		return fmt.Errorf("authemail: password reset payload missing email or token")
	}

	// Build the reset URL from canonical AppPublicURL — not from request headers.
	// Send recipients to the SPA password form, which POSTs to the separately
	// configured API URL.
	resetURL := h.appPublicURL + "/reset-password?token=" + p.Token

	h.logger.Info("authemail: sending password reset email",
		slog.String("user_id", p.UserID),
		// token and resetURL intentionally omitted
	)

	subject, htmlBody, textBody := renderPasswordResetEmail(resetURL, p.ExpiresAt)

	if err := h.sender.Send(ctx, emailadapter.Message{
		To:       p.Email,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
	}); err != nil {
		return fmt.Errorf("authemail: send password reset email to user %s: %w", p.UserID, err)
	}

	h.logger.Info("authemail: password reset email delivered",
		slog.String("user_id", p.UserID),
	)
	return nil
}

// ─── locale-aware template rendering ──────────────────────────────────────────

// renderVerificationEmail returns the subject, HTML body, and plain-text body
// for a verification email in the requested locale.
//
// Supported locales: "en" (default), "ru". All other values fall back to "en".
// Templates include the token expiry time and the clickable link.
// All user-supplied strings are HTML-escaped before embedding in the HTML body.
func renderVerificationEmail(locale, verifyURL string, expiresAt time.Time) (subject, htmlBody, textBody string) {
	// html.EscapeString encodes <, >, &, ', " — safe to embed in HTML attributes.
	safeURL := html.EscapeString(verifyURL)
	expiry := expiresAt.UTC().Format(time.RFC3339)

	switch normalizeLocale(locale) {
	case "ru":
		subject = "Подтвердите адрес электронной почты Arena Platform"
		htmlBody = fmt.Sprintf(verifyHTMLRu, expiry, safeURL, safeURL)
		textBody = renderVerifyTextRu(verifyURL, expiry)
	default: // "en" and all unsupported locales
		subject = "Verify your Arena Platform email address"
		htmlBody = fmt.Sprintf(verifyHTMLEn, expiry, safeURL, safeURL)
		textBody = renderVerifyTextEn(verifyURL, expiry)
	}
	return
}

// renderPasswordResetEmail returns subject, HTML body, and text body for a
// password-reset email. Always rendered in English.
func renderPasswordResetEmail(resetURL string, expiresAt time.Time) (subject, htmlBody, textBody string) {
	safeURL := html.EscapeString(resetURL)
	expiry := expiresAt.UTC().Format(time.RFC3339)
	subject = "Reset your Arena Platform password"
	htmlBody = fmt.Sprintf(resetHTMLEn, expiry, safeURL, safeURL)
	textBody = renderResetTextEn(resetURL, expiry)
	return
}

// normalizeLocale returns a canonical locale tag, falling back to "en" for
// anything not explicitly supported.
func normalizeLocale(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "ru":
		return "ru"
	default:
		return "en"
	}
}

// ─── HTML + text template strings ─────────────────────────────────────────────
// Templates use fmt.Sprintf with pre-escaped URL and formatted expiry string.
// Positional arguments: [1]=expiry, [2]=safeURL (href), [3]=safeURL (display).

const verifyHTMLEn = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Verify your email</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Verify your email address</h1>
  <p>Thank you for registering with Arena Platform.</p>
  <p>Click the button below to verify your email address.
     This link is valid until <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Verify email
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    If the button does not work, copy and paste this URL into your browser:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    If you did not create an Arena Platform account, you can safely ignore this email.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform &mdash; automated message</p>
</body>
</html>`

const verifyHTMLRu = `<!DOCTYPE html>
<html lang="ru">
<head><meta charset="UTF-8"><title>Подтвердите email</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Подтвердите адрес электронной почты</h1>
  <p>Спасибо за регистрацию на платформе Arena.</p>
  <p>Нажмите кнопку ниже, чтобы подтвердить адрес электронной почты.
     Ссылка действительна до <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Подтвердить email
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    Если кнопка не работает, скопируйте и вставьте эту ссылку в браузер:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    Если вы не регистрировались на платформе Arena, просто проигнорируйте это письмо.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform &mdash; автоматическое сообщение</p>
</body>
</html>`

const resetHTMLEn = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Reset your password</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Reset your password</h1>
  <p>We received a request to reset your Arena Platform password.</p>
  <p>Click the button below to choose a new password.
     This link is valid until <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Reset password
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    If the button does not work, copy and paste this URL into your browser:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    If you did not request a password reset, you can safely ignore this email.
    Your password will not change.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform &mdash; automated message</p>
</body>
</html>`

func renderVerifyTextEn(rawURL, expiry string) string {
	var b bytes.Buffer
	b.WriteString("Verify your Arena Platform email address\n\n")
	b.WriteString("Thank you for registering with Arena Platform.\n\n")
	fmt.Fprintf(&b, "Please open the link below to verify your email address.\nThis link is valid until %s.\n\n", expiry)
	fmt.Fprintf(&b, "%s\n\n", rawURL)
	b.WriteString("If you did not create an Arena Platform account, you can safely ignore this email.\n")
	b.WriteString("\n--\nArena Platform -- automated message\n")
	return b.String()
}

func renderVerifyTextRu(rawURL, expiry string) string {
	var b bytes.Buffer
	b.WriteString("Подтвердите адрес электронной почты Arena Platform\n\n")
	b.WriteString("Спасибо за регистрацию на платформе Arena.\n\n")
	fmt.Fprintf(&b, "Перейдите по ссылке ниже, чтобы подтвердить адрес электронной почты.\nСсылка действительна до %s.\n\n", expiry)
	fmt.Fprintf(&b, "%s\n\n", rawURL)
	b.WriteString("Если вы не регистрировались на платформе Arena, просто проигнорируйте это письмо.\n")
	b.WriteString("\n--\nArena Platform -- автоматическое сообщение\n")
	return b.String()
}

func renderResetTextEn(rawURL, expiry string) string {
	var b bytes.Buffer
	b.WriteString("Reset your Arena Platform password\n\n")
	b.WriteString("We received a request to reset your Arena Platform password.\n\n")
	fmt.Fprintf(&b, "Open the link below to choose a new password.\nThis link is valid until %s.\n\n", expiry)
	fmt.Fprintf(&b, "%s\n\n", rawURL)
	b.WriteString("If you did not request a password reset, you can safely ignore this email.\n")
	b.WriteString("Your password will not change.\n")
	b.WriteString("\n--\nArena Platform -- automated message\n")
	return b.String()
}

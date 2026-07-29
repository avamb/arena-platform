// Package email provides email sending abstractions for arena_new.
//
// Sender is the core interface. Two implementations are provided:
//
//   - LogSender: writes the email to a slog.Logger (dev / test / CI).
//     No network connection is opened; Send always returns nil.
//
//   - SMTPSender: delivers via Go's standard library net/smtp. Supports
//     plain dial with opportunistic STARTTLS, and implicit TLS (port 465).
//     No external dependencies — only Go stdlib.
//
// Feature #141: ticket delivery via email.
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
)

// Message holds all fields required to send a single transactional email.
type Message struct {
	// From optionally overrides the configured platform sender. It is used only
	// for an organizer identity that has already been verified by Brevo.
	From string
	// To is the recipient email address (envelope and header).
	To string
	// Subject is the email subject line (UTF-8; will be Q-encoded in headers).
	Subject string
	// ReplyTo is the optional address recipients' mail clients use for replies.
	// It affects only the RFC 5322 message header; the SMTP envelope sender
	// remains the configured platform address.
	ReplyTo string
	// HTMLBody is the primary body of the email in HTML format.
	HTMLBody string
	// TextBody is the plain-text fallback body. Displayed by clients that
	// do not render HTML, and by accessibility tools.
	TextBody string
	// Attachments are optional files to attach to the email.
	Attachments []Attachment
}

// Attachment represents a file attached to an outgoing email.
type Attachment struct {
	// Filename is the name displayed in the email client's attachment list.
	Filename string
	// ContentType is the MIME type (e.g. "application/pdf", "image/png").
	ContentType string
	// Data is the raw attachment bytes (not base64-encoded — the sender
	// encodes them when building the MIME message).
	Data []byte
}

// Sender is the interface all email-sending adapters must satisfy.
//
// Implementations must be safe for concurrent use from multiple goroutines.
// A non-nil error from Send indicates a transient failure that callers
// (typically the worker handler) should treat as retriable.
type Sender interface {
	// Send delivers the email described by msg.
	// Returns nil on success, or a non-nil error on failure.
	Send(ctx context.Context, msg Message) error
}

// ──────────────────────────────────────────────────────────────────────────────
// LogSender — dev / test implementation
// ──────────────────────────────────────────────────────────────────────────────

// DevOnlySender is an optional marker interface for email.Sender implementations
// that are NOT suitable for production use. A DevOnlySender does not open a
// real network connection and must not be used where honest delivery status is
// required.
//
// Callers (such as the ticket delivery worker handler) MUST check IsDevOnly
// before recording delivery_jobs.status='sent': a dev-only sender that returns
// nil from Send has not actually delivered the message.
type DevOnlySender interface {
	Sender
	// DevOnly returns true, indicating this sender is restricted to
	// development / CI / test environments.
	DevOnly() bool
}

// IsDevOnly returns true when s is a non-production, development-only sender
// (e.g. LogSender). Returns false for nil (nil means no sender at all, which
// is also non-production, but callers handle nil separately).
func IsDevOnly(s Sender) bool {
	if d, ok := s.(DevOnlySender); ok {
		return d.DevOnly()
	}
	return false
}

// LogSender writes email content to a slog.Logger instead of delivering it
// via SMTP. Use this in development, CI, and unit tests where a real SMTP
// server is unavailable.
//
// Each Send call emits a single structured log line with the recipient, subject,
// attachment count, and the first 200 characters of the text body.
//
// LogSender implements DevOnlySender: callers MUST NOT record delivery
// status 'sent' after a LogSender.Send call — the message was logged,
// not delivered.
type LogSender struct {
	// Logger receives the email log entries. Falls back to slog.Default() when nil.
	Logger *slog.Logger
}

// DevOnly implements DevOnlySender and returns true, marking LogSender as a
// development-only sender restricted to non-production environments.
func (s *LogSender) DevOnly() bool { return true }

// Send logs the email and always returns nil (never fails).
func (s *LogSender) Send(_ context.Context, msg Message) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("email: (log-only) LogSender — email not sent",
		slog.String("to", msg.To),
		slog.String("subject", msg.Subject),
		slog.Int("attachments", len(msg.Attachments)),
		slog.String("text_preview", truncateStr(msg.TextBody, 200)),
	)
	return nil
}

// compile-time assertions
var _ Sender = (*LogSender)(nil)
var _ DevOnlySender = (*LogSender)(nil)

// ──────────────────────────────────────────────────────────────────────────────
// SMTPSender — production implementation (net/smtp, no external deps)
// ──────────────────────────────────────────────────────────────────────────────

// SMTPConfig holds the SMTP connection parameters for SMTPSender.
type SMTPConfig struct {
	// Host is the SMTP server hostname (e.g. "smtp.mailgun.org", "mailhog").
	Host string
	// Port is the SMTP server port as a string (e.g. "25", "465", "587").
	Port string
	// Username is the SMTP AUTH username. Leave empty to skip authentication.
	Username string
	// Password is the SMTP AUTH password.
	Password string
	// From is the envelope sender address (e.g. "tickets@arena.example.com").
	From string
	// UseTLS enables implicit TLS on connect (port 465 style). When false,
	// opportunistic STARTTLS is used instead.
	UseTLS bool
}

// SMTPSender sends email via Go's standard net/smtp package. Each Send call
// opens a fresh TCP connection, delivers the message, and closes the connection.
// This is correct behaviour for low-volume transactional email — connection
// reuse is not worth the concurrency complexity at this traffic level.
//
// Authentication: PLAIN auth is used when Username is non-empty.
// TLS: implicit (UseTLS=true) or opportunistic STARTTLS (default).
type SMTPSender struct {
	cfg SMTPConfig
}

// NewSMTPSender constructs an SMTPSender from the given SMTPConfig.
func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	return &SMTPSender{cfg: cfg}
}

// Send delivers the email described by msg via SMTP.
//
// The MIME structure is:
//
//	multipart/mixed  (when attachments are present)
//	  text/plain     (quoted-printable)
//	  text/html      (quoted-printable)
//	  application/X  (base64, one per attachment)
//
// Without attachments:
//
//	text/html (quoted-printable, simple single-part message)
func (s *SMTPSender) Send(_ context.Context, msg Message) error {
	from := s.cfg.From
	if msg.From != "" {
		parsed, err := mail.ParseAddress(msg.From)
		if err != nil || parsed.Address != msg.From {
			return fmt.Errorf("smtp: invalid From address")
		}
		from = msg.From
	}
	raw, err := buildMIMEMessage(from, msg)
	if err != nil {
		return fmt.Errorf("smtp: build MIME message: %w", err)
	}

	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	if s.cfg.UseTLS {
		return s.sendImplicitTLS(addr, auth, from, msg.To, raw)
	}
	return s.sendSTARTTLS(addr, auth, from, msg.To, raw)
}

func (s *SMTPSender) sendImplicitTLS(addr string, auth smtp.Auth, from, to string, raw []byte) error {
	tlsCfg := &tls.Config{
		ServerName: s.cfg.Host,
		MinVersion: tls.VersionTLS12,
	}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("smtp: dial TLS %s: %w", addr, err)
	}
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: new client (TLS): %w", err)
	}
	defer client.Close()
	return deliverMessage(client, auth, from, to, raw)
}

func (s *SMTPSender) sendSTARTTLS(addr string, auth smtp.Auth, from, to string, raw []byte) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp: dial %s: %w", addr, err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{
			ServerName: s.cfg.Host,
			MinVersion: tls.VersionTLS12,
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: STARTTLS: %w", err)
		}
	}

	return deliverMessage(client, auth, from, to, raw)
}

// deliverMessage performs the SMTP envelope commands and data transfer.
func deliverMessage(c *smtp.Client, auth smtp.Auth, from, to string, raw []byte) error {
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp: AUTH: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := wc.Write(raw); err != nil {
		return fmt.Errorf("smtp: write message body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp: close DATA writer: %w", err)
	}
	return c.Quit()
}

// compile-time assertion
var _ Sender = (*SMTPSender)(nil)

// ──────────────────────────────────────────────────────────────────────────────
// MIME builder
// ──────────────────────────────────────────────────────────────────────────────

// buildMIMEMessage constructs the raw RFC 5322 / MIME bytes for an email.
//
// With attachments → multipart/mixed containing text/plain + text/html + attachments.
// Without attachments → single text/html part (simpler, sufficient for tickets).
func buildMIMEMessage(from string, msg Message) ([]byte, error) {
	if msg.ReplyTo != "" {
		if _, err := mail.ParseAddress(msg.ReplyTo); err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: invalid Reply-To address: %w", err)
		}
	}

	var buf bytes.Buffer

	if len(msg.Attachments) == 0 {
		// Simple single-part HTML email (most common for ticket delivery without PDF).
		writeRFC5322HeaderWithReplyTo(&buf, from, msg.To, msg.Subject, "text/html; charset=utf-8", msg.ReplyTo)
		buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qpw := quotedprintable.NewWriter(&buf)
		if _, err := qpw.Write([]byte(msg.HTMLBody)); err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: write HTML body: %w", err)
		}
		_ = qpw.Close()
		return buf.Bytes(), nil
	}

	// Multipart/mixed for emails with attachments.
	mw := multipart.NewWriter(&buf)
	writeRFC5322HeaderWithReplyTo(&buf, from, msg.To, msg.Subject,
		"multipart/mixed; boundary=\""+mw.Boundary()+"\"", msg.ReplyTo)

	// text/plain part
	if msg.TextBody != "" {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"text/plain; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: create text/plain part: %w", err)
		}
		qpw := quotedprintable.NewWriter(pw)
		if _, err := qpw.Write([]byte(msg.TextBody)); err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: write text body: %w", err)
		}
		_ = qpw.Close()
	}

	// text/html part
	{
		hw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"text/html; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: create text/html part: %w", err)
		}
		qpw := quotedprintable.NewWriter(hw)
		if _, err := qpw.Write([]byte(msg.HTMLBody)); err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: write HTML body: %w", err)
		}
		_ = qpw.Close()
	}

	// Attachment parts
	for _, att := range msg.Attachments {
		ap, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type": {att.ContentType + "; name=\"" + att.Filename + "\""},
			"Content-Disposition": {
				"attachment; filename=\"" + att.Filename + "\"",
			},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: create attachment part %q: %w",
				att.Filename, err)
		}
		encoded := base64.StdEncoding.EncodeToString(att.Data)
		if _, err := ap.Write([]byte(encoded)); err != nil {
			return nil, fmt.Errorf("buildMIMEMessage: write attachment %q: %w",
				att.Filename, err)
		}
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("buildMIMEMessage: close multipart writer: %w", err)
	}
	return buf.Bytes(), nil
}

// writeRFC5322Header writes the standard email headers to buf.
// It does NOT write the blank line separator — that is the caller's
// responsibility (or handled by the multipart writer for mixed messages).
func writeRFC5322Header(buf *bytes.Buffer, from, to, subject, contentType string) {
	writeRFC5322HeaderWithReplyTo(buf, from, to, subject, contentType, "")
}

// writeRFC5322HeaderWithReplyTo writes the standard email headers plus an
// optional Reply-To. Keeping the compatibility wrapper above avoids widening
// the helper's call sites while ensuring every MIME shape emits the header.
func writeRFC5322HeaderWithReplyTo(buf *bytes.Buffer, from, to, subject, contentType, replyTo string) {
	buf.WriteString("From: ")
	buf.WriteString(from)
	buf.WriteString("\r\nTo: ")
	buf.WriteString(to)
	buf.WriteString("\r\nSubject: ")
	buf.WriteString(mime.QEncoding.Encode("utf-8", subject))
	if replyTo != "" {
		buf.WriteString("\r\nReply-To: ")
		buf.WriteString(replyTo)
	}
	buf.WriteString("\r\nMIME-Version: 1.0\r\nContent-Type: ")
	buf.WriteString(contentType)
	buf.WriteString("\r\n")
}

// truncateStr clips s to at most n runes, appending "…" when truncated.
func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

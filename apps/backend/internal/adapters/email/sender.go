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
	"net/smtp"
	"net/textproto"
)

// Message holds all fields required to send a single transactional email.
type Message struct {
	// To is the recipient email address (envelope and header).
	To string
	// Subject is the email subject line (UTF-8; will be Q-encoded in headers).
	Subject string
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

// LogSender writes email content to a slog.Logger instead of delivering it
// via SMTP. Use this in development, CI, and unit tests where a real SMTP
// server is unavailable.
//
// Each Send call emits a single structured log line with the recipient, subject,
// attachment count, and the first 200 characters of the text body.
type LogSender struct {
	// Logger receives the email log entries. Falls back to slog.Default() when nil.
	Logger *slog.Logger
}

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

// compile-time assertion
var _ Sender = (*LogSender)(nil)

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
	raw, err := buildMIMEMessage(s.cfg.From, msg)
	if err != nil {
		return fmt.Errorf("smtp: build MIME message: %w", err)
	}

	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	if s.cfg.UseTLS {
		return s.sendImplicitTLS(addr, auth, msg.To, raw)
	}
	return s.sendSTARTTLS(addr, auth, msg.To, raw)
}

func (s *SMTPSender) sendImplicitTLS(addr string, auth smtp.Auth, to string, raw []byte) error {
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
	return deliverMessage(client, auth, s.cfg.From, to, raw)
}

func (s *SMTPSender) sendSTARTTLS(addr string, auth smtp.Auth, to string, raw []byte) error {
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

	return deliverMessage(client, auth, s.cfg.From, to, raw)
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
	var buf bytes.Buffer

	if len(msg.Attachments) == 0 {
		// Simple single-part HTML email (most common for ticket delivery without PDF).
		writeRFC5322Header(&buf, from, msg.To, msg.Subject, "text/html; charset=utf-8")
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
	writeRFC5322Header(&buf, from, msg.To, msg.Subject,
		"multipart/mixed; boundary=\""+mw.Boundary()+"\"")

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
	buf.WriteString("From: ")
	buf.WriteString(from)
	buf.WriteString("\r\nTo: ")
	buf.WriteString(to)
	buf.WriteString("\r\nSubject: ")
	buf.WriteString(mime.QEncoding.Encode("utf-8", subject))
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

package email

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

func TestBuildMIMEMessageIncludesReplyToWhenPresent(t *testing.T) {
	raw, err := buildMIMEMessage("tickets@arena.example", Message{
		To:       "buyer@example.test",
		Subject:  "Your ticket",
		ReplyTo:  "organizer@example.test",
		HTMLBody: "<p>Ticket</p>",
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}
	if !strings.Contains(string(raw), "\r\nReply-To: organizer@example.test\r\n") {
		t.Fatalf("MIME message missing Reply-To header:\n%s", raw)
	}
}

func TestBuildMIMEMessageOmitsReplyToWhenEmpty(t *testing.T) {
	raw, err := buildMIMEMessage("tickets@arena.example", Message{
		To:       "buyer@example.test",
		Subject:  "Your ticket",
		HTMLBody: "<p>Ticket</p>",
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}
	if strings.Contains(string(raw), "Reply-To:") {
		t.Fatalf("MIME message unexpectedly contains Reply-To header:\n%s", raw)
	}
}

func TestSMTPSenderRejectsInvalidSenderOverride(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{From: "tickets@arena.example"})
	if err := s.Send(t.Context(), Message{From: "not an email", To: "buyer@example.test"}); err == nil {
		t.Fatal("expected invalid From error")
	}
}

// A ticket e-mail carries a PDF, so it takes the multipart branch. That branch
// used to omit the blank line between the header block and the first
// boundary, which a strict server (Brevo: "554 5.0.0 Header parsing error")
// rejects outright. Parse the built message with the standard library the
// same way a receiving server would.
func TestBuildMIMEMessage_MultipartWithCyrillicSubjectParses(t *testing.T) {
	subject := "Ваш билет: Процесс рождения слова на сцене в этюдах — Анна Макагон, 30 сентября 2026"
	raw, err := buildMIMEMessage("noreply@arena.example", Message{
		To:       "buyer@example.com",
		Subject:  subject,
		ReplyTo:  "organizer@example.com",
		HTMLBody: "<p>Здравствуйте! Příliš žluťoučký kůň.</p>",
		TextBody: "Здравствуйте!",
		Attachments: []Attachment{{
			Filename:    "ticket-01a0bbfd.pdf",
			ContentType: "application/pdf",
			Data:        []byte("%PDF-1.4 fake"),
		}},
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}

	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage: %v", err)
	}
	// Every header must be a real header: before the fix the boundary line was
	// swallowed into the header block.
	for k := range m.Header {
		if strings.HasPrefix(k, "--") {
			t.Fatalf("boundary parsed as a header: %q", k)
		}
	}
	got, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decode subject: %v", err)
	}
	if got != subject {
		t.Fatalf("subject round trip = %q, want %q", got, subject)
	}

	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("content type = %q, %v", mediaType, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types, altTypes []string
	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			t.Fatalf("NextPart: %v", perr)
		}
		ct := p.Header.Get("Content-Type")
		types = append(types, ct)
		if mt, ap, _ := mime.ParseMediaType(ct); mt == "multipart/alternative" {
			altTypes = partTypes(t, p, ap["boundary"])
		}
	}
	// The plain and HTML bodies are two versions of ONE letter: they must sit
	// together in multipart/alternative. As siblings in multipart/mixed a mail
	// client shows both, the raw text above the designed letter.
	if len(types) != 2 || !strings.HasPrefix(types[0], "multipart/alternative") {
		t.Fatalf("parts = %v, want multipart/alternative + pdf", types)
	}
	if !strings.HasPrefix(types[1], "application/pdf") {
		t.Fatalf("second part = %q, want the PDF", types[1])
	}
	if len(altTypes) != 2 || !strings.HasPrefix(altTypes[0], "text/plain") ||
		!strings.HasPrefix(altTypes[1], "text/html") {
		t.Fatalf("alternative parts = %v, want text/plain then text/html", altTypes)
	}
}

// Without a plain-text version the HTML body sits directly beside the
// attachments; there is nothing to offer as an alternative.
func TestBuildMIMEMessage_AttachmentWithoutTextBody(t *testing.T) {
	raw, err := buildMIMEMessage("noreply@arena.example", Message{
		To:       "buyer@example.com",
		Subject:  "Ticket",
		HTMLBody: "<p>x</p>",
		Attachments: []Attachment{{
			Filename: "t.pdf", ContentType: "application/pdf", Data: []byte("%PDF"),
		}},
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage: %v", err)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	types := partTypes(t, m.Body, params["boundary"])
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/html") ||
		!strings.HasPrefix(types[1], "application/pdf") {
		t.Fatalf("parts = %v, want text/html + pdf", types)
	}
}

func partTypes(t *testing.T, body io.Reader, boundary string) []string {
	t.Helper()
	mr := multipart.NewReader(body, boundary)
	var types []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return types
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
}

// No physical header line may exceed the RFC 5322 hard limit, and a long
// localized subject must be folded rather than emitted as one line.
func TestBuildMIMEMessage_LongSubjectIsFolded(t *testing.T) {
	subject := strings.Repeat("Мастер-класс в Учебном театре ", 8)
	raw, err := buildMIMEMessage("noreply@arena.example", Message{
		To: "buyer@example.com", Subject: subject, HTMLBody: "<p>x</p>",
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage() error = %v", err)
	}
	head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	for _, line := range bytes.Split(head, []byte("\r\n")) {
		if len(line) > 78 {
			t.Fatalf("header line is %d bytes (>78): %.60q…", len(line), line)
		}
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage: %v", err)
	}
	got, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || got != subject {
		t.Fatalf("subject round trip = %q (%v)", got, err)
	}
}

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
	var types []string
	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			t.Fatalf("NextPart: %v", perr)
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
	if len(types) != 3 {
		t.Fatalf("parts = %v, want text/plain + text/html + pdf", types)
	}
	if !strings.HasPrefix(types[2], "application/pdf") {
		t.Fatalf("third part = %q, want the PDF", types[2])
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

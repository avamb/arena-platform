package email

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
)

const trackedLetterID = "arena-letter:0192f3c4-5b6a-7c8d-9e0f-112233445566"

// trackedTicketLetter is a ticket letter as delivery builds it: HTML with a
// closing </body>, a text version and two PDF tickets.
func trackedTicketLetter() Message {
	return Message{
		To:      "buyer@example.com",
		Subject: "Ваши билеты / Your tickets",
		HTMLBody: "<!doctype html><html><head><meta charset=\"utf-8\"></head><body>" +
			"<p>Здравствуйте! Příliš žluťoučký kůň.</p></body></html>",
		TextBody: "Здравствуйте! Ваши билеты во вложении.",
		Attachments: []Attachment{
			{Filename: "ticket-1.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 one")},
			{Filename: "ticket-2.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 two")},
		},
		TrackingID: trackedLetterID,
	}
}

// decodedPart is one leaf MIME part with its body decoded.
type decodedPart struct {
	mediaType string
	body      string
}

// leafParts walks a message and returns every non-multipart part.
func leafParts(t *testing.T, header map[string][]string, body io.Reader) []decodedPart {
	t.Helper()
	ct := ""
	if v := header["Content-Type"]; len(v) > 0 {
		ct = v[0]
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("content type %q: %v", ct, err)
	}
	if !strings.HasPrefix(mt, "multipart/") {
		raw, _ := io.ReadAll(body)
		if enc := header["Content-Transfer-Encoding"]; len(enc) > 0 && strings.EqualFold(enc[0], "quoted-printable") {
			raw, _ = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		}
		return []decodedPart{{mediaType: mt, body: string(raw)}}
	}
	var out []decodedPart
	mr := multipart.NewReader(body, params["boundary"])
	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			return out
		}
		if perr != nil {
			t.Fatalf("NextPart: %v", perr)
		}
		out = append(out, leafParts(t, p.Header, p)...)
	}
}

// assertTrackedLetter checks the facts the Brevo delivery-status chain relies
// on, on the raw bytes the relay receives.
//
// Decision: the open-tracking pixel is NOT inserted by arena (no template or
// sender code writes an <img> for it). Brevo injects it into the HTML body of
// a relayed message. Arena therefore only guarantees what Brevo needs to be
// able to do that: one text/html alternative with a closing </body>, one
// letter, one X-Mailin-custom header.
func assertTrackedLetter(t *testing.T, raw []byte) {
	t.Helper()
	// The tracking header sits in the header block exactly once.
	headEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headEnd < 0 {
		t.Fatal("no header block terminator")
	}
	head := string(raw[:headEnd])
	if n := strings.Count(strings.ToLower(head), "x-mailin-custom:"); n != 1 {
		t.Fatalf("X-Mailin-custom appears %d times in the headers, want 1:\n%s", n, head)
	}
	if !strings.Contains(head, "\r\nX-Mailin-custom: "+trackedLetterID+"\r\n") {
		t.Fatalf("tracking header has the wrong value:\n%s", head)
	}
	// ...and nowhere in the body (a second copy would be a second letter).
	if n := strings.Count(strings.ToLower(string(raw)), "x-mailin-custom"); n != 1 {
		t.Fatalf("X-Mailin-custom appears %d times in the message, want 1", n)
	}

	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage: %v", err)
	}
	if got := m.Header["X-Mailin-Custom"]; len(got) != 1 || got[0] != trackedLetterID {
		t.Fatalf("parsed X-Mailin-custom = %v", got)
	}
	parts := leafParts(t, m.Header, m.Body)

	var html, text, pdf []decodedPart
	for _, p := range parts {
		switch p.mediaType {
		case "text/html":
			html = append(html, p)
		case "text/plain":
			text = append(text, p)
		case "application/pdf":
			pdf = append(pdf, p)
		default:
			t.Fatalf("unexpected part type %q", p.mediaType)
		}
	}
	if len(html) != 1 {
		t.Fatalf("%d text/html parts, want exactly 1 (a second HTML body is a double letter)", len(html))
	}
	if len(text) != 1 || !strings.Contains(text[0].body, "Ваши билеты") {
		t.Fatalf("text/plain alternative missing or wrong: %+v", text)
	}
	if len(pdf) != 2 {
		t.Fatalf("%d PDF attachments, want 2", len(pdf))
	}
	// Brevo injects its pixel before the last </body>; without it the HTML
	// part has nowhere to take one.
	if n := strings.Count(strings.ToLower(html[0].body), "</body>"); n != 1 {
		t.Fatalf("HTML part has %d </body> tags, want 1:\n%s", n, html[0].body)
	}
	if n := strings.Count(strings.ToLower(html[0].body), "<html"); n != 1 {
		t.Fatalf("HTML part has %d <html> roots, want 1", n)
	}
	if !strings.Contains(html[0].body, "Příliš žluťoučký kůň") {
		t.Fatalf("HTML body did not survive quoted-printable:\n%s", html[0].body)
	}
}

func TestBuildMIMEMessage_TicketLetterIsOneTrackedLetter(t *testing.T) {
	raw, err := buildMIMEMessage("tickets@example.com", trackedTicketLetter())
	if err != nil {
		t.Fatal(err)
	}
	assertTrackedLetter(t, raw)
}

// recordingSMTP is a one-connection SMTP server that counts the commands that
// make a delivery and keeps every DATA body it received.
type recordingSMTP struct {
	mu    sync.Mutex
	data  []string
	mails int
	rcpts int
}

func (r *recordingSMTP) snapshot() (data []string, mails, rcpts int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.data...), r.mails, r.rcpts
}

func startRecordingSMTP(t *testing.T) (rec *recordingSMTP, host, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	rec = &recordingSMTP{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		rd := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var body strings.Builder
		inData := false
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					rec.mu.Lock()
					rec.data = append(rec.data, body.String())
					rec.mu.Unlock()
					w("250 2.0.0 OK: queued as <202610141200.1@smtp-relay.mailin.fr>")
					continue
				}
				body.WriteString(line)
				continue
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "MAIL"):
				rec.mu.Lock()
				rec.mails++
				rec.mu.Unlock()
				w("250 OK")
			case strings.HasPrefix(cmd, "RCPT"):
				rec.mu.Lock()
				rec.rcpts++
				rec.mu.Unlock()
				w("250 OK")
			case cmd == "DATA":
				inData = true
				w("354 go ahead")
			case cmd == "QUIT":
				w("221 bye")
				return
			default:
				w("250 OK")
			}
		}
	}()
	host, port, _ = net.SplitHostPort(ln.Addr().String())
	return rec, host, port
}

// One sendRecorded call is one SendReceipt call: one MAIL, one RCPT, one DATA
// transfer, and the message on the wire is the tracked letter.
func TestSMTPSender_TicketLetterIsOneDataTransfer(t *testing.T) {
	rec, host, port := startRecordingSMTP(t)
	s := NewSMTPSender(SMTPConfig{Host: host, Port: port, From: "tickets@example.com"})

	rc, err := SendWithReceipt(context.Background(), s, trackedTicketLetter())
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if rc.MessageID != "202610141200.1@smtp-relay.mailin.fr" {
		t.Fatalf("message id = %q", rc.MessageID)
	}
	data, mails, rcpts := rec.snapshot()
	if len(data) != 1 || mails != 1 || rcpts != 1 {
		t.Fatalf("DATA transfers = %d, MAIL = %d, RCPT = %d, want 1 each", len(data), mails, rcpts)
	}
	// The fake server returns the lines it read; the body is the same bytes
	// buildMIMEMessage produced, with CRLF line ends.
	assertTrackedLetter(t, []byte(data[0]))
}

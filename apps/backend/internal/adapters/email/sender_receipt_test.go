package email

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

// fakeSMTP is a one-connection SMTP server that answers DATA with reply and
// records the message it received.
func fakeSMTP(t *testing.T, reply string) (addr string, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var body strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					out <- body.String()
					w(reply)
					continue
				}
				body.WriteString(line)
				continue
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
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
	return ln.Addr().String(), out
}

func TestSMTPSender_ReceiptCarriesBrevoQueuedID(t *testing.T) {
	addr, got := fakeSMTP(t, "250 2.0.0 OK: queued as <202610131055.12345678901@smtp-relay.mailin.fr>")
	host, port, _ := net.SplitHostPort(addr)
	s := NewSMTPSender(SMTPConfig{Host: host, Port: port, From: "tickets@example.com"})

	rc, err := s.SendReceipt(context.Background(), Message{
		To: "buyer@example.com", Subject: "Tickets", HTMLBody: "<p>hi</p>",
		TrackingID: "arena-letter:0192f3c4-5b6a-7c8d-9e0f-112233445566",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if rc.MessageID != "202610131055.12345678901@smtp-relay.mailin.fr" {
		t.Fatalf("message id = %q", rc.MessageID)
	}
	raw := <-got
	if !strings.Contains(raw, "X-Mailin-custom: arena-letter:0192f3c4-5b6a-7c8d-9e0f-112233445566\r\n") {
		t.Fatalf("tracking header missing:\n%s", raw)
	}
}

func TestSMTPSender_ReplyWithoutIDIsStillSuccess(t *testing.T) {
	addr, _ := fakeSMTP(t, "250 OK")
	host, port, _ := net.SplitHostPort(addr)
	s := NewSMTPSender(SMTPConfig{Host: host, Port: port, From: "tickets@example.com"})
	rc, err := SendWithReceipt(context.Background(), s, Message{To: "b@example.com", Subject: "x", HTMLBody: "x"})
	if err != nil || rc.MessageID != "" {
		t.Fatalf("receipt = %+v, err = %v", rc, err)
	}
}

func TestSMTPSender_RefusedDataIsAnError(t *testing.T) {
	addr, _ := fakeSMTP(t, "554 5.0.0 Header parsing error")
	host, port, _ := net.SplitHostPort(addr)
	s := NewSMTPSender(SMTPConfig{Host: host, Port: port, From: "tickets@example.com"})
	if err := s.Send(context.Background(), Message{To: "b@example.com", Subject: "x", HTMLBody: "x"}); err == nil {
		t.Fatal("a 554 reply to DATA must fail the send")
	}
}

func TestQueuedMessageID(t *testing.T) {
	cases := map[string]string{
		"2.0.0 OK: queued as <a.1@smtp-relay.mailin.fr>": "a.1@smtp-relay.mailin.fr",
		"2.0.0 Ok: queued as 4ABCD12345":                 "4ABCD12345",
		"OK":                                             "",
		"queued as <>":                                   "",
	}
	for in, want := range cases {
		if got := QueuedMessageID(in); got != want {
			t.Errorf("QueuedMessageID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildMIMEMessage_TrackingIDValidated(t *testing.T) {
	raw, err := buildMIMEMessage("a@example.com", Message{To: "b@example.com", Subject: "x", HTMLBody: "x",
		TrackingID: "evil\r\nBcc: c@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "X-Mailin-custom") || strings.Contains(string(raw), "Bcc:") {
		t.Fatalf("an invalid tracking id must not reach the headers:\n%s", raw)
	}
}

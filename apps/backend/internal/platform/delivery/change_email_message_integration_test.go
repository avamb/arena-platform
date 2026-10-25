//go:build integration

// change_email_message_integration_test.go — RT-11 (spec 44): the organizer's
// message of the change and cancel letters is stored as ART next to its format
// (session_changes.message_format) and the letters print it through
// richtext.Render with the session_message profile, formatted in the HTML part
// and as plain words in the text part. A journal row written before migration
// 0169 (format 'text') still prints as before.
package delivery

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
)

// emailPartBody returns the decoded body of the first part whose Content-Type starts with prefix.
func emailPartBody(t *testing.T, raw, prefix string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse captured message: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type: %v", err)
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		// A letter without a text version is ONE quoted-printable part.
		if !strings.HasPrefix(mediaType, prefix) {
			return ""
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
		if err != nil {
			t.Fatalf("decode %s body: %v", prefix, err)
		}
		return string(decoded)
	}
	var walk func(body io.Reader, boundary string) (string, bool)
	walk = func(body io.Reader, boundary string) (string, bool) {
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				return "", false
			}
			ct := part.Header.Get("Content-Type")
			if mt, p, _ := mime.ParseMediaType(ct); strings.HasPrefix(mt, "multipart/") {
				if s, ok := walk(part, p["boundary"]); ok {
					return s, true
				}
				continue
			}
			if !strings.HasPrefix(ct, prefix) {
				continue
			}
			decoded, err := io.ReadAll(quotedprintable.NewReader(part))
			if err != nil {
				t.Fatalf("decode %s part: %v", prefix, err)
			}
			return string(decoded), true
		}
	}
	if s, ok := walk(msg.Body, params["boundary"]); ok {
		return s
	}
	return ""
}

const artMessage = `<p>Doors open at <strong>19:30</strong>.</p><ul><li>Bring ID</li></ul><p>Info: <a href="https://example.com/info">https://example.com/info</a></p><script>alert(1)</script>`

func assertArtMessageLetter(t *testing.T, label, html, text string) {
	t.Helper()
	for _, want := range []string{"<strong>19:30</strong>", "<li>Bring ID</li>", `href="https://example.com/info"`, `rel="noopener noreferrer"`} {
		if !strings.Contains(html, want) {
			t.Errorf("%s: html part lacks %q", label, want)
		}
	}
	for _, bad := range []string{"&lt;p&gt;", "&lt;strong&gt;", "&lt;li&gt;", "<script", "alert(1)", "white-space:pre-line"} {
		if strings.Contains(html, bad) {
			t.Errorf("%s: html part carries %q", label, bad)
		}
	}
	if text == "" {
		return // a letter without a text version
	}
	for _, bad := range []string{"<p>", "<strong>", "<li>", "<a "} {
		if strings.Contains(text, bad) {
			t.Errorf("%s: text part carries the tag %q:\n%s", label, bad, text)
		}
	}
	if !strings.Contains(text, "Doors open at 19:30.") || !strings.Contains(text, "Bring ID") || !strings.Contains(text, "https://example.com/info") {
		t.Errorf("%s: text part lacks the message words:\n%s", label, text)
	}
}

func TestSessionChange_ARTMessageIsStoredAsARTAndPrintedFormatted(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()

	// A move with an ART message.
	res, err := f.saveFormat(ctx, t, scMoveWeek, nil, artMessage, "art")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var stored, format string
	if err := f.pool.QueryRow(ctx, `SELECT message, message_format FROM session_changes WHERE id = $1`, res.ChangeID).Scan(&stored, &format); err != nil {
		t.Fatal(err)
	}
	if format != "art" || strings.Contains(stored, "<script") || !strings.Contains(stored, "<strong>19:30</strong>") {
		t.Fatalf("journal row: format=%q message=%q", format, stored)
	}
	raw, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("move letter: err=%v sent=%v", err, raw != "")
	}
	assertArtMessageLetter(t, "change", emailPartBody(t, raw, "text/html"), emailPartBody(t, raw, "text/plain"))

	// A cancellation with an ART message.
	res, err = f.saveFormat(ctx, t, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, nil, artMessage, "art")
	if err != nil {
		t.Fatalf("Apply cancel: %v", err)
	}
	raw, err = f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("cancel letter: err=%v sent=%v", err, raw != "")
	}
	assertArtMessageLetter(t, "cancel", emailPartBody(t, raw, "text/html"), emailPartBody(t, raw, "text/plain"))
}

func TestSessionChange_PlainMessageKeepsWorkingAndOldRowsPrintAsBefore(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()

	// No format (the Telegram bot): the plain text is stored as typed with the format text, as always,
	// and the letter prints it through the same converter as every other text, with its line break.
	res, err := f.save(ctx, t, scMoveWeek, nil, "Doors open at 19:30.\nPlease bring <ID>.")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var stored, format string
	if err := f.pool.QueryRow(ctx, `SELECT message, message_format FROM session_changes WHERE id = $1`, res.ChangeID).Scan(&stored, &format); err != nil {
		t.Fatal(err)
	}
	if format != "text" || stored != "Doors open at 19:30.\nPlease bring <ID>." {
		t.Fatalf("journal row: format=%q message=%q", format, stored)
	}
	raw, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("letter: err=%v sent=%v", err, raw != "")
	}
	html := emailPartBody(t, raw, "text/html")
	if !strings.Contains(html, "Doors open at 19:30.<br>Please bring &lt;ID&gt;.") {
		t.Errorf("html part lacks the plain message:\n%s", html)
	}
	if text := emailPartBody(t, raw, "text/plain"); !strings.Contains(text, "Please bring <ID>.") {
		t.Errorf("text part lacks the plain message:\n%s", text)
	}

	// A row of the old shape (the default format 'text') still prints.
	f.mustExec(ctx, t, `UPDATE session_changes SET message = $2, message_format = 'text' WHERE id = $1`, res.ChangeID, "Old row, first line\nsecond line")
	f.mustExec(ctx, t, `UPDATE session_change_notices SET state = 'queued' WHERE change_id = $1`, res.ChangeID)
	raw, err = f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("old row letter: err=%v sent=%v", err, raw != "")
	}
	if html := emailPartBody(t, raw, "text/html"); !strings.Contains(html, "Old row, first line<br>second line") {
		t.Errorf("an old plain row is not printed with its line break:\n%s", html)
	}
}

func TestSessionChange_RefusedMessageWritesNothing(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()

	for name, c := range map[string]struct{ message, format string }{
		"bidi override":   {"<p>a\u202eb</p>", "art"},
		"unknown format":  {"x", "markdown"},
		"too many links":  {`<p><a href="https://a.example/1">1</a> <a href="https://a.example/2">2</a> <a href="https://a.example/3">3</a> <a href="https://a.example/4">4</a></p>`, "art"},
		"too long (art)":  {"<p>" + strings.Repeat("x", 1001) + "</p>", "art"},
		"too long (text)": {strings.Repeat("x", 1001), "text"},
	} {
		if _, err := f.saveFormat(ctx, t, scMoveWeek, nil, c.message, c.format); err == nil {
			t.Errorf("%s: the message was accepted", name)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_changes WHERE session_id = $1`, f.SessionID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("journal rows after refused messages = %d (%v)", n, err)
	}
}

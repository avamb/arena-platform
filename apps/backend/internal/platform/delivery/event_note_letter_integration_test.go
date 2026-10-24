//go:build integration

// event_note_letter_integration_test.go — task 7-1 (EC-31) on a live database:
// the organizer's own event text is printed in the ticket letter and in the
// session-change letter (not in the cancellation), the stored template_version
// of such a letter ends in "+note", and a letter without a note is what it
// was before.
//
// Same fixtures and DATABASE_URL pattern as letter_status_integration_test.go
// and change_email_integration_test.go: point it at a FRESH scratch database.
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

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// crlf: the templates are CRLF files, so a rendered text part has CRLF line ends.
const crlf = "\r\n"

const letterNote = "Bring a warm jacket.\n\nProgramme: https://example.com/programme"

func TestEventNote_TicketLetterPrintsItAndVersionSaysSo(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_email_notes WHERE event_id = $1`, seed.EventID)
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id FROM events WHERE id = $1`, seed.EventID).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	queries := gen.New(pool)
	send := func(ticket uuid.UUID) (*receiptSender, string) {
		t.Helper()
		sender := &receiptSender{messageID: "2026101610." + uuid.NewString()[:8] + "@smtp-relay.mailin.fr"}
		h := NewHandler(HandlerOptions{
			TicketQueries: queries, DeliveryJobQueries: queries, CredentialQueries: queries,
			Sender: sender, Logger: testLogger(),
		})
		if err := h(ctx, mustJSON(t, Payload{TicketID: ticket.String(), Locale: "ru"})); err != nil {
			t.Fatalf("worker job: %v", err)
		}
		if len(sender.got) != 1 {
			t.Fatalf("messages sent = %d", len(sender.got))
		}
		var version string
		if err := pool.QueryRow(ctx, `SELECT template_version FROM email_letters WHERE order_id = $1 ORDER BY sent_at DESC LIMIT 1`,
			seed.OrderID).Scan(&version); err != nil {
			t.Fatalf("letter row: %v", err)
		}
		return sender, version
	}

	// Without a note: the letter is what it was, the version is the file's.
	plain, plainVersion := send(seed.TicketIDs[0])
	if strings.Contains(plain.got[0].TextBody, "О мероприятии") || strings.HasSuffix(plainVersion, templates.NoteVersionSuffix) {
		t.Fatalf("a letter without a note carries the block or the suffix (%q)", plainVersion)
	}

	// With a note, the next order letter: the three tickets are all sent
	// already, so make them pending again for the second send.
	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'pending', attempts = 0, sent_at = NULL, processing_at = NULL WHERE ticket_id = ANY($1)`,
		seed.TicketIDs); err != nil {
		t.Fatal(err)
	}
	if err := queries.UpsertEventEmailNote(ctx, seed.EventID, orgID, letterNote, "text"); err != nil {
		t.Fatal(err)
	}
	with, withVersion := send(seed.TicketIDs[0])
	text := strings.ReplaceAll(with.got[0].TextBody, crlf, "\n")
	if !strings.Contains(text, "О мероприятии:\nBring a warm jacket.") || !strings.Contains(text, "https://example.com/programme") {
		t.Fatalf("the note is missing from the text part:\n%s", text)
	}
	if h := with.got[0].HTMLBody; !strings.Contains(h, `href="https://example.com/programme"`) || !strings.Contains(h, `rel="noopener noreferrer"`) {
		t.Error("the link is not an anchor with rel=noopener noreferrer")
	}
	if !strings.HasPrefix(withVersion, "ticket.ru@") || !strings.HasSuffix(withVersion, templates.NoteVersionSuffix) {
		t.Fatalf("template_version = %q, want ticket.ru@<hash>+note", withVersion)
	}
	if strings.TrimSuffix(withVersion, templates.NoteVersionSuffix) != plainVersion {
		t.Errorf("the file part of the version changed: %q vs %q", withVersion, plainVersion)
	}
}

func TestEventNote_ChangeLetterPrintsItCancelDoesNot(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()
	defer func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM event_email_notes WHERE event_id = $1`, f.EventID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, f.OrderID)
	}()
	if err := gen.New(f.pool).UpsertEventEmailNote(ctx, f.EventID, f.orgID, letterNote, "text"); err != nil {
		t.Fatal(err)
	}

	// A move: the letter carries the note and the version says so.
	res, err := f.save(ctx, t, scMoveWeek, nil, "Moved.")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("job: %v (sent %v)", err, raw != "")
	}
	body := strings.ReplaceAll(emailTextBody(t, raw), crlf, "\n")
	if !strings.Contains(body, "About the event:\nBring a warm jacket.") {
		t.Fatalf("the change letter lacks the note:\n%s", body)
	}
	var version string
	if err := f.pool.QueryRow(ctx, `SELECT template_version FROM email_letters WHERE order_id = $1 AND kind = 'change'`, f.OrderID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(version, "change.en@") || !strings.HasSuffix(version, templates.NoteVersionSuffix) {
		t.Fatalf("change template_version = %q", version)
	}

	// A cancellation: the event is not happening, no note, no suffix.
	cancel, err := f.save(ctx, t, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, nil, "Cancelled.")
	if err != nil {
		t.Fatalf("Apply cancel: %v", err)
	}
	if len(cancel.Kinds) == 0 || cancel.Kinds[0] != sessionchange.KindCancelled {
		t.Fatalf("not a cancellation: %+v", cancel)
	}
	raw, err = f.runJob(ctx, t, cancel.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("cancel job: %v (sent %v)", err, raw != "")
	}
	if cancelBody := emailTextBody2(t, raw); strings.Contains(cancelBody, "Bring a warm jacket.") {
		t.Fatal("the cancellation letter carries the event note")
	}
	if err := f.pool.QueryRow(ctx, `SELECT template_version FROM email_letters WHERE order_id = $1 AND kind = 'cancel'`, f.OrderID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(version, templates.NoteVersionSuffix) {
		t.Fatalf("cancel template_version = %q carries the suffix", version)
	}
}

// artNote is a note as RT-02 stores it: ART with note_format 'art'.
const artNote = `<p>Bring a <strong>warm</strong> jacket.</p><ul><li>Cloakroom is free</li></ul><p>Map: <a href="https://example.com/programme">the programme</a></p>`

// assertARTLetter checks the HTML and text parts of a letter that carries artNote.
func assertARTLetter(t *testing.T, html, text string) {
	t.Helper()
	for _, want := range []string{"<strong>warm</strong>", "<li>Cloakroom is free</li>", `href="https://example.com/programme"`, `rel="noopener noreferrer"`} {
		if !strings.Contains(html, want) {
			t.Errorf("html part lacks %q", want)
		}
	}
	for _, bad := range []string{"&lt;p&gt;", "&lt;strong&gt;", "&lt;a ", "&lt;li&gt;"} {
		if strings.Contains(html, bad) {
			t.Errorf("html part carries the escaped markup %q", bad)
		}
	}
	for _, bad := range []string{"<p>", "<strong>", "<a ", "<li>", "<ul>"} {
		if strings.Contains(text, bad) {
			t.Errorf("text part carries the tag %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "Bring a warm jacket.") || !strings.Contains(text, "Cloakroom is free") || !strings.Contains(text, "https://example.com/programme") {
		t.Errorf("text part lacks the note words:\n%s", text)
	}
}

// emailHTMLBody returns the decoded text/html part of a captured message.
func emailHTMLBody(t *testing.T, raw string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse captured message: %v", err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type: %v", err)
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
			if mt, p, _ := mime.ParseMediaType(ct); mt == "multipart/alternative" {
				if s, ok := walk(part, p["boundary"]); ok {
					return s, true
				}
				continue
			}
			if !strings.HasPrefix(ct, "text/html") {
				continue
			}
			decoded, err := io.ReadAll(quotedprintable.NewReader(part))
			if err != nil {
				t.Fatalf("decode text/html part: %v", err)
			}
			return string(decoded), true
		}
	}
	if s, ok := walk(msg.Body, params["boundary"]); ok {
		return s
	}
	t.Fatal("captured message has no text/html part")
	return ""
}

func TestEventNote_TicketLetterPrintsAnARTNoteFormatted(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_email_notes WHERE event_id = $1`, seed.EventID)
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id FROM events WHERE id = $1`, seed.EventID).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	queries := gen.New(pool)
	if err := queries.UpsertEventEmailNote(ctx, seed.EventID, orgID, artNote, "art"); err != nil {
		t.Fatal(err)
	}
	sender := &receiptSender{messageID: "2026101611." + uuid.NewString()[:8] + "@smtp-relay.mailin.fr"}
	h := NewHandler(HandlerOptions{
		TicketQueries: queries, DeliveryJobQueries: queries, CredentialQueries: queries,
		Sender: sender, Logger: testLogger(),
	})
	if err := h(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[0].String(), Locale: "en"})); err != nil {
		t.Fatalf("worker job: %v", err)
	}
	if len(sender.got) != 1 {
		t.Fatalf("messages sent = %d", len(sender.got))
	}
	assertARTLetter(t, sender.got[0].HTMLBody, strings.ReplaceAll(sender.got[0].TextBody, crlf, "\n"))
}

// A job queued before the release has no note_format in its payload and the
// note it reads is a legacy text row: it must still read as text.
func TestEventNote_JobWithoutFormatReadsTheNoteAsText(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_email_notes WHERE event_id = $1`, seed.EventID)
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id FROM events WHERE id = $1`, seed.EventID).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	queries := gen.New(pool)
	if err := queries.UpsertEventEmailNote(ctx, seed.EventID, orgID, "Plain **note** <b>x</b>\n\nhttps://example.com/old", "text"); err != nil {
		t.Fatal(err)
	}
	sender := &receiptSender{messageID: "2026101612." + uuid.NewString()[:8] + "@smtp-relay.mailin.fr"}
	h := NewHandler(HandlerOptions{
		TicketQueries: queries, DeliveryJobQueries: queries, CredentialQueries: queries,
		Sender: sender, Logger: testLogger(),
	})
	if err := h(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[0].String(), Locale: "en"})); err != nil {
		t.Fatalf("worker job: %v", err)
	}
	html := sender.got[0].HTMLBody
	if !strings.Contains(html, "<strong>note</strong>") || !strings.Contains(html, "&lt;b&gt;x&lt;/b&gt;") || !strings.Contains(html, `href="https://example.com/old"`) {
		t.Errorf("legacy note in the html part:\n%s", html)
	}
}

func TestEventNote_ChangeLetterPrintsAnARTNoteFormatted(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()
	defer func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM event_email_notes WHERE event_id = $1`, f.EventID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, f.OrderID)
	}()
	if err := gen.New(f.pool).UpsertEventEmailNote(ctx, f.EventID, f.orgID, artNote, "art"); err != nil {
		t.Fatal(err)
	}
	res, err := f.save(ctx, t, scMoveWeek, nil, "Moved.")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("job: %v (sent %v)", err, raw != "")
	}
	assertARTLetter(t, emailHTMLBody(t, raw), strings.ReplaceAll(emailTextBody(t, raw), crlf, "\n"))
}

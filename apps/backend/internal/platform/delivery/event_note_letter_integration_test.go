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
	if err := queries.UpsertEventEmailNote(ctx, seed.EventID, orgID, letterNote); err != nil {
		t.Fatal(err)
	}
	with, withVersion := send(seed.TicketIDs[0])
	text := strings.ReplaceAll(with.got[0].TextBody, crlf, "\n")
	if !strings.Contains(text, "О мероприятии:\nBring a warm jacket.") || !strings.Contains(text, "https://example.com/programme") {
		t.Fatalf("the note is missing from the text part:\n%s", text)
	}
	if !strings.Contains(with.got[0].HTMLBody, `<a href="https://example.com/programme" rel="noopener"`) {
		t.Error("the link is not an anchor with rel=noopener")
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
	if err := gen.New(f.pool).UpsertEventEmailNote(ctx, f.EventID, f.orgID, letterNote); err != nil {
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

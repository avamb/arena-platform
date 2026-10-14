//go:build integration

// letter_status_integration_test.go — live-DB proof that ticket.deliver
// records its letter: one email_letters row per order letter, carrying the
// relay's message id, linked from every delivery_jobs row of the letter, and
// its id in the X-Mailin-custom header; a refused send leaves no row.
//
// Same fixture and DATABASE_URL pattern as order_letter_integration_test.go:
// point it at a FRESH scratch database, never at the shared dev stand.
package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

// receiptSender accepts every message and answers with a fixed message id,
// as the Brevo relay does in its final reply to DATA.
type receiptSender struct {
	messageID string
	fail      bool
	got       []email.Message
}

func (s *receiptSender) Send(ctx context.Context, m email.Message) error {
	_, err := s.SendReceipt(ctx, m)
	return err
}

// DefaultFrom mirrors email.SMTPSender: the platform sender of a message
// without its own From.
func (s *receiptSender) DefaultFrom() string { return "tickets@arena.test" }

func (s *receiptSender) SendReceipt(_ context.Context, m email.Message) (email.Receipt, error) {
	s.got = append(s.got, m)
	if s.fail {
		return email.Receipt{}, errors.New("smtp: 554 rejected")
	}
	return email.Receipt{MessageID: s.messageID}, nil
}

func TestLetterStatus_TicketLetterIsRecordedAndLinked(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()

	mid := "2026101310." + uuid.NewString()[:8] + "@smtp-relay.mailin.fr"
	sender := &receiptSender{messageID: mid}
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries: queries, DeliveryJobQueries: queries, CredentialQueries: queries,
		Sender: sender, Logger: testLogger(),
	})
	if err := handler(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[0].String(), Locale: "en"})); err != nil {
		t.Fatalf("worker job: %v", err)
	}
	if len(sender.got) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(sender.got))
	}

	var (
		letterID          uuid.UUID
		orgID             uuid.UUID
		kind, status, rcp string
		storedMID         *string
	)
	if err := pool.QueryRow(ctx, `SELECT id, org_id, kind, status, recipient, provider_message_id
		FROM email_letters WHERE order_id = $1`, seed.OrderID).
		Scan(&letterID, &orgID, &kind, &status, &rcp, &storedMID); err != nil {
		t.Fatalf("letter row: %v", err)
	}
	if kind != emailstatus.KindTicket || status != emailstatus.StatusSent || rcp != seed.RecipientEmail {
		t.Fatalf("letter = kind %q status %q recipient %q", kind, status, rcp)
	}
	if storedMID == nil || *storedMID != mid {
		t.Fatalf("provider_message_id = %v, want %q", storedMID, mid)
	}
	if sender.got[0].TrackingID != emailstatus.TrackingID(letterID) {
		t.Fatalf("tracking id = %q, want %q", sender.got[0].TrackingID, emailstatus.TrackingID(letterID))
	}
	// The letter's passport: what proves later which letter went out.
	var (
		subject, bodyHash, tplVersion *string
		numbers                       []string
		attachments                   []map[string]any
	)
	if err := pool.QueryRow(ctx, `SELECT subject, body_sha256, template_version, ticket_numbers, attachments
		FROM email_letters WHERE id = $1`, letterID).
		Scan(&subject, &bodyHash, &tplVersion, &numbers, &attachments); err != nil {
		t.Fatal(err)
	}
	if subject == nil || *subject != sender.got[0].Subject {
		t.Fatalf("subject = %v, want %q", subject, sender.got[0].Subject)
	}
	if bodyHash == nil || *bodyHash != emailstatus.BodyDigest(sender.got[0].HTMLBody, sender.got[0].TextBody) {
		t.Fatalf("body_sha256 = %v", bodyHash)
	}
	var fromAddr, replyTo *string
	if err := pool.QueryRow(ctx, `SELECT from_address, reply_to FROM email_letters WHERE id = $1`, letterID).
		Scan(&fromAddr, &replyTo); err != nil {
		t.Fatal(err)
	}
	if want := sender.got[0].ReplyTo; (want == "") != (replyTo == nil) || (replyTo != nil && *replyTo != want) {
		t.Fatalf("reply_to = %v, want %q", replyTo, want)
	}
	if want := sender.got[0].From; want == "" && (fromAddr == nil || *fromAddr != "tickets@arena.test") ||
		want != "" && (fromAddr == nil || *fromAddr != want) {
		t.Fatalf("from_address = %v (message From %q)", fromAddr, want)
	}
	if tplVersion == nil || !strings.HasPrefix(*tplVersion, "ticket.en@") {
		t.Fatalf("template_version = %v", tplVersion)
	}
	if len(numbers) != 3 {
		t.Fatalf("ticket_numbers = %v, want the three tickets", numbers)
	}
	if len(attachments) != len(sender.got[0].Attachments) || len(attachments) != 3 {
		t.Fatalf("attachments = %v", attachments)
	}
	for i, a := range sender.got[0].Attachments {
		if attachments[i]["sha256"] != emailstatus.FileDigest(a.Data) || attachments[i]["filename"] != a.Filename {
			t.Fatalf("attachment %d = %v", i, attachments[i])
		}
	}

	var linked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM delivery_jobs
		WHERE id = ANY($1) AND email_letter_id = $2 AND provider_message_id = $3`,
		seed.JobIDs, letterID, mid).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != len(seed.JobIDs) {
		t.Fatalf("delivery jobs linked to the letter = %d, want %d", linked, len(seed.JobIDs))
	}

	// The order view shows one sent ticket letter with all three tickets.
	letters, err := emailstatus.OrderLetters(ctx, pool, seed.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(letters) != 1 || letters[0].Status != emailstatus.StatusSent || len(letters[0].TicketIDs) != 3 {
		t.Fatalf("order letters = %+v", letters)
	}
}

func TestLetterStatus_RefusedSendLeavesNoLetter(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()

	sender := &receiptSender{fail: true}
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries: queries, DeliveryJobQueries: queries, CredentialQueries: queries,
		Sender: sender, Logger: testLogger(),
	})
	if err := handler(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[0].String(), Locale: "en"})); err == nil {
		t.Fatal("a refused send must return an error")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_letters WHERE order_id = $1`, seed.OrderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("letters after a refused send = %d, want 0", n)
	}
	letters, err := emailstatus.OrderLetters(ctx, pool, seed.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(letters) != 1 || letters[0].Status != emailstatus.StatusQueued || len(letters[0].TicketIDs) != 3 {
		t.Fatalf("the released jobs must show as one queued letter: %+v", letters)
	}
}

// seedOrgID reads the organization of the seeded order.
func seedOrgID(ctx context.Context, t *testing.T, pool *pgxpool.Pool, orderID uuid.UUID) uuid.UUID {
	t.Helper()
	var org uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id FROM orders WHERE id = $1`, orderID).Scan(&org); err != nil {
		t.Fatal(err)
	}
	return org
}

// TestLetterStatus_DuplicateMessageIDStillMarksTheLetterSent (review M1): a
// relay that repeats a message id must not leave the second letter
// "sending" with its jobs unlinked. The id stays on the first letter only.
func TestLetterStatus_DuplicateMessageIDStillMarksTheLetterSent(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	org := seedOrgID(ctx, t, pool, seed.OrderID)
	mid := "repeat." + uuid.NewString()[:8] + "@relay.test"

	first, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindTicket, Recipient: seed.RecipientEmail, DeliveryJobIDs: seed.JobIDs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if err := emailstatus.Accepted(ctx, pool, first, mid, seed.JobIDs[:1]); err != nil {
		t.Fatalf("first letter: %v", err)
	}
	second, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindTicket, Recipient: seed.RecipientEmail, DeliveryJobIDs: seed.JobIDs[1:]})
	if err != nil {
		t.Fatal(err)
	}
	if err := emailstatus.Accepted(ctx, pool, second, mid, seed.JobIDs[1:]); err != nil {
		t.Fatalf("a repeated message id must not fail Accepted: %v", err)
	}

	var status string
	var storedMID *string
	if err := pool.QueryRow(ctx, `SELECT status, provider_message_id FROM email_letters WHERE id = $1`, second).
		Scan(&status, &storedMID); err != nil {
		t.Fatal(err)
	}
	if status != emailstatus.StatusSent || storedMID != nil {
		t.Fatalf("second letter = status %q, message id %v; want sent without an id", status, storedMID)
	}
	var linked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM delivery_jobs WHERE id = ANY($1) AND email_letter_id = $2`,
		seed.JobIDs[1:], second).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 2 {
		t.Fatalf("jobs linked to the second letter = %d, want 2", linked)
	}
}

// TestLetterStatus_StaleSweepKeepsALetterThatWentOut (review M2): a letter
// left "sending" for a day is evidence when its delivery job says the
// letter went out — the sweep makes it "sent" (receipt_missing) instead of
// deleting it. A letter that never went out is deleted.
func TestLetterStatus_StaleSweepKeepsALetterThatWentOut(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	org := seedOrgID(ctx, t, pool, seed.OrderID)

	wentOut, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindTicket, Recipient: seed.RecipientEmail, DeliveryJobIDs: seed.JobIDs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	neverSent, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindTicket, Recipient: seed.RecipientEmail, DeliveryJobIDs: seed.JobIDs[1:2]})
	if err != nil {
		t.Fatal(err)
	}
	// The relay accepted the first letter and the handler marked its job
	// sent, but Accepted never ran (the worker died). Both rows are a day old.
	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = $1`, seed.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE email_letters SET sent_at = now() - interval '2 days' WHERE id = ANY($1)`,
		[]uuid.UUID{wentOut, neverSent}); err != nil {
		t.Fatal(err)
	}
	if err := emailstatus.SweepStale(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var status string
	var receiptMissing bool
	if err := pool.QueryRow(ctx, `SELECT status, receipt_missing FROM email_letters WHERE id = $1`, wentOut).
		Scan(&status, &receiptMissing); err != nil {
		t.Fatalf("the letter that went out was deleted: %v", err)
	}
	if status != emailstatus.StatusSent || !receiptMissing {
		t.Fatalf("letter that went out = %q receipt_missing %v", status, receiptMissing)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_letters WHERE id = $1`, neverSent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a letter that never went out must be deleted by the sweep")
	}
}

// seedSessionChange writes a session change of the seed's session with a
// notice for the seed's order and returns the change id.
func seedSessionChange(ctx context.Context, t *testing.T, pool *pgxpool.Pool, seed orderLetterSeed, org uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO session_changes (session_id, event_id, org_id, kinds, old_state, new_state)
		VALUES ($1, $2, $3, ARRAY['time'], '{}', '{}') RETURNING id`, seed.SessionID, seed.EventID, org).Scan(&id); err != nil {
		t.Fatalf("session change: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_change_notices (change_id, order_id, route) VALUES ($1, $2, 'arena')`,
		id, seed.OrderID); err != nil {
		t.Fatalf("notice: %v", err)
	}
	return id
}

// TestLetterStatus_StaleChangeLetterIsNotConfirmedByAnotherNotice (review
// 2026-10-14 M1): a change letter left 'sending' must not become "sent"
// because ANOTHER change notice of the same order went out. Only the notice
// linked to the letter confirms it.
func TestLetterStatus_StaleChangeLetterIsNotConfirmedByAnotherNotice(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM session_changes WHERE session_id = $1`, seed.SessionID)
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	org := seedOrgID(ctx, t, pool, seed.OrderID)
	first := seedSessionChange(ctx, t, pool, seed, org)
	second := seedSessionChange(ctx, t, pool, seed, org)

	// The letter of the first change never went out (the worker died).
	lost, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindChange, Recipient: seed.RecipientEmail, ChangeID: first})
	if err != nil {
		t.Fatal(err)
	}
	// The letter of the second change did, and its notice says so.
	sent, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindChange, Recipient: seed.RecipientEmail, ChangeID: second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE session_change_notices SET state = 'sent', updated_at = now()
		WHERE change_id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE email_letters SET sent_at = now() - interval '2 days' WHERE id = ANY($1)`,
		[]uuid.UUID{lost, sent}); err != nil {
		t.Fatal(err)
	}
	if err := emailstatus.SweepStale(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_letters WHERE id = $1`, lost).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a change letter that never went out was confirmed by another change's notice")
	}
	var status string
	var receiptMissing bool
	if err := pool.QueryRow(ctx, `SELECT status, receipt_missing FROM email_letters WHERE id = $1`, sent).
		Scan(&status, &receiptMissing); err != nil {
		t.Fatalf("the letter its own notice confirms was deleted: %v", err)
	}
	if status != emailstatus.StatusSent || !receiptMissing {
		t.Fatalf("confirmed letter = %q receipt_missing %v", status, receiptMissing)
	}
}

// TestLetterStatus_StaleLetterOfAProcessingJobIsKept (review 2026-10-14 L2):
// a worker that died after the relay's 250 and before it marked the job
// leaves the job 'processing'. The letter may have gone out, so the sweep
// keeps it.
func TestLetterStatus_StaleLetterOfAProcessingJobIsKept(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1`, seed.OrderID)
	}()
	org := seedOrgID(ctx, t, pool, seed.OrderID)
	letter, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{OrgID: org, OrderID: &seed.OrderID,
		Kind: emailstatus.KindTicket, Recipient: seed.RecipientEmail, DeliveryJobIDs: seed.JobIDs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'processing' WHERE id = $1`, seed.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE email_letters SET sent_at = now() - interval '2 days' WHERE id = $1`, letter); err != nil {
		t.Fatal(err)
	}
	if err := emailstatus.SweepStale(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM email_letters WHERE id = $1`, letter).Scan(&status); err != nil {
		t.Fatalf("a letter whose job is still processing was deleted: %v", err)
	}
	if status != emailstatus.StatusSending {
		t.Fatalf("status = %q, want it kept as sending", status)
	}
}

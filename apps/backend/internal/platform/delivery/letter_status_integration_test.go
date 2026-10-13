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
	"testing"

	"github.com/google/uuid"

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

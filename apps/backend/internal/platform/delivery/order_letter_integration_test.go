//go:build integration

// order_letter_integration_test.go — live-DB proof that the tickets of one
// paid order go out in ONE e-mail (all PDFs attached, one "Payment" block),
// that the sibling worker jobs skip instead of mailing again, and that a
// failed send hands every claimed delivery job back to 'pending'.
//
// Same fixture and DATABASE_URL pattern as presentation_integration_test.go:
// point it at a FRESH scratch database, never at the shared dev stand.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// orderLetterSeed is the presentation fixture grown to a three-ticket order.
type orderLetterSeed struct {
	presentationSeed
	TicketIDs []uuid.UUID
	JobIDs    []uuid.UUID
}

// seedOrderOfThree adds two more tickets (each with an order item and a
// pending delivery job) to the order seedPresentationTicket built, and gives
// the checkout session the money the payment block reads: 3 x 18.90 + 1.90
// service charge = 40.60 EUR... expressed in minor units below.
func seedOrderOfThree(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (orderLetterSeed, func()) {
	t.Helper()
	base, baseCleanup := seedPresentationTicket(ctx, t, pool)
	out := orderLetterSeed{
		presentationSeed: base,
		TicketIDs:        []uuid.UUID{base.TicketID},
		JobIDs:           []uuid.UUID{base.DeliveryJobID},
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seedOrderOfThree: %v\n  sql: %s", err, strings.TrimSpace(sql))
		}
	}

	var csID, tierID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT checkout_session_id, tier_id FROM tickets WHERE id = $1`, base.TicketID,
	).Scan(&csID, &tierID); err != nil {
		t.Fatalf("seedOrderOfThree: read base ticket: %v", err)
	}

	for ordinal := 2; ordinal <= 3; ordinal++ {
		tktID, djID, itemID := uuid.New(), uuid.New(), uuid.New()
		exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		      VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			tktID, csID, base.SessionID, tierID, base.RecipientEmail, base.OrderID, ordinal-1)
		exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id,
		                               unit_price, discount, charge, total)
		      VALUES ($1, $2, $3, 'ticket', $4, $5, 1890, 0, 95, 1985)`,
			itemID, base.OrderID, ordinal, tierID, tktID)
		exec(`INSERT INTO delivery_jobs (id, ticket_id, recipient_email) VALUES ($1, $2, $3)`,
			djID, tktID, base.RecipientEmail)
		out.TicketIDs = append(out.TicketIDs, tktID)
		out.JobIDs = append(out.JobIDs, djID)
	}

	// 3 x 1890 subtotal + 285 service charge = 5955.
	exec(`UPDATE checkout_sessions
	         SET currency = 'EUR', subtotal = 5670, discount = 0, platform_fee = 285,
	             total = 5955, payment_provider = 'stripe'
	       WHERE id = $1`, csID)
	exec(`UPDATE orders SET paid_at = now() WHERE id = $1`, base.OrderID)

	cleanup := func() {
		for _, id := range out.TicketIDs[1:] {
			for _, sql := range []string{
				`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = $1`,
				`DELETE FROM ticket_credentials WHERE ticket_id = $1::uuid`,
				`DELETE FROM delivery_jobs WHERE ticket_id = $1::uuid`,
				`DELETE FROM order_items WHERE ticket_id = $1::uuid`,
				`DELETE FROM tickets WHERE id = $1::uuid`,
			} {
				if _, err := pool.Exec(ctx, sql, id.String()); err != nil {
					t.Logf("seedOrderOfThree cleanup: %s: %v", sql, err)
				}
			}
		}
		baseCleanup()
	}
	return out, cleanup
}

// attachmentNames returns the filenames of every attachment of a captured
// SMTP message, in order.
func attachmentNames(t *testing.T, raw string) []string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse captured message: %v", err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type: %v", err)
	}
	var names []string
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if part.FileName() != "" {
			names = append(names, part.FileName())
		}
		_, _ = io.Copy(io.Discard, part)
	}
	return names
}

func jobStatuses(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ids []uuid.UUID) map[uuid.UUID]string {
	t.Helper()
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM delivery_jobs WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatalf("read delivery job %s: %v", id, err)
		}
		out[id] = s
	}
	return out
}

// TestOrderLetter_OneEmailCarriesEveryTicketAndThePayment: the worker job of
// the first ticket sends ONE message with three PDFs and the payment block;
// the other two worker jobs find nothing to claim and send nothing.
func TestOrderLetter_OneEmailCarriesEveryTicketAndThePayment(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()

	smtp := newSMTPCaptureServer(t)
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             buildSMTPSender(smtp.Addr),
		Logger:             testLogger(),
	})

	// The middle ticket's worker job runs first: whichever job starts first
	// owns the letter, the position in the order does not matter.
	if err := handler(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[1].String(), Locale: "en"})); err != nil {
		t.Fatalf("first worker job: %v", err)
	}
	var raw string
	select {
	case b := <-smtp.Captured:
		raw = string(b)
	case <-time.After(10 * time.Second):
		t.Fatal("no message captured within 10s")
	}

	if names := attachmentNames(t, raw); len(names) != 3 {
		t.Fatalf("attachments = %v, want one PDF per ticket (3)", names)
	}

	body := emailTextBody(t, raw)
	for label, want := range map[string]string{
		"order number": fmt.Sprintf("%d", seed.OrderSystemID),
		"total":        "59.55 EUR",
		"subtotal":     "56.70 EUR",
		"service fee":  "2.85 EUR",
		"event":        seed.EventName,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("e-mail body lacks the %s %q\n--- body ---\n%s", label, want, body)
		}
	}
	for _, id := range seed.TicketIDs {
		if strings.Contains(body, id.String()) {
			t.Errorf("e-mail body leaks the ticket UUID %s", id)
		}
	}

	// Every job of the letter is 'sent' now.
	for id, status := range jobStatuses(ctx, t, pool, seed.JobIDs) {
		if status != StatusSent {
			t.Errorf("delivery_job %s is %q, want %q", id, status, StatusSent)
		}
	}

	// The other worker jobs skip: nil error, and a second message would need a
	// second SMTP session, which the capture server refuses (the handler would
	// then return an error).
	for _, i := range []int{0, 2} {
		if err := handler(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[i].String(), Locale: "en"})); err != nil {
			t.Errorf("worker job of ticket %d must skip silently, got: %v", i, err)
		}
	}
	select {
	case <-smtp.Captured:
		t.Error("a second e-mail was sent for the same order")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestOrderLetter_ResendOfOneTicketIsASingleTicketLetter: a resend requeues
// one ticket; its letter carries that ticket only and no payment block.
func TestOrderLetter_ResendOfOneTicketIsASingleTicketLetter(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()

	// The first send already happened: all three jobs are terminal.
	if _, err := pool.Exec(ctx,
		`UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = ANY($1)`, seed.JobIDs,
	); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	// Resend of ticket 2 only.
	if _, err := gen.New(pool).RequeueDeliveryJob(ctx, seed.TicketIDs[1], nil); err != nil {
		t.Fatalf("requeue: %v", err)
	}

	raw := runDeliveryWithPayload(ctx, t, pool, Payload{TicketID: seed.TicketIDs[1].String(), Locale: "en"})
	if names := attachmentNames(t, raw); len(names) != 1 {
		t.Fatalf("attachments = %v, want exactly the resent ticket", names)
	}
	body := emailTextBody(t, raw)
	if strings.Contains(body, "59.55 EUR") {
		t.Errorf("a one-ticket resend printed the whole order's total\n--- body ---\n%s", body)
	}
	statuses := jobStatuses(ctx, t, pool, seed.JobIDs)
	if statuses[seed.JobIDs[1]] != StatusSent {
		t.Errorf("resent job is %q, want sent", statuses[seed.JobIDs[1]])
	}
}

type failingSender struct{ calls int }

func (f *failingSender) Send(context.Context, email.Message) error {
	f.calls++
	return errors.New("smtp: 554 rejected")
}

// TestOrderLetter_FailedSendReleasesEveryJob: nothing was delivered, so all
// three claimed jobs are 'pending' again and the retry sends the whole letter.
func TestOrderLetter_FailedSendReleasesEveryJob(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()

	queries := gen.New(pool)
	sender := &failingSender{}
	handler := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             sender,
		Logger:             testLogger(),
	})
	if err := handler(ctx, mustJSON(t, Payload{TicketID: seed.TicketIDs[0].String(), Locale: "en"})); err == nil {
		t.Fatal("handler must return the send error so the worker retries")
	}
	if sender.calls != 1 {
		t.Fatalf("send calls = %d, want 1", sender.calls)
	}
	for id, status := range jobStatuses(ctx, t, pool, seed.JobIDs) {
		if status != StatusPending {
			t.Errorf("delivery_job %s is %q after a failed send, want %q", id, status, StatusPending)
		}
	}

	// The retry now succeeds with one message for the whole order.
	raw := runDeliveryWithPayload(ctx, t, pool, Payload{TicketID: seed.TicketIDs[0].String(), Locale: "en"})
	if names := attachmentNames(t, raw); len(names) != 3 {
		t.Fatalf("retry attachments = %v, want 3", names)
	}
}

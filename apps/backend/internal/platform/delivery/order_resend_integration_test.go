//go:build integration

// order_resend_integration_test.go — EC-13: the worker side of "resend an
// order's tickets to a one-time address". The route requeues every active
// ticket's delivery job with the chosen address and gives each worker payload
// a recipient_expires_at; here the real handler runs those jobs against a live
// database and a capturing SMTP server. Within the deadline the whole order
// leaves in ONE message to the chosen address; past it nothing is sent and the
// jobs end 'skipped', so a retry after a long outage never mails a stale
// address.
//
// Same fixture and DATABASE_URL pattern as order_letter_integration_test.go:
// point it at a FRESH scratch database.
package delivery

import (
	"context"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// requeueOrder does what the resend route does to the delivery jobs: every
// ticket's job is requeued to addr.
func requeueOrder(ctx context.Context, t *testing.T, q *gen.Queries, tickets []uuid.UUID, addr string) {
	t.Helper()
	for _, id := range tickets {
		if _, err := q.RequeueDeliveryJob(ctx, id, &addr); err != nil {
			t.Fatalf("requeue %s: %v", id, err)
		}
	}
}

func TestOrderResend_OneTimeAddressGetsOneLetter(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = ANY($1)`, seed.JobIDs); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	other := "one-time-" + uuid.NewString()[:8] + "@example.org"
	requeueOrder(ctx, t, gen.New(pool), seed.TicketIDs, other)

	expires := time.Now().UTC().Add(time.Hour)
	raw := runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID: seed.TicketIDs[2].String(), Locale: "en", RecipientExpiresAt: &expires,
	})
	if names := attachmentNames(t, raw); len(names) != 3 {
		t.Fatalf("attachments = %v, want one PDF per ticket (3) in one letter", names)
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	if to := msg.Header.Get("To"); !strings.Contains(to, other) {
		t.Fatalf("To = %q, want the one-time address %q", to, other)
	}
	for id, status := range jobStatuses(ctx, t, pool, seed.JobIDs) {
		if status != StatusSent {
			t.Errorf("delivery_job %s is %q, want sent", id, status)
		}
	}
	// The order's own address is untouched.
	var buyer *string
	if err := pool.QueryRow(ctx, `SELECT buyer_email FROM orders WHERE id = $1`, seed.OrderID).Scan(&buyer); err != nil {
		t.Fatal(err)
	}
	if buyer != nil && strings.EqualFold(*buyer, other) {
		t.Fatalf("the one-time address leaked into orders.buyer_email: %q", *buyer)
	}
}

func TestOrderResend_ExpiredOneTimeAddressIsNeverMailed(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = ANY($1)`, seed.JobIDs); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	requeueOrder(ctx, t, queries, seed.TicketIDs, "stale-"+uuid.NewString()[:8]+"@example.org")

	// The worker was down for a day: every payload carries a deadline in the past.
	past := time.Now().UTC().Add(-time.Minute)
	for _, id := range seed.TicketIDs {
		if err := handler(ctx, mustJSON(t, Payload{TicketID: id.String(), Locale: "en", RecipientExpiresAt: &past})); err != nil {
			t.Fatalf("an expired job must end quietly, got: %v", err)
		}
	}
	select {
	case <-smtp.Captured:
		t.Fatal("a letter was sent to an expired one-time address")
	case <-time.After(300 * time.Millisecond):
	}
	for id, status := range jobStatuses(ctx, t, pool, seed.JobIDs) {
		if status != StatusSkipped {
			t.Errorf("delivery_job %s is %q, want %q", id, status, StatusSkipped)
		}
	}
}

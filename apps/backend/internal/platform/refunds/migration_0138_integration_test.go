//go:build integration

// migration_0138_integration_test.go — the refunds.cancelled_ticket_id
// column of migration 0138 (PAY-03 sixth review, LOW-1 and LOW-2). The
// backfill is read verbatim out of the embedded migration file, between its
// markers, and run against rows written in the pre-0138 shape, so the test
// can never drift from what ships.
package refunds_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func backfill0138(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/0138_refund_engine.sql")
	if err != nil {
		t.Fatalf("read migration 0138: %v", err)
	}
	const (
		begin = "-- backfill cancelled_ticket_id: begin"
		end   = "-- backfill cancelled_ticket_id: end"
	)
	body := string(raw)
	i, j := strings.Index(body, begin), strings.Index(body, end)
	if i < 0 || j <= i {
		t.Fatal("migration 0138 lost its backfill markers")
	}
	return body[i+len(begin) : j]
}

// legacyTicketCancelRefund writes what POST /v1/tickets/{id}/cancel left
// behind before 0138: no marker, only requested_by.
func (f *fixture) legacyTicketCancelRefund(t *testing.T, ticket uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO refunds
		(payment_intent_id, org_id, amount, currency, reason, requested_by)
		VALUES ($1, $2, 100, 'EUR', 'legacy', $3) RETURNING id`,
		f.payment, f.org, refunds.TicketCancelRequestedByPrefix+ticket.String()).Scan(&id); err != nil {
		t.Fatalf("legacy refund: %v", err)
	}
	return id
}

// TestMigration0138_BackfillsLegacyTicketCancelRefunds (LOW-1): a legacy
// ticket-cancel refund gets its marker when the ticket links back to it,
// AND when the link was never written but the named ticket belongs to the
// payment's checkout session and is cancelled. A refund naming an active
// ticket (a client could have typed that requested_by) gets none.
func TestMigration0138_BackfillsLegacyTicketCancelRefunds(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	third := f.addTicket(t)
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `UPDATE tickets SET refund_id = NULL WHERE id = $1`, third) })
	linked := f.legacyTicketCancelRefund(t, f.tickets[0])
	f.exec(t, `UPDATE tickets SET status = 'cancelled', refund_id = $1 WHERE id = $2`, linked, f.tickets[0])
	unlinked := f.legacyTicketCancelRefund(t, f.tickets[1])
	f.exec(t, `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, f.tickets[1])
	forged := f.legacyTicketCancelRefund(t, third) // third stays active
	if _, err := f.pool.Exec(ctx, backfill0138(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for id, want := range map[uuid.UUID]*uuid.UUID{linked: &f.tickets[0], unlinked: &f.tickets[1], forged: nil} {
		var got *uuid.UUID
		if err := f.pool.QueryRow(ctx, `SELECT cancelled_ticket_id FROM refunds WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (want == nil) != (got == nil) || (want != nil && *got != *want) {
			t.Fatalf("refund %s: cancelled_ticket_id = %v, want %v", id, got, want)
		}
	}
}

// TestMigration0138_CancelledTicketIDIsConstrained (LOW-2): the marker
// names a real ticket, and a refund is either ticket-level or a
// ticket-cancel refund, never both.
func TestMigration0138_CancelledTicketIDIsConstrained(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	insert := func(ticketID, marker uuid.UUID) error {
		_, err := f.pool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, ticket_id, cancelled_ticket_id)
			VALUES ($1, $2, 100, 'EUR', $3, $4)`, f.payment, f.org, nullUUID(ticketID), marker)
		return err
	}
	code := func(err error) string {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			return pg.Code
		}
		return ""
	}
	if err := insert(uuid.Nil, uuid.New()); code(err) != "23503" {
		t.Fatalf("unknown ticket as marker: %v; want a foreign-key violation", err)
	}
	if err := insert(f.tickets[0], f.tickets[1]); code(err) != "23514" {
		t.Fatalf("ticket_id and cancelled_ticket_id together: %v; want a check violation", err)
	}
}

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

//go:build integration

// automatic_refund_marker_integration_test.go — PAY-03 fifth review, M-4:
// the refund POST /v1/tickets/{id}/cancel writes for refund_mode=automatic
// carries the ticket it speaks for in refunds.cancelled_ticket_id, written
// in the same transaction as the refund. The refund engine keys on that
// marker, never on requested_by (client text on POST /v1/refunds).
package htickets

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestAutomaticRefundCarriesItsTicketMarker(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)
	org, err := q.InsertOrganization(ctx, "PAY-03 marker "+uuid.NewString()[:8], "pay03-marker-"+uuid.NewString(), "ES", "en", 1200)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	// The marker references tickets(id): a real ticket is needed.
	venue, event, session, tier, channel, res, cs, ticket := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	defer func() {
		for _, s := range []string{
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM payment_intents WHERE org_id = $1`,
			`DELETE FROM tickets WHERE checkout_session_id IN (SELECT id FROM checkout_sessions WHERE org_id = $1)`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(ctx, s, org.ID); err != nil {
				t.Logf("cleanup %q: %v", s, err)
			}
		}
	}()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'V marker', 'Europe/Madrid')`, []any{venue, org.ID}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'E marker', 'published', 'public')`, []any{event, org.ID}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 10, 'scheduled', 'EUR', 'override')`, []any{session, event, venue}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
		  VALUES ($1, $2, 'Stalls', 'fixed', 2500, 'EUR', 1, true)`, []any{tier, session}},
		{`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'C marker', '{}'::jsonb)`, []any{channel, org.ID}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, []any{res, org.ID, channel, session}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, []any{cs, org.ID, channel, res}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, ordinal)
		  VALUES ($1, $2, $3, $4, 'buyer@example.com', 0)`, []any{ticket, cs, session, tier}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	ref := "cs_marker_" + uuid.NewString()
	pi, err := q.InsertPaymentIntent(ctx, &cs, org.ID, "stripe", &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	h := New(q, q, nil, q, q, q, nil, nil, pool, pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil)
	row, err := h.insertAutomaticRefund(ctx, &pi, ticket, 2500, "adult ticket cancelled", "ticket.cancel:"+ticket.String())
	if err != nil {
		t.Fatalf("insertAutomaticRefund: %v", err)
	}
	var marker *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT cancelled_ticket_id FROM refunds WHERE id = $1`, row.ID).Scan(&marker); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker == nil || *marker != ticket {
		t.Fatalf("cancelled_ticket_id = %v; want %s", marker, ticket)
	}
}

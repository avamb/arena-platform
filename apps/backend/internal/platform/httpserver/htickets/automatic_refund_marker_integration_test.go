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
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM refunds WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_intents WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()
	ref := "cs_marker_" + uuid.NewString()
	pi, err := q.InsertPaymentIntent(ctx, nil, org.ID, "stripe", &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	h := New(q, q, nil, q, q, q, nil, nil, pool, pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil)
	ticket := uuid.New()
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

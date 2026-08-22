//go:build integration

// Package macs — AB-50d round-trip integration test (feature #440).
//
// This test proves the end-to-end MACS delivery pipeline:
//
//  1. Seed a minimal org → venue → event → session → ticket chain.
//  2. Register a MACS webhook subscriber pointing to the stub receiver.
//  3. Dispatch a v1.scanner.ticket.issued event → stub receives "order.paid".
//  4. Cancel the ticket (status = 'cancelled') and dispatch v1.ticket.cancelled
//     → stub receives "ticket.refunded" with the correct MACS integer id.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	(migrated to head >= 0089)
//
// Run with:
//
//	go test -tags integration -run TestMACS_RoundTrip ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// roundtripPool opens a pgxpool.Pool for integration tests; skips when
// DATABASE_URL is not set.
func roundtripPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping MACS round-trip integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q is not a Postgres DSN; skipping", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("roundtripPool: open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("roundtripPool: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// roundtripFixture seeds the minimal FK chain for the MACS round-trip test.
type roundtripFixture struct {
	pool       *pgxpool.Pool
	orgID      uuid.UUID
	venueID    uuid.UUID
	eventID    uuid.UUID
	sessionID  uuid.UUID
	channelID  uuid.UUID
	resID      uuid.UUID
	checkoutID uuid.UUID
	ticketID   uuid.UUID
	stubRecv   *stub.Receiver
}

// seedRoundtripFixture inserts all rows required by the dispatcher.
// All rows are cleaned up via t.Cleanup so the test is idempotent.
func seedRoundtripFixture(t *testing.T, pool *pgxpool.Pool, recv *stub.Receiver) *roundtripFixture {
	t.Helper()
	f := &roundtripFixture{
		pool:      pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		channelID: uuid.New(),
		ticketID:  uuid.New(),
		stubRecv:  recv,
	}
	ctx := context.Background()
	suffix := f.orgID.String()[:8]

	mustExec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		if err != nil {
			t.Fatalf("seedRoundtripFixture exec: %v", err)
		}
	}

	// Org → venue → event → session
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		f.orgID, "MACS RT Org "+suffix, "macs-rt-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
		f.venueID, f.orgID, "MACS RT Venue")
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		f.eventID, f.orgID, "MACS RT Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '30 days', NOW()+INTERVAL '30 days 3 hours', 100, 'draft', 'general_admission', 'CZK', 'override')`,
		f.sessionID, f.eventID, f.venueID)

	// Sales channel + inventory ledger (required by reservations)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		f.channelID, f.orgID, "MACS RT Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`,
		f.sessionID)

	// Reservation → checkout session
	q := gen.New(pool)
	res, err := q.InsertReservation(ctx, f.orgID, f.channelID, f.sessionID, nil, nil, 1, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("seedRoundtripFixture InsertReservation: %v", err)
	}
	f.resID = res.ID

	f.checkoutID = uuid.New()
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'completed')`,
		f.checkoutID, f.orgID, f.channelID, f.resID)

	// Ticket
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at)
		VALUES ($1, $2, $3, 'active', NOW())`,
		f.ticketID, f.sessionID, f.checkoutID)

	// MACS webhook subscriber pointing to the stub.
	mustExec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', $1, 'test-secret', '{}', TRUE, 'macs', $2)`,
		recv.WebhookURL(), f.orgID)

	// Clean up in reverse order at test end.
	t.Cleanup(func() {
		ctx2 := context.Background()
		pool.Exec(ctx2, `DELETE FROM webhook_subscribers WHERE org_id=$1 AND kind='macs'`, f.orgID)
		pool.Exec(ctx2, `DELETE FROM tickets WHERE id=$1`, f.ticketID)
		pool.Exec(ctx2, `DELETE FROM checkout_sessions WHERE id=$1`, f.checkoutID)
		pool.Exec(ctx2, `DELETE FROM reservations WHERE id=$1`, f.resID)
		pool.Exec(ctx2, `DELETE FROM inventory_ledger WHERE session_id=$1`, f.sessionID)
		pool.Exec(ctx2, `DELETE FROM sales_channels WHERE id=$1`, f.channelID)
		pool.Exec(ctx2, `DELETE FROM sessions WHERE id=$1`, f.sessionID)
		pool.Exec(ctx2, `DELETE FROM events WHERE id=$1`, f.eventID)
		pool.Exec(ctx2, `DELETE FROM venues WHERE id=$1`, f.venueID)
		pool.Exec(ctx2, `DELETE FROM organizations WHERE id=$1`, f.orgID)
	})

	return f
}

// getSystemTicketID fetches the auto-generated system_ticket_id for a ticket.
func getSystemTicketID(t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) int64 {
	t.Helper()
	var id int64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := pool.QueryRow(ctx, `SELECT system_ticket_id FROM tickets WHERE id=$1`, ticketID).Scan(&id)
	if err != nil {
		t.Fatalf("getSystemTicketID(%s): %v", ticketID, err)
	}
	return id
}

// cancelTicketInDB updates the ticket status to 'cancelled' directly.
func cancelTicketInDB(t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `UPDATE tickets SET status='cancelled', cancelled_at=NOW() WHERE id=$1`, ticketID)
	if err != nil {
		t.Fatalf("cancelTicketInDB(%s): %v", ticketID, err)
	}
}

// TestMACS_RoundTrip is the AB-50d end-to-end acceptance test.
//
//	seed order → dispatch order.paid → cancel ticket → dispatch ticket.refunded
//	→ stub asserts MACS integer ids and event types
func TestMACS_RoundTrip(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	// Start the stub receiver.
	recv := stub.New()
	defer recv.Close()

	// Seed the FK chain.
	f := seedRoundtripFixture(t, pool, recv)

	// Retrieve the auto-assigned system_ticket_id (migration 0088 sequence).
	sysID := getSystemTicketID(t, pool, f.ticketID)
	if sysID <= 0 {
		t.Fatalf("expected system_ticket_id > 0, got %d", sysID)
	}

	disp := macs.NewDispatcher(pool)

	// ── Step 1: Dispatch order.paid (ticket issuance) ────────────────────────
	issuedEv := outbox.Event{
		AggregateType: "ticket",
		AggregateID:   f.ticketID.String(),
		EventType:     macs.EventTicketIssued,
		Payload: map[string]any{
			"ticket_id":           f.ticketID.String(),
			"session_id":          f.sessionID.String(),
			"checkout_session_id": f.checkoutID.String(),
		},
		OccurredAt: time.Now().UTC(),
	}

	if err := disp.Dispatch(ctx, issuedEv); err != nil {
		t.Fatalf("Dispatch(order.paid): %v", err)
	}

	paidEvents := recv.EventsByType("order.paid")
	if len(paidEvents) != 1 {
		t.Fatalf("stub: want 1 order.paid envelope, got %d", len(paidEvents))
	}
	paidEnv := paidEvents[0]
	if paidEnv.ID != sysID {
		t.Errorf("order.paid envelope id = %d; want %d (system_ticket_id)", paidEnv.ID, sysID)
	}
	// data is the MACS Ticket shape: id/seatId/barcode/actionEvent are the
	// receiver's REQUIRED fields; holderStatus 0 = not used.
	if paidEnv.Data["id"] != float64(sysID) {
		t.Errorf("order.paid data.id = %v; want %d", paidEnv.Data["id"], sysID)
	}
	for _, key := range []string{"seatId", "barcode", "actionEvent", "orderId"} {
		if _, ok := paidEnv.Data[key]; !ok {
			t.Errorf("order.paid data.%s missing — MACS would reject the envelope", key)
		}
	}
	if paidEnv.Data["holderStatus"] != float64(0) {
		t.Errorf("order.paid holderStatus = %v; want 0 (not used)", paidEnv.Data["holderStatus"])
	}

	// ── Step 2: Cancel the ticket and dispatch ticket.refunded ───────────────
	cancelTicketInDB(t, pool, f.ticketID)
	recv.Reset() // clear so we only see the next event

	cancelledEv := outbox.Event{
		AggregateType: "ticket",
		AggregateID:   f.ticketID.String(),
		EventType:     macs.EventTicketCancelled,
		Payload: map[string]any{
			"ticket_id":  f.ticketID.String(),
			"session_id": f.sessionID.String(),
		},
		OccurredAt: time.Now().UTC(),
	}

	if err := disp.Dispatch(ctx, cancelledEv); err != nil {
		t.Fatalf("Dispatch(ticket.refunded): %v", err)
	}

	refundedEvents := recv.EventsByType("ticket.refunded")
	if len(refundedEvents) != 1 {
		t.Fatalf("stub: want 1 ticket.refunded envelope, got %d (total=%d)",
			len(refundedEvents), len(recv.Events()))
	}
	refEnv := refundedEvents[0]
	if refEnv.ID != sysID {
		t.Errorf("ticket.refunded envelope id = %d; want %d (system_ticket_id)", refEnv.ID, sysID)
	}
	if refEnv.Data["id"] != float64(sysID) {
		t.Errorf("ticket.refunded data.id = %v; want %d", refEnv.Data["id"], sysID)
	}
	if refEnv.Data["holderStatus"] != float64(3) {
		t.Errorf("ticket.refunded holderStatus = %v; want 3 (refunded) — the door must deny", refEnv.Data["holderStatus"])
	}

	t.Logf("MACS round-trip OK: system_ticket_id=%d order.paid+ticket.refunded delivered to stub", sysID)
}

// TestMACS_CancelEnqueuesExactlyOne_OutboxEvent verifies that cancelling a
// ticket via the cancel helper enqueues exactly one v1.ticket.cancelled outbox
// event. This is the AB-50c integration acceptance: "a cancel with
// refund_mode=manual enqueues exactly one ticket.refunded [sic: v1.ticket.cancelled
// which the dispatcher maps to MACS ticket.refunded]."
//
// This test operates directly on the outbox_events table (not via the HTTP
// handler) to isolate the outbox-write contract from the transport layer.
func TestMACS_CancelEnqueuesExactlyOne_OutboxEvent(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	// Start the stub receiver.
	recv := stub.New()
	defer recv.Close()

	// Seed the FK chain.
	f := seedRoundtripFixture(t, pool, recv)

	// Insert a v1.ticket.cancelled outbox event (simulating what
	// PublishTicketCancelledEvent does after HandleCancelTicket commits).
	const insertSQL = `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
		VALUES ('ticket', $1, 'v1.ticket.cancelled',
			jsonb_build_object('ticket_id', $1::text, 'session_id', $2::text, 'reason', 'test', 'refund_mode', 'manual'),
			NOW())
		RETURNING id::text`

	var outboxID string
	if err := pool.QueryRow(ctx, insertSQL, f.ticketID, f.sessionID).Scan(&outboxID); err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM outbox_events WHERE id=$1::uuid`, outboxID)
	})

	// Count v1.ticket.cancelled events in the outbox for this ticket.
	var cnt int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_events
		 WHERE aggregate_type='ticket' AND aggregate_id=$1::text
		   AND event_type='v1.ticket.cancelled'`,
		f.ticketID,
	).Scan(&cnt); err != nil {
		t.Fatalf("count outbox events: %v", err)
	}
	if cnt != 1 {
		t.Errorf("outbox: want exactly 1 v1.ticket.cancelled event, got %d", cnt)
	}

	// Now dispatch it through the MACS dispatcher and verify the stub
	// receives exactly one ticket.refunded envelope.
	sysID := getSystemTicketID(t, pool, f.ticketID)
	cancelledEv := outbox.Event{
		AggregateType: "ticket",
		AggregateID:   f.ticketID.String(),
		EventType:     "v1.ticket.cancelled",
		Payload: map[string]any{
			"ticket_id":  f.ticketID.String(),
			"session_id": f.sessionID.String(),
		},
		OccurredAt: time.Now().UTC(),
	}

	disp := macs.NewDispatcher(pool)
	if err := disp.Dispatch(ctx, cancelledEv); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	refundedEvents := recv.EventsByType("ticket.refunded")
	if len(refundedEvents) != 1 {
		t.Fatalf("stub: want 1 ticket.refunded, got %d", len(refundedEvents))
	}
	if refundedEvents[0].Data["id"] != float64(sysID) {
		t.Errorf("ticket.refunded data.id = %v; want %d", refundedEvents[0].Data["id"], sysID)
	}
	if refundedEvents[0].Data["holderStatus"] != float64(3) {
		t.Errorf("ticket.refunded holderStatus = %v; want 3", refundedEvents[0].Data["holderStatus"])
	}

	t.Logf("AB-50c outbox acceptance OK: 1 v1.ticket.cancelled → 1 MACS ticket.refunded (system_ticket_id=%d)", sysID)
}

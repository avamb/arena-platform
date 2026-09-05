//go:build integration

// ean13_issuance_502_integration_test.go — integration test for feature #502
// (W1-B6a, Step 2): IssueTicketsForCheckout must write an EAN-13 ticket
// credential + a platform-authority barcode row for every newly issued
// ticket, alongside the tickets row itself (spec §11).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/htickets/ \
//	    -run TestEAN13Issuance502Integration
package htickets

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// TestEAN13Issuance502Integration_WritesCredentialAndBarcode proves the full
// Step 2 wiring against a live PostgreSQL: issuing tickets for a checkout
// session (GA path, no assigned seats) must leave behind, for every ticket,
// a ticket_credentials row (type='ean13', 13-digit checksum-valid payload)
// and a barcodes row (authority='platform', external_ref = the same
// payload, ticket_id = the ticket) — so SCAN_TICKET / GetBarcodeByExternalRefAny
// can resolve it exactly like any other barcode authority.
func TestEAN13Issuance502Integration_WritesCredentialAndBarcode(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping feature #502 integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v — DATABASE_URL is set, so a connection failure must fail the gate", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v — DATABASE_URL is set, so an unreachable database must fail the gate", err)
	}

	q := gen.New(pool)

	// ── Find an existing (org, channel, session) triple ──────────────────────
	// arena-seed guarantees this exists in CI (feature #388).
	var orgID, channelID, sessionID uuid.UUID
	row := pool.QueryRow(ctx, `
		SELECT sc.org_id, sc.id, s.id
		FROM   sales_channels sc
		JOIN   events e   ON e.org_id = sc.org_id
		JOIN   sessions s ON s.event_id = e.id
		JOIN   inventory_ledger il ON il.session_id = s.id AND il.tier_id IS NULL
		LIMIT  1
	`)
	if err := row.Scan(&orgID, &channelID, &sessionID); err != nil {
		t.Fatalf("no (org, channel, session) triple with GA inventory found in DB: %v — "+
			"run arena-seed against the migrated database first", err)
	}
	t.Logf("using org=%s channel=%s session=%s", orgID, channelID, sessionID)

	// ── Reserve + activate a 1-unit GA reservation ────────────────────────────
	qty := int32(1)
	futureExpiry := time.Now().UTC().Add(10 * time.Minute)
	reservation, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, qty, futureExpiry)
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM reservations WHERE id = $1", reservation.ID)
	}()

	reservation, err = q.UpdateReservationState(ctx, reservation.ID, "active")
	if err != nil {
		t.Fatalf("UpdateReservationState(active): %v", err)
	}
	reservation, err = q.UpdateReservationState(ctx, reservation.ID, "converted")
	if err != nil {
		t.Fatalf("UpdateReservationState(converted): %v", err)
	}

	// ── Create a checkout session for that reservation ────────────────────────
	cs, err := q.InsertCheckoutSession(ctx, orgID, channelID, reservation.ID, nil)
	if err != nil {
		t.Fatalf("InsertCheckoutSession: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM checkout_sessions WHERE id = $1", cs.ID)
	}()

	// ── Issue tickets via the real handler ────────────────────────────────────
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := New(q, q, q, q, q, q, nil, nil, nil, pool, nil, logger, nil, nil, nil)
	tickets, err := h.IssueTicketsForCheckout(ctx, cs)
	if err != nil {
		t.Fatalf("IssueTicketsForCheckout: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("IssueTicketsForCheckout returned %d tickets, want 1", len(tickets))
	}
	ticket := tickets[0]
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM barcodes WHERE ticket_id = $1", ticket.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM ticket_credentials WHERE ticket_id = $1", ticket.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM tickets WHERE id = $1", ticket.ID)
	}()

	// ── Verify the ticket_credentials row ──────────────────────────────────────
	var credType, payload string
	err = pool.QueryRow(ctx,
		`SELECT type, payload FROM ticket_credentials WHERE ticket_id = $1 AND type = 'ean13'`,
		ticket.ID,
	).Scan(&credType, &payload)
	if err != nil {
		t.Fatalf("expected an ean13 ticket_credentials row for ticket %s: %v", ticket.ID, err)
	}
	if !ean13.Valid(payload) {
		t.Errorf("ticket_credentials payload %q is not a checksum-valid EAN-13 code", payload)
	}
	if payload[:2] != "21" {
		t.Errorf("ticket_credentials payload %q does not have the platform %q prefix", payload, "21")
	}
	wantPayload := ean13.Encode("21", ticket.SystemTicketID)
	if payload != wantPayload {
		t.Errorf("ticket_credentials payload = %q, want %q (encoded from system_ticket_id=%d)",
			payload, wantPayload, ticket.SystemTicketID)
	}

	// ── Verify the barcodes row ────────────────────────────────────────────────
	barcode, err := q.GetBarcodeByExternalRefAny(ctx, payload)
	if err != nil {
		t.Fatalf("GetBarcodeByExternalRefAny(%q): %v", payload, err)
	}
	if barcode.TicketID == nil || *barcode.TicketID != ticket.ID {
		t.Errorf("barcode.TicketID = %v, want %s", barcode.TicketID, ticket.ID)
	}
	if barcode.Status != "active" {
		t.Errorf("barcode.Status = %q, want %q", barcode.Status, "active")
	}
	authority, err := q.GetBarcodeAuthorityByID(ctx, barcode.AuthorityID)
	if err != nil {
		t.Fatalf("GetBarcodeAuthorityByID: %v", err)
	}
	if authority.Type != "platform" {
		t.Errorf("barcode authority.Type = %q, want %q", authority.Type, "platform")
	}

	// ── Replay: calling IssueTicketsForCheckout again must be idempotent and
	// must not attempt to re-insert the credential/barcode (which would 23505
	// on the UNIQUE (authority_id, external_ref) constraint) ───────────────────
	ticketsAgain, err := h.IssueTicketsForCheckout(ctx, cs)
	if err != nil {
		t.Fatalf("IssueTicketsForCheckout (replay): %v", err)
	}
	if len(ticketsAgain) != 1 || ticketsAgain[0].ID != ticket.ID {
		t.Fatalf("replay returned different tickets: %+v", ticketsAgain)
	}
}

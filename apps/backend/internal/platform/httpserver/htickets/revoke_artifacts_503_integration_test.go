//go:build integration

// revoke_artifacts_503_integration_test.go — integration test for feature
// #503 (W1-B6b): RevokeTicketArtifactsTx must revoke ALL THREE credential
// types (static_qr, pdf, ean13) — not just "qr"/"pdf" as the pre-#503 typo
// (`[]string{"qr", "pdf"}`, "qr" instead of "static_qr") left it — plus
// every barcode row for the ticket, regardless of authority.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/htickets/ \
//	    -run TestRevokeTicketArtifacts503Integration
package htickets

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestRevokeTicketArtifacts503Integration_RevokesAllCredentialTypesAndBarcodes(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping feature #503 integration test")
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

	// ── Reserve + activate a 1-unit GA reservation, then issue a ticket ───────
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

	cs, err := q.InsertCheckoutSession(ctx, orgID, channelID, reservation.ID, nil)
	if err != nil {
		t.Fatalf("InsertCheckoutSession: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM checkout_sessions WHERE id = $1", cs.ID)
	}()

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

	// Issuance (feature #502) already left an ean13 credential + platform
	// barcode behind. Round out the fixture with static_qr and pdf
	// credentials plus a second (legacy_bil24) barcode, so the test proves
	// RevokeTicketArtifactsTx handles every credential type and every
	// barcode authority, not just the ean13/platform pair issuance created.
	if _, err := q.InsertTicketCredential(ctx, ticket.ID, "static_qr", "deadbeefcafef00d"); err != nil {
		t.Fatalf("InsertTicketCredential(static_qr): %v", err)
	}
	if _, err := q.InsertTicketCredential(ctx, ticket.ID, "pdf", "cGRmLWJ5dGVz"); err != nil {
		t.Fatalf("InsertTicketCredential(pdf): %v", err)
	}
	// The 'legacy_bil24' authority is only seeded on demand (via
	// POST /v1/barcodes/authorities or the Bil24 import tooling), not by
	// migration 0029 itself — only 'platform' is. Fetch-or-create it here so
	// this test doesn't depend on prior state of the dev-stand/CI database.
	legacyAuthority, err := q.GetBarcodeAuthorityByType(ctx, "legacy_bil24")
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetBarcodeAuthorityByType(legacy_bil24): %v", err)
		}
		legacyAuthority, err = q.InsertBarcodeAuthority(ctx, "legacy_bil24", "Legacy Bil24")
		if err != nil {
			t.Fatalf("InsertBarcodeAuthority(legacy_bil24): %v", err)
		}
	}
	ticketID := ticket.ID
	if _, err := q.InsertBarcode(ctx, legacyAuthority.ID, "24"+ticket.ID.String()[:11], &ticketID); err != nil {
		t.Fatalf("InsertBarcode(legacy_bil24): %v", err)
	}

	// ── Revoke, inside a transaction, exactly as cancel.go does ──────────────
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	RevokeTicketArtifactsTx(ctx, logger, q, q, tx, []gen.TicketRow{ticket})
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("tx.Commit: %v", err)
	}

	// ── Every credential type must carry a non-nil revoked_at ────────────────
	for _, credType := range []string{"static_qr", "pdf", "ean13"} {
		cred, err := q.GetCredentialByTicketID(ctx, ticket.ID, credType)
		if err != nil {
			t.Fatalf("GetCredentialByTicketID(%q): %v", credType, err)
		}
		if cred.RevokedAt == nil {
			t.Errorf("credential type=%q: RevokedAt is nil, want non-nil (regression of the qr/static_qr typo)", credType)
		}
	}

	// ── Every barcode row for the ticket must be status='revoked' ────────────
	barcodes, err := q.ListBarcodesByTicketID(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("ListBarcodesByTicketID: %v", err)
	}
	if len(barcodes) != 2 {
		t.Fatalf("ListBarcodesByTicketID returned %d rows, want 2 (platform ean13 + legacy_bil24)", len(barcodes))
	}
	for _, b := range barcodes {
		if b.Status != "revoked" {
			t.Errorf("barcode %s (authority=%s): Status = %q, want %q", b.ID, b.AuthorityID, b.Status, "revoked")
		}
	}
}

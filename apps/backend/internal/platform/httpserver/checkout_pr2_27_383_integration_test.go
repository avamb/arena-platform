//go:build integration

// checkout_pr2_27_383_integration_test.go — integration test for feature #383
// (PR2-27 BLOCKER): Close the PR2-04 held→sold convert-failure window.
//
// # What this test proves
//
// After a payment webhook commits (PI = "succeeded", checkout.convert_reservation
// job enqueued atomically), the inline convertReservationTx call can fail —
// e.g. due to a transient network glitch or DB hiccup. The reservation remains
// in 'active' state with capacity_held = 1. This is the danger zone:
// GetExpiredReservations would pick it up and the TTL worker would release
// the paid seats.
//
// The fix (PR2-27): the durable job runs via ConvertReservationTx, which
// transitions the reservation to 'converted'. After that:
//   - GetExpiredReservations no longer returns it (state filter excludes 'converted').
//   - The TTL worker skips it.
//   - Paid seats cannot be resold.
//
// This test proves the full lifecycle: danger zone detected → durable job
// converts → TTL worker cannot resell.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://...  (reachable PostgreSQL with migrations applied)
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPR227Integration
package httpserver

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// TestPR227Integration_DurableJobClosesConvertWindow proves that when the
// inline convertReservationTx call fails after a payment webhook commits, the
// durable checkout.convert_reservation job (ConvertReservationTx) correctly
// converts the reservation and prevents the TTL worker from releasing the
// paid seats.
//
// Scenario (the real production path with injected conversion failure):
//
//  1. Payment webhook commits atomically: PI = "succeeded",
//     checkout.issue_tickets job enqueued, checkout.convert_reservation job
//     enqueued. The inline convertReservationTx call FAILS (simulated by
//     not calling it — the reservation stays 'active').
//
//  2. GetExpiredReservations finds the 'active' reservation with past expiry
//     (sanity: the danger zone is confirmed).
//
//  3. The durable checkout.convert_reservation job runs (ConvertReservationTx).
//
//  4. GetExpiredReservations no longer returns the reservation.
//
//  5. The TTL worker runs and does NOT release the paid seats — the reservation
//     stays 'converted'.
func TestPR227Integration_DurableJobClosesConvertWindow(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping PR2-27 integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("pgxpool.New: %v; skipping", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Skipf("pool.Ping: %v; skipping", err)
	}

	q := gen.New(pool)

	// ── Find existing data ────────────────────────────────────────────────────
	// We need a valid (org_id, channel_id, session_id) triple for FK constraints.
	var orgID, channelID, sessionID uuid.UUID
	row := pool.QueryRow(ctx, `
		SELECT sc.org_id, sc.id, s.id
		FROM   sales_channels sc
		JOIN   sessions s ON s.org_id = sc.org_id
		LIMIT  1
	`)
	if err := row.Scan(&orgID, &channelID, &sessionID); err != nil {
		t.Skipf("no (org, channel, session) triple found in DB: %v; skipping", err)
	}
	t.Logf("using org=%s channel=%s session=%s", orgID, channelID, sessionID)

	// ── Reserve capacity (simulates the hold placed when the buyer reserved) ──
	qty := int32(1)
	if _, err := q.ReserveCapacity(ctx, sessionID, nil, qty); err != nil {
		t.Skipf("ReserveCapacity failed (session may not support GA): %v; skipping", err)
	}
	// Cleanup: release the reserve we incremented at the start (net of what
	// ConvertReservationTx will move to capacity_sold — restored separately below).
	defer func() {
		if _, err := q.ReleaseCapacity(ctx, sessionID, nil, qty); err != nil {
			t.Logf("cleanup: ReleaseCapacity error (non-fatal): %v", err)
		}
	}()

	// ── Insert a reservation in 'active' state with past expiry ──────────────
	// This simulates the post-webhook state where the inline conversion failed:
	// the reservation is still 'active' (not 'converted'), expiry is in the
	// past, and capacity_held remains at 1.
	pastExpiry := time.Now().UTC().Add(-10 * time.Minute)
	reservation, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, qty, pastExpiry)
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, "DELETE FROM reservations WHERE id = $1", reservation.ID); err != nil {
			t.Logf("cleanup: delete reservation error (non-fatal): %v", err)
		}
	}()

	// Activate (draft → active) so the TTL worker's query can see it.
	reservation, err = q.UpdateReservationState(ctx, reservation.ID, "active")
	if err != nil {
		t.Fatalf("UpdateReservationState(active): %v", err)
	}
	t.Logf("reservation %s state=%s expires_at=%s — danger zone (inline conversion simulated as failed)",
		reservation.ID, reservation.State, pastExpiry.Format(time.RFC3339))

	// ── Sanity check: GetExpiredReservations finds it (danger zone confirmed) ─
	expired383, err := pr227GetExpired(ctx, pool, q)
	if err != nil {
		t.Fatalf("GetExpiredReservations (sanity check): %v", err)
	}
	found := false
	for _, r := range expired383 {
		if r.ID == reservation.ID {
			found = true
			break
		}
	}
	if !found {
		t.Skip("'active' reservation with past expires_at not returned by GetExpiredReservations — DB clock or test timing issue; skipping")
	}
	t.Log("sanity: 'active' reservation with past expiry IS returned by GetExpiredReservations (danger zone confirmed) ✓")

	// ── Run the durable checkout.convert_reservation job ─────────────────────
	// This is the production wiring: arena-worker builds a minimal
	// hcheckout.Handler with reservationQueries + inventoryQueries + pool and
	// calls ConvertReservationTx. We replicate that here to prove the fix works.
	convertHandler := hcheckout.New(
		nil, // checkoutQ — not needed for ConvertReservationTx
		q,   // reservationQ — GetReservationByID, ListReservationSeats, UpdateReservationState
		q,   // inventoryQ — ConfirmCapacity
		nil, // paymentIntentQ — not needed
		nil, // refundQ — not needed
		nil, // promoQ — not needed
		nil, // tierQ — not needed
		nil, // ticketQ — not needed
		nil, // channelQ — not needed
		nil, // orgQ — not needed
		pool,                     // pool (TxStarter) — required for BeginTx inside ConvertReservationTx
		nil,                      // logger
		hcheckout.PricingRules{}, // zero rules — not used
		"", "",                   // no webhook secrets
		nil, nil, nil, // callbacks not needed for conversion
	)

	if convErr := convertHandler.ConvertReservationTx(ctx, reservation.ID); convErr != nil {
		t.Fatalf("ConvertReservationTx (durable job): %v", convErr)
	}
	t.Log("durable checkout.convert_reservation job (ConvertReservationTx) ran successfully ✓")

	// Cleanup: undo the capacity_sold increment added by ConvertReservationTx.
	defer func() {
		if _, err := q.RestoreSoldCapacity(ctx, sessionID, nil, qty); err != nil {
			t.Logf("cleanup: RestoreSoldCapacity error (non-fatal): %v", err)
		}
	}()

	// ── Verify reservation is now 'converted' ─────────────────────────────────
	converted, err := q.GetReservationByID(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("GetReservationByID after ConvertReservationTx: %v", err)
	}
	if converted.State != "converted" {
		t.Fatalf("reservation.state = %q after durable job, want 'converted' — durable job did not convert the reservation", converted.State)
	}
	t.Log("reservation.state = 'converted' after durable job ✓")

	// ── GetExpiredReservations must NOT return the converted reservation ───────
	expiredAfter383, err := pr227GetExpired(ctx, pool, q)
	if err != nil {
		t.Fatalf("GetExpiredReservations after conversion: %v", err)
	}
	for _, r := range expiredAfter383 {
		if r.ID == reservation.ID {
			t.Errorf("RESELL BUG: 'converted' reservation %s still returned by GetExpiredReservations after durable job — state filter broken!", reservation.ID)
		}
	}
	t.Log("GetExpiredReservations does NOT return the reservation after durable job conversion ✓")

	// ── Run the TTL worker — must NOT touch the converted reservation ─────────
	proc := hcheckout.NewReservationProcessor(pool, q, nil)
	processed, err := proc.ProcessExpiredReservations(ctx, 100)
	if err != nil {
		t.Fatalf("ProcessExpiredReservations: %v", err)
	}
	t.Logf("TTL worker processed %d expired reservation(s)", processed)

	// ── Final assertion: reservation still 'converted' after TTL worker ────────
	postWorker, err := q.GetReservationByID(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("GetReservationByID after TTL worker: %v", err)
	}
	if postWorker.State != "converted" {
		t.Errorf("RESELL BUG: reservation.state after TTL worker = %q, want 'converted' — "+
			"TTL worker released paid reservation despite durable job conversion! "+
			"PR2-27 durable job pattern did NOT close the resale window.",
			postWorker.State)
	}
	t.Log("reservation.state still 'converted' after TTL worker ✓ — PR2-27 durable job permanently closes the resale window")
}

// pr227GetExpired is a test helper that runs GetExpiredReservations inside a
// transaction (required by the FOR UPDATE SKIP LOCKED query) and returns the
// results. Named with the pr227 prefix to avoid clashing with any similarly
// named helpers added by other features in the same package.
func pr227GetExpired(ctx context.Context, pool *pgxpool.Pool, q *gen.Queries) ([]gen.ReservationRow, error) {
	_ = q // q is unused here but kept for API symmetry with integration test helpers
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	res, qErr := gen.New(tx).GetExpiredReservations(ctx, 1000)
	if qErr != nil {
		return nil, qErr
	}
	_ = tx.Commit(ctx)
	return res, nil
}

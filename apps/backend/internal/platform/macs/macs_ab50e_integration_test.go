//go:build integration

// Package macs — AB-50e integration test (feature #441).
//
// This test proves the enhanced stub receiver with ticket store,
// HMAC verification, and per-ticket holderStatus tracking:
//
//  1. Seed 3 tickets in one session (3 separate checkout sessions).
//     The session venue is linked to a city so the MACS export carries
//     a non-empty cityName (strict stub validation, AB-50g).
//  2. Start a secret-bearing stub receiver (HMAC-SHA256 verification).
//  3. Export all tickets and POST to stub /import/tickets.
//  4. Verify /import/tickets validates required fields (422 on missing).
//  5. Verify stub rejects envelopes with invalid HMAC signature (401).
//  6. Dispatch order.paid for all 3 tickets → stub records holderStatus=0.
//  7. Cancel ticket[0]; first delivery → stub returns 503 (retry simulation).
//  8. Retry dispatch → succeeds; stub now has holderStatus=3 for ticket[0].
//  9. ticket[1] and ticket[2] remain holderStatus=0.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	(migrated to head >= 0089)
//
// Run with:
//
//	go test -tags integration -run TestMACS_AB50e ./apps/backend/internal/platform/macs/
package macs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// TestMACS_AB50e_ThreeTicketRoundTrip is the AB-50e end-to-end acceptance test.
//
//	3 tickets seeded → export → import into stub →
//	order.paid for all 3 → cancel ticket[0] with retry →
//	assert per-ticket holderStatus (0/0/0 → 3/0/0) →
//	HMAC validation verified on stub.
func TestMACS_AB50e_ThreeTicketRoundTrip(t *testing.T) {
	pool := roundtripPool(t) // skips when DATABASE_URL not set
	ctx := context.Background()

	const signingSecret = "ab50e-test-secret-hmac"

	// Start the stub with HMAC verification enabled.
	recv := stub.NewWithSecret(signingSecret)
	defer recv.Close()

	// ── Seed: org → city → venue → event → session → channel ─────────────
	orgID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	cityID := uuid.New()
	suffix := orgID.String()[:8]
	citySlug := "macs-ab50e-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		if err != nil {
			t.Fatalf("seedAB50e exec: %v\nSQL: %s", err, sql)
		}
	}

	// Seed city using the IL country from migration 0006.
	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migration 0006 not applied?): %v", err)
	}
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`,
		cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'AB50e Test City')`,
		citySlug)

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "AB50e Org "+suffix, "ab50e-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`,
		venueID, orgID, "AB50e Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "AB50e Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '60 days', NOW()+INTERVAL '60 days 3 hours',
		        100, 'draft', 'general_admission', 'CZK', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "AB50e Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`,
		sessionID)

	// MACS webhook subscriber with the test signing secret.
	mustExec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', $1, $2, '{}', TRUE, 'macs', $3)`,
		recv.WebhookURL(), signingSecret, orgID)

	// ── Seed: 3 reservations → 3 checkout sessions → 3 tickets ──────────
	var ticketIDs [3]uuid.UUID
	var checkoutIDs [3]uuid.UUID
	q := gen.New(pool)

	for i := 0; i < 3; i++ {
		res, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, 1, time.Now().UTC().Add(30*time.Minute))
		if err != nil {
			t.Fatalf("InsertReservation[%d]: %v", i, err)
		}

		checkoutIDs[i] = uuid.New()
		mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed')`,
			checkoutIDs[i], orgID, channelID, res.ID)

		ticketIDs[i] = uuid.New()
		mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at)
			VALUES ($1, $2, $3, 'active', NOW())`,
			ticketIDs[i], sessionID, checkoutIDs[i])
	}

	// Cleanup in reverse order.
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM webhook_subscribers WHERE org_id=$1 AND kind='macs'`, orgID)
		for i := 2; i >= 0; i-- {
			pool.Exec(c, `DELETE FROM tickets WHERE id=$1`, ticketIDs[i])
			pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, checkoutIDs[i])
		}
		pool.Exec(c, `DELETE FROM reservations WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		// City + i18n_text must be deleted after the venue that references it.
		pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	// ── Retrieve system_ticket_ids ────────────────────────────────────────
	var sysIDs [3]int64
	for i := 0; i < 3; i++ {
		sysIDs[i] = getSystemTicketID(t, pool, ticketIDs[i])
		if sysIDs[i] <= 0 {
			t.Fatalf("ticket[%d] system_ticket_id = %d; want > 0", i, sysIDs[i])
		}
	}

	// ── Step 1: MACS export and import into stub ──────────────────────────
	export, err := macs.QueryAndBuildExport(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("QueryAndBuildExport: %v", err)
	}
	if len(export) != 3 {
		t.Fatalf("export: want 3 orders (one per checkout session), got %d", len(export))
	}

	// Verify cityName is populated (city seeding validates the fix).
	if export[0].TicketList[0].ActionEvent.CityName == "" {
		t.Fatal("export: cityName is empty — city seeding failed")
	}

	// Validate required-field check (422) — tamper with a copy.
	badExport := make([]map[string]any, 1)
	badExport[0] = map[string]any{
		"id":         float64(sysIDs[0]),
		"ticketList": []map[string]any{{"id": float64(sysIDs[0])}}, // missing barcode, seatId, actionEvent
	}
	badBody, _ := json.Marshal(badExport)
	badResp, err := http.Post(recv.ImportURL(), "application/json", bytes.NewReader(badBody))
	if err != nil {
		t.Fatalf("import bad payload: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad import: want 422, got %d", badResp.StatusCode)
	}

	// Import the well-formed export.
	exportBody, _ := json.Marshal(export)
	importResp, err := http.Post(recv.ImportURL(), "application/json", bytes.NewReader(exportBody))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	importResp.Body.Close()
	if importResp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", importResp.StatusCode)
	}

	// Assert all 3 tickets are stored in the stub with holderStatus=0.
	for i, sysID := range sysIDs {
		tk := recv.TicketByID(sysID)
		if tk == nil {
			t.Errorf("after import: ticket[%d] (sysID=%d) not in stub store", i, sysID)
			continue
		}
		if tk.HolderStatus != 0 {
			t.Errorf("after import: ticket[%d] holderStatus = %d, want 0", i, tk.HolderStatus)
		}
	}

	// ── Step 2: HMAC verification — stub rejects bad signature ───────────
	badSigReq, _ := http.NewRequest(http.MethodPost, recv.WebhookURL(),
		bytes.NewReader([]byte(`{"id":1,"created":"2026-01-01T00:00:00Z","type":"test","data":{}}`)))
	badSigReq.Header.Set("Content-Type", "application/json")
	badSigReq.Header.Set("X-MACS-Signature", "sha256=badhash")
	badSigResp, err := http.DefaultClient.Do(badSigReq)
	if err != nil {
		t.Fatalf("bad-sig delivery: %v", err)
	}
	badSigResp.Body.Close()
	if badSigResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad HMAC: want 401, got %d", badSigResp.StatusCode)
	}

	disp := macs.NewDispatcher(pool)

	// ── Step 3: Dispatch order.paid for all 3 tickets ────────────────────
	for i, ticketID := range ticketIDs {
		issuedEv := outbox.Event{
			AggregateType: "ticket",
			AggregateID:   ticketID.String(),
			EventType:     macs.EventTicketIssued,
			Payload: map[string]any{
				"ticket_id":           ticketID.String(),
				"session_id":          sessionID.String(),
				"checkout_session_id": checkoutIDs[i].String(),
			},
			OccurredAt: time.Now().UTC(),
		}
		if err := disp.Dispatch(ctx, issuedEv); err != nil {
			t.Fatalf("Dispatch order.paid ticket[%d]: %v", i, err)
		}
	}

	paidEvents := recv.EventsByType("order.paid")
	if len(paidEvents) != 3 {
		t.Fatalf("stub: want 3 order.paid envelopes, got %d", len(paidEvents))
	}

	// Verify holderStatus=0 for all 3 tickets after order.paid.
	for i, sysID := range sysIDs {
		tk := recv.TicketByID(sysID)
		if tk == nil {
			t.Errorf("after order.paid: ticket[%d] (sysID=%d) not in stub", i, sysID)
			continue
		}
		if tk.HolderStatus != 0 {
			t.Errorf("after order.paid: ticket[%d] holderStatus = %d, want 0", i, tk.HolderStatus)
		}
	}

	// ── Step 4: Cancel ticket[0] — retry simulation ──────────────────────
	cancelTicketInDB(t, pool, ticketIDs[0])
	recv.Reset() // clear order.paid events

	cancelledEv := outbox.Event{
		AggregateType: "ticket",
		AggregateID:   ticketIDs[0].String(),
		EventType:     macs.EventTicketCancelled,
		Payload: map[string]any{
			"ticket_id":  ticketIDs[0].String(),
			"session_id": sessionID.String(),
		},
		OccurredAt: time.Now().UTC(),
	}

	// Make the stub return 503 on the first delivery attempt.
	recv.SetOnceFailPath("/_wh/tickets")
	if err := disp.Dispatch(ctx, cancelledEv); err == nil {
		t.Error("want error when stub returns 503 (simulating outbox retry trigger)")
	}

	// Verify delivery count is 1 (one attempt was made).
	if cnt := recv.DeliveryCount("/_wh/tickets"); cnt != 1 {
		t.Errorf("delivery count after 503: want 1, got %d", cnt)
	}

	// Retry: stub now returns 200.
	if err := disp.Dispatch(ctx, cancelledEv); err != nil {
		t.Fatalf("Dispatch ticket.refunded (retry): %v", err)
	}

	// Verify total delivery count is 2 (two attempts total).
	if cnt := recv.DeliveryCount("/_wh/tickets"); cnt != 2 {
		t.Errorf("delivery count after retry: want 2, got %d", cnt)
	}

	refundedEvents := recv.EventsByType("ticket.refunded")
	if len(refundedEvents) != 1 {
		t.Fatalf("stub: want 1 ticket.refunded, got %d (total=%d)",
			len(refundedEvents), len(recv.Events()))
	}

	// ── Step 5: Assert holderStatus transitions ───────────────────────────
	// ticket[0] must be refunded (holderStatus=3).
	tk0 := recv.TicketByID(sysIDs[0])
	if tk0 == nil {
		t.Fatalf("ticket[0] (sysID=%d) not in stub after refund", sysIDs[0])
	}
	if tk0.HolderStatus != 3 {
		t.Errorf("ticket[0] holderStatus = %d, want 3 (refunded)", tk0.HolderStatus)
	}

	// ticket[1] and ticket[2] must remain active (holderStatus=0).
	for i := 1; i < 3; i++ {
		tk := recv.TicketByID(sysIDs[i])
		if tk == nil {
			t.Errorf("ticket[%d] (sysID=%d) not in stub", i, sysIDs[i])
			continue
		}
		if tk.HolderStatus != 0 {
			t.Errorf("ticket[%d] holderStatus = %d, want 0 (still active)", i, tk.HolderStatus)
		}
	}

	t.Logf("AB-50e three-ticket round-trip OK: sysIDs=%v; holderStatus: 3/0/0", sysIDs)
}

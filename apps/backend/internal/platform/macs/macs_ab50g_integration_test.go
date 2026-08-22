//go:build integration

// Package macs — AB-50g integration test (feature #444).
//
// This test proves the full MACS cancel round-trip through the real HTTP
// cancel handler and the real OutboxEventsDispatcher worker loop:
//
//  1. Seed 3 tickets in one session with a city-linked venue (so the MACS
//     export carries a non-empty cityName, satisfying strict stub validation).
//  2. Export all 3 tickets and import them into the stub.
//  3. Dispatch order.paid for all 3 via the MACS dispatcher.
//  4. Cancel ticket[0] via the REAL htickets.HandleCancelTicket handler
//     (not cancelTicketInDB) — verifies the full cancel pipeline.
//  5. A test callback (publishTicketCancelledEvent) writes the resulting
//     v1.ticket.cancelled event directly to outbox_events.
//  6. The REAL OutboxEventsDispatcher (PGOutboxEventStore) claims and
//     delivers the row. The stub first returns 503 → MarkFailed is called;
//     assert next_attempt_at is set and dead_lettered_at is nil.
//  7. Reset next_attempt_at to the past, run the dispatcher again → stub
//     returns 200 → MarkDispatched called; assert processed_at is set.
//  8. Assert holderStatus: ticket[0]=3, ticket[1]=0, ticket[2]=0.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	(migrated to head >= 0089)
//
// Run with:
//
//	go test -tags integration -run TestMACS_AB50g ./apps/backend/internal/platform/macs/
package macs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// TestMACS_AB50g_RealCancelRoundTrip is the AB-50g end-to-end acceptance test.
//
//	real cancel handler → outbox_events row written → OutboxEventsDispatcher →
//	MACS dispatcher → stub 503 (next_attempt_at set) →
//	retry → stub 200 → processed_at set → holderStatus 3/0/0
func TestMACS_AB50g_RealCancelRoundTrip(t *testing.T) {
	pool := roundtripPool(t) // skips when DATABASE_URL not set
	ctx := context.Background()

	const signingSecret = "ab50g-test-secret-hmac"

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
	citySlug := "macs-ab50g-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		if err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	// Seed city using the IL country from migration 0006.
	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migration 0006 not applied?): %v", err)
	}
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`,
		cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'AB50g Test City')`,
		citySlug)

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "AB50g Org "+suffix, "ab50g-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`,
		venueID, orgID, "AB50g Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "AB50g Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'CZK', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "AB50g Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, NULL, 100, 3)`,
		sessionID)
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
		mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
			checkoutIDs[i], orgID, channelID, res.ID)
		ticketIDs[i] = uuid.New()
		mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at) VALUES ($1, $2, $3, 'active', NOW())`,
			ticketIDs[i], sessionID, checkoutIDs[i])
	}

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM webhook_subscribers WHERE org_id=$1 AND kind='macs'`, orgID)
		pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id=$1::text AND event_type='v1.ticket.cancelled'`, ticketIDs[0].String())
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
		pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	// ── Get system_ticket_ids ─────────────────────────────────────────────
	var sysIDs [3]int64
	for i := 0; i < 3; i++ {
		sysIDs[i] = getSystemTicketID(t, pool, ticketIDs[i])
		if sysIDs[i] <= 0 {
			t.Fatalf("ticket[%d] system_ticket_id = %d; want > 0", i, sysIDs[i])
		}
	}

	// ── Step 1: Export and import into stub ──────────────────────────────
	export, err := macs.QueryAndBuildExport(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("QueryAndBuildExport: %v", err)
	}
	if len(export) != 3 {
		t.Fatalf("export: want 3 orders, got %d", len(export))
	}
	if export[0].TicketList[0].ActionEvent.CityName == "" {
		t.Fatal("export: cityName is empty — city seeding failed")
	}

	exportBody, _ := json.Marshal(export)
	importResp, err := http.Post(recv.ImportURL(), "application/json", bytes.NewReader(exportBody))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	importResp.Body.Close()
	if importResp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", importResp.StatusCode)
	}

	// ── Step 2: Dispatch order.paid for all 3 tickets ────────────────────
	macsDisp := macs.NewDispatcher(pool)
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
		if err := macsDisp.Dispatch(ctx, issuedEv); err != nil {
			t.Fatalf("Dispatch order.paid ticket[%d]: %v", i, err)
		}
	}

	// ── Step 3: Cancel ticket[0] via REAL HandleCancelTicket handler ─────
	//
	// publishCancelled writes v1.ticket.cancelled to outbox_events so that
	// the real OutboxEventsDispatcher (backed by PGOutboxEventStore) can
	// claim and deliver it.
	publishCancelled := func(cancelCtx context.Context, ticket gen.TicketRow, reason, refundMode string) {
		_, dbErr := pool.Exec(cancelCtx, `
			INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
			VALUES ('ticket', $1::text, 'v1.ticket.cancelled',
				jsonb_build_object(
					'ticket_id',   $1::text,
					'session_id',  $2::text,
					'reason',      $3::text,
					'refund_mode', $4::text
				),
				NOW()
			)`,
			ticket.ID.String(), ticket.SessionID.String(), reason, refundMode,
		)
		if dbErr != nil {
			t.Logf("publishCancelled: insert outbox_events failed: %v", dbErr)
		}
	}

	genQ := gen.New(pool)
	cancelHandler := htickets.New(
		genQ,           // ticketQueries
		nil,            // credentialQueries (nil-safe)
		nil,            // complimentaryQueries (not used in cancel)
		genQ,           // inventoryQueries (for RestoreSoldCapacity)
		nil,            // reservationQueries (manual mode needs none)
		nil,            // barcodeQueries (nil-safe)
		nil,            // deliveryJobQueries
		nil,            // feedTokenQueries
		nil,            // workerPool
		pool,           // pool (TxStarter)
		nil,            // audit (nil — skip for test)
		slog.Default(),
		nil,              // publishTicketIssuedEvents
		nil,              // publishTicketRevokedV1Events
		publishCancelled, // publishTicketCancelledEvent
	)

	cancelBody, _ := json.Marshal(map[string]any{
		"reason":      "AB-50g test cancellation",
		"refund_mode": "manual",
	})
	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/tickets/"+ticketIDs[0].String()+"/cancel",
		bytes.NewReader(cancelBody))
	cancelReq.Header.Set("Content-Type", "application/json")

	// Inject chi URL param and auth actor into the context.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", ticketIDs[0].String())
	cancelReq = cancelReq.WithContext(
		context.WithValue(
			auth.WithActor(cancelReq.Context(), auth.Actor{ID: "ab50g-test-actor", Type: "user"}),
			chi.RouteCtxKey, rctx,
		),
	)

	cancelW := httptest.NewRecorder()
	cancelHandler.HandleCancelTicket(cancelW, cancelReq)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("HandleCancelTicket: want 200, got %d\nBody: %s",
			cancelW.Code, cancelW.Body.String())
	}

	// Verify the ticket is now 'cancelled' in the DB.
	var ticketStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, ticketIDs[0]).Scan(&ticketStatus); err != nil {
		t.Fatalf("read ticket status: %v", err)
	}
	if ticketStatus != "cancelled" {
		t.Fatalf("ticket[0] status = %q; want 'cancelled'", ticketStatus)
	}

	// Verify exactly one outbox_events row was written by publishCancelled.
	var outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM outbox_events
		WHERE aggregate_id=$1::text AND event_type='v1.ticket.cancelled'`,
		ticketIDs[0].String(),
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox_events: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox_events: want exactly 1 v1.ticket.cancelled row, got %d", outboxCount)
	}

	// ── Step 4: OutboxEventsDispatcher — stub returns 503 ────────────────
	//
	// Key design: use context.Background() (not a cancel-able child) so that
	// calling oed.Stop() does not cancel in-flight DB queries inside MarkFailed.
	// BackoffFunc returns 1 hour so the dispatcher does not auto-retry after the
	// 503; we control the retry manually in step 5.
	store := outbox.NewPGOutboxEventStore(pool)
	oed, err := outbox.NewOutboxEventsDispatcher(outbox.OutboxEventsDispatcherOptions{
		Store:        store,
		Dispatcher:   macsDisp,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		BackoffFunc:  func(int) time.Duration { return time.Hour },
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	// Reset delivery counts accumulated during the order.paid dispatches above
	// so that DeliveryCount("/_wh/tickets") == 0 before the first dispatcher run.
	// Without this, the poll condition < 1 is immediately false and we would not
	// wait for the actual 503 delivery attempt.
	recv.Reset()
	recv.SetOnceFailPath("/_wh/tickets")
	go func() { _ = oed.Run(context.Background()) }()

	// Wait until the stub has received exactly 1 delivery attempt (the 503).
	// This is the definitive signal that dispatch ran and returned an error.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && recv.DeliveryCount("/_wh/tickets") < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if recv.DeliveryCount("/_wh/tickets") < 1 {
		_ = oed.Stop()
		t.Fatal("first dispatcher: no delivery attempt reached the stub within 5s")
	}
	// Stop() waits for Run() to exit — Run() only exits after deliverRow()
	// returns, which means MarkFailed has already committed to the DB.
	// No sleep needed: the guarantee comes from Stop() itself.
	_ = oed.Stop()

	// Assert: after MarkFailed, next_attempt_at is set and row is NOT dead-lettered.
	var nextAttemptAt *time.Time
	var deadLetteredAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at, dead_lettered_at FROM outbox_events
		WHERE aggregate_id=$1::text AND event_type='v1.ticket.cancelled'`,
		ticketIDs[0].String(),
	).Scan(&nextAttemptAt, &deadLetteredAt); err != nil {
		t.Fatalf("read outbox row after 503: %v", err)
	}
	if nextAttemptAt == nil {
		t.Error("after 503 failure: next_attempt_at should be set by MarkFailed (not nil)")
	}
	if deadLetteredAt != nil {
		t.Error("after 503 failure: dead_lettered_at should be nil (not dead-lettered yet)")
	}

	// ── Step 5: Reset next_attempt_at → retry → stub returns 200 ─────────
	//
	// The claim set next_attempt_at = now+5min; MarkFailed overwrites it to
	// now+1hour (BackoffFunc). Reset it to the past so the second dispatcher
	// can immediately claim the row.
	if _, err := pool.Exec(ctx, `
		UPDATE outbox_events
		   SET next_attempt_at = NOW() - '1 second'::interval
		 WHERE aggregate_id=$1::text AND event_type='v1.ticket.cancelled'`,
		ticketIDs[0].String(),
	); err != nil {
		t.Fatalf("reset next_attempt_at: %v", err)
	}

	oed2, err := outbox.NewOutboxEventsDispatcher(outbox.OutboxEventsDispatcherOptions{
		Store:        store,
		Dispatcher:   macsDisp,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		BackoffFunc:  func(int) time.Duration { return time.Hour },
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher2: %v", err)
	}

	go func() { _ = oed2.Run(context.Background()) }()

	// Wait until processed_at is set (successful dispatch → MarkDispatched ran).
	deadline2 := time.Now().Add(5 * time.Second)
	var processedAt *time.Time
	for time.Now().Before(deadline2) {
		if err := pool.QueryRow(ctx, `
			SELECT processed_at FROM outbox_events
			WHERE aggregate_id=$1::text AND event_type='v1.ticket.cancelled'`,
			ticketIDs[0].String(),
		).Scan(&processedAt); err != nil {
			t.Fatalf("poll processed_at: %v", err)
		}
		if processedAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = oed2.Stop()

	if processedAt == nil {
		t.Error("after successful retry: processed_at should be set by MarkDispatched")
	}

	// ── Step 6: Assert holderStatus ──────────────────────────────────────
	// ticket[0] was cancelled and the refund webhook was delivered → holderStatus=3.
	tk0 := recv.TicketByID(sysIDs[0])
	if tk0 == nil {
		t.Fatalf("ticket[0] (sysID=%d) not in stub after cancel webhook", sysIDs[0])
	}
	if tk0.HolderStatus != 3 {
		t.Errorf("ticket[0] holderStatus = %d, want 3 (refunded)", tk0.HolderStatus)
	}

	// ticket[1] and ticket[2] were imported but never cancelled → holderStatus=0.
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

	t.Logf("AB-50g real-cancel round-trip OK: sysIDs=%v; ticket[0] cancelled via real handler → outbox_events → OutboxEventsDispatcher → MACS stub; holderStatus 3/0/0",
		sysIDs)
}

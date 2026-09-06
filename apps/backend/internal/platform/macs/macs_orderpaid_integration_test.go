//go:build integration

// Package macs — W1-Ma round-trip (feature #510, spec §10 M1/M2/M5).
//
// The AB-50g round-trip proved the CANCEL half of the MACS contract. This is
// its sale half, rewritten for the order aggregate:
//
//  1. Seed ONE orders row (migration 0092) carrying 3 tickets.
//  2. Write a single v1.order.paid row to outbox_events.
//  3. Drive the REAL OutboxEventsDispatcher. The stub answers the first
//     delivery with HTTP 200 {"status":"Error"} — a BODY-level refusal. The
//     dispatcher must treat that as a failure: next_attempt_at set,
//     processed_at still nil, nothing recorded, no ticket state changed.
//  4. Reset next_attempt_at, run the dispatcher again → {"status":"OK"} →
//     processed_at set.
//  5. Exactly ONE order.paid envelope reached the stub, and it carries the
//     whole order: data.status = "PAID" and a ticketList of all 3 tickets.
//  6. A v1.scanner.ticket.issued for a ticket that BELONGS to the order
//     delivers nothing — the sale must not be announced twice.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	(migrated to head >= 0092)
//
// Run with:
//
//	go test -tags integration -run TestMACS_W1Ma ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// TestMACS_W1Ma_OrderPaidRoundTrip is the W1-Ma acceptance test: one order,
// N tickets, delivered exactly once, with a body-level Error forcing a retry.
func TestMACS_W1Ma_OrderPaidRoundTrip(t *testing.T) {
	pool := roundtripPool(t) // skips when DATABASE_URL not set
	ctx := context.Background()

	const signingSecret = "w1ma-test-secret-hmac"
	const webhookPath = "/_wh/tickets"

	recv := stub.NewWithSecret(signingSecret)
	defer recv.Close()

	// ── Seed: org → city → venue → event → session → channel ─────────────
	orgID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	cityID := uuid.New()
	orderID := uuid.New()
	checkoutID := uuid.New()
	suffix := orgID.String()[:8]
	citySlug := "macs-w1ma-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migration 0006 not applied?): %v", err)
	}
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`, cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'W1-Ma Test City')`,
		citySlug)
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "W1Ma Org "+suffix, "w1ma-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`,
		venueID, orgID, "W1Ma Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "W1Ma Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'CZK', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "W1Ma Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, NULL, 100, 3)`,
		sessionID)
	mustExec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', $1, $2, '{}', TRUE, 'macs', $3)`,
		recv.WebhookURL(), signingSecret, orgID)

	// ── Seed: ONE order aggregate with 3 tickets ─────────────────────────
	q := gen.New(pool)
	res, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, 3, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		checkoutID, orgID, channelID, res.ID)
	mustExec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id,
			reservation_id, source, status, currency, subtotal, discount, charge, total, buyer_email, paid_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid','CZK',3000,0,0,3000,'w1ma@example.com',NOW())`,
		orderID, orgID, channelID, eventID, sessionID, checkoutID, res.ID)

	var ticketIDs [3]uuid.UUID
	for i := range ticketIDs {
		ticketIDs[i] = uuid.New()
		mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, order_id, status, issued_at, ordinal, holder_email)
			VALUES ($1,$2,$3,$4,'active',NOW(),$5,'w1ma@example.com')`,
			ticketIDs[i], sessionID, checkoutID, orderID, i+1)
	}

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM webhook_subscribers WHERE org_id=$1 AND kind='macs'`, orgID)
		pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id=$1::text`, orderID.String())
		for i := range ticketIDs {
			pool.Exec(c, `DELETE FROM tickets WHERE id=$1`, ticketIDs[i])
		}
		pool.Exec(c, `DELETE FROM orders WHERE id=$1`, orderID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, checkoutID)
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

	var sysIDs [3]int64
	for i := range ticketIDs {
		sysIDs[i] = getSystemTicketID(t, pool, ticketIDs[i])
		if sysIDs[i] <= 0 {
			t.Fatalf("ticket[%d] system_ticket_id = %d; want > 0", i, sysIDs[i])
		}
	}

	// ── The outbox row the ordering layer publishes on payment ───────────
	mustExec(`INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
		VALUES ('order', $1::text, 'v1.order.paid',
			jsonb_build_object('order_id', $1::text, 'org_id', $2::text, 'session_id', $3::text),
			NOW())`,
		orderID.String(), orgID.String(), sessionID.String())

	macsDisp := macs.NewDispatcher(pool)
	store := outbox.NewPGOutboxEventStore(pool)
	newDispatcher := func() *outbox.OutboxEventsDispatcher {
		t.Helper()
		oed, derr := outbox.NewOutboxEventsDispatcher(outbox.OutboxEventsDispatcherOptions{
			Store:        store,
			Dispatcher:   macsDisp,
			PollInterval: 20 * time.Millisecond,
			MaxAttempts:  5,
			BackoffFunc:  func(int) time.Duration { return time.Hour },
		})
		if derr != nil {
			t.Fatalf("NewOutboxEventsDispatcher: %v", derr)
		}
		return oed
	}

	// ── Step 1: the stub refuses the payload at the BODY level ───────────
	//
	// HTTP 200 {"status":"Error"} — the transport succeeded, MACS did not
	// accept. The dispatcher must still report a failure so the outbox retries.
	recv.SetOnceErrorPath(webhookPath)
	oed := newDispatcher()
	go func() { _ = oed.Run(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && recv.DeliveryCount(webhookPath) < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if recv.DeliveryCount(webhookPath) < 1 {
		_ = oed.Stop()
		t.Fatal("first dispatcher: no delivery attempt reached the stub within 5s")
	}
	// Stop() waits for Run() to exit, which happens only after MarkFailed
	// committed — no sleep needed.
	_ = oed.Stop()

	var nextAttemptAt, deadLetteredAt, processedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at, dead_lettered_at, processed_at FROM outbox_events
		WHERE aggregate_id=$1::text AND event_type='v1.order.paid'`,
		orderID.String(),
	).Scan(&nextAttemptAt, &deadLetteredAt, &processedAt); err != nil {
		t.Fatalf("read outbox row after the Error ack: %v", err)
	}
	if processedAt != nil {
		t.Fatal(`a 200 {"status":"Error"} ack must NOT mark the event processed`)
	}
	if nextAttemptAt == nil {
		t.Error(`after the Error ack: next_attempt_at should be set by MarkFailed`)
	}
	if deadLetteredAt != nil {
		t.Error("after the Error ack: dead_lettered_at should be nil (not dead-lettered yet)")
	}
	// The refusal changed nothing on the receiving side.
	if got := len(recv.EventsByType("order.paid")); got != 0 {
		t.Errorf("recorded order.paid envelopes after the refusal = %d; want 0", got)
	}
	if tk := recv.TicketByID(sysIDs[0]); tk != nil {
		t.Errorf("ticket %d tracked by the stub after a refused delivery: %+v", sysIDs[0], tk)
	}

	// ── Step 2: retry → {"status":"OK"} → processed ──────────────────────
	if _, err := pool.Exec(ctx, `
		UPDATE outbox_events
		   SET next_attempt_at = NOW() - '1 second'::interval
		 WHERE aggregate_id=$1::text AND event_type='v1.order.paid'`,
		orderID.String(),
	); err != nil {
		t.Fatalf("reset next_attempt_at: %v", err)
	}

	oed2 := newDispatcher()
	go func() { _ = oed2.Run(context.Background()) }()

	deadline2 := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline2) {
		if err := pool.QueryRow(ctx, `
			SELECT processed_at FROM outbox_events
			WHERE aggregate_id=$1::text AND event_type='v1.order.paid'`,
			orderID.String(),
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
		t.Fatal(`after the OK ack: processed_at should be set by MarkDispatched`)
	}
	if got := recv.DeliveryCount(webhookPath); got != 2 {
		t.Errorf("delivery attempts = %d; want 2 (refusal + retry)", got)
	}

	// ── Step 3: ONE envelope, carrying the WHOLE order ───────────────────
	paid := recv.EventsByType("order.paid")
	if len(paid) != 1 {
		t.Fatalf("order.paid envelopes = %d; want exactly 1 — %d tickets are one sale", len(paid), len(ticketIDs))
	}
	data := paid[0].Data
	if data["status"] != "PAID" {
		t.Errorf("data.status = %v; want \"PAID\"", data["status"])
	}
	list, ok := data["ticketList"].([]any)
	if !ok {
		t.Fatalf("data.ticketList is %T; want a JSON array", data["ticketList"])
	}
	if len(list) != len(ticketIDs) {
		t.Fatalf("data.ticketList has %d entries; want %d", len(list), len(ticketIDs))
	}
	seen := map[int64]bool{}
	for i, item := range list {
		tk, isObj := item.(map[string]any)
		if !isObj {
			t.Fatalf("data.ticketList[%d] is %T; want an object", i, item)
		}
		id, isNum := tk["id"].(float64)
		if !isNum {
			t.Fatalf("data.ticketList[%d].id is %T; want a number", i, tk["id"])
		}
		seen[int64(id)] = true
		for _, field := range []string{"seatId", "barcode", "actionEvent", "orderId"} {
			if _, present := tk[field]; !present {
				t.Errorf("data.ticketList[%d] is missing the MACS-required field %q", i, field)
			}
		}
	}
	for i, sysID := range sysIDs {
		if !seen[sysID] {
			t.Errorf("ticket[%d] (sysID=%d) is missing from data.ticketList", i, sysID)
		}
		tk := recv.TicketByID(sysID)
		if tk == nil {
			t.Errorf("ticket[%d] (sysID=%d) not tracked by the stub after order.paid", i, sysID)
			continue
		}
		if tk.HolderStatus != 0 {
			t.Errorf("ticket[%d] holderStatus = %d; want 0 (valid)", i, tk.HolderStatus)
		}
	}

	// ── Step 4: an order-linked ticket.issued announces nothing ──────────
	//
	// tickets.order_id is set, so v1.order.paid already covered this sale;
	// the complimentary path must stay silent or MACS sees the sale twice.
	before := recv.DeliveryCount(webhookPath)
	issued := outbox.Event{
		AggregateType: "ticket",
		AggregateID:   ticketIDs[0].String(),
		EventType:     macs.EventTicketIssued,
		Payload: map[string]any{
			"ticket_id":           ticketIDs[0].String(),
			"session_id":          sessionID.String(),
			"checkout_session_id": checkoutID.String(),
		},
		OccurredAt: time.Now().UTC(),
	}
	if err := macsDisp.Dispatch(ctx, issued); err != nil {
		t.Fatalf("dispatch ticket.issued for an order-linked ticket: %v", err)
	}
	if got := recv.DeliveryCount(webhookPath); got != before {
		t.Errorf("delivery attempts = %d; want %d — an order-linked ticket.issued must not re-announce the sale", got, before)
	}
	if got := len(recv.EventsByType("order.paid")); got != 1 {
		t.Errorf("order.paid envelopes after the ticket.issued = %d; want 1", got)
	}

	t.Logf("W1-Ma order.paid round-trip OK: order=%s sysIDs=%v; Error ack retried, one envelope with %d tickets",
		orderID, sysIDs, len(list))
}

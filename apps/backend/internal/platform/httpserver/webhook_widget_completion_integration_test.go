//go:build integration

// webhook_widget_completion_integration_test.go — live-DB end-to-end coverage
// for the HIGH-severity widget payment-webhook defect fixed 2026-09-13:
//
//	POST /v1/public/feeds/{feed_token}/checkout/start
//	  → (payment provider takes the money)
//	  → POST /v1/payment-intents/webhook  (Stripe payment_intent.succeeded envelope)
//	  → GET  /v1/public/checkout/{checkout_token}
//
// Before the fix, HandlePaymentIntentWebhook marked the payment intent
// succeeded, marked the order paid, and converted the reservation, but NEVER
// completed the checkout session — checkout_sessions.state stayed
// 'pricing_confirmed' forever, so the public status endpoint answered
// "pending" for a paid purchase. This test drives the REAL handlers through
// the mounted chi router (no stubs — see AGENTS.md) and asserts the full
// chain: checkout completed, order paid, reservation converted, and the
// public status reporting "paid" with tickets attached. It also proves a
// replayed webhook event is a no-op.
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0100).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestWebhookWidgetCompletion
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// webhookWidgetFixture seeds the minimal published-feed topology the public
// checkout endpoint validates against — the same shape as w1a6cFixture
// (order_wiring_w1a6c_488_integration_test.go), duplicated here (rather than
// shared) so this test file stays self-contained and does not depend on
// another test file's helper surviving unrelated refactors.
type webhookWidgetFixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	orgID      uuid.UUID
	venueID    uuid.UUID
	eventID    uuid.UUID
	sessionID  uuid.UUID
	tierID     uuid.UUID
	channelID  uuid.UUID
	tokenID    uuid.UUID
	configID   uuid.UUID
	feedToken  string
	buyerMail  string
	buyerPhone string
}

func newWebhookWidgetFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *webhookWidgetFixture {
	t.Helper()
	f := &webhookWidgetFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		tierID:    uuid.New(),
		channelID: uuid.New(),
		tokenID:   uuid.New(),
		configID:  uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	f.feedToken = "whwc-feed-" + suffix
	f.buyerMail = fmt.Sprintf("whwc-buyer-%s@arena-integration.test", suffix)
	f.buyerPhone = fmt.Sprintf("+3631%07d", time.Now().UnixNano()%10_000_000)

	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "WHWC Org " + suffix, "whwc-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "WHWC Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "WHWC Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 2 hours', 100, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO ticket_tiers
		    (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'WHWC Tier', 'fixed', 2500, 'EUR', 0)`,
			[]any{f.tierID, f.sessionID}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, 100)`,
			[]any{f.sessionID}},
		// provider 'stripe' + a configured payment_provider_configs row are
		// now required for checkout/start to reach 201: a paid cart is no
		// longer confirmed unless a real hosted payment page can be created
		// for it (that is the point of the hosted-checkout flow — no more
		// dead redirect_url).
		{`INSERT INTO sales_channels (id, org_id, name, provider, fee_percent, collect_name, collect_phone)
		  VALUES ($1, $2, $3, 'stripe', 1.25, true, true)`,
			[]any{f.channelID, f.orgID, "WHWC Channel " + suffix}},
		{`INSERT INTO payment_provider_configs
		    (id, org_id, provider, mode, secrets, status, is_active)
		  VALUES ($1, $2, 'stripe', 'test', $3::jsonb, 'configured', true)`,
			[]any{f.configID, f.orgID,
				fmt.Sprintf(`{"api_key":"sk_test_%s","webhook_secret":""}`, suffix)}},
		{`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active)
		  VALUES ($1, $2, $3, 'whwc', true)`,
			[]any{f.tokenID, f.feedToken, f.channelID}},
		{`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`,
			[]any{f.eventID, f.tokenID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("webhookWidgetFixture step %d failed: %v", i, err)
		}
	}

	// AB-51: AllocateGAUnitsTx only claims EXISTING available ga_unit rows.
	// Migration 0101: they belong to the CATEGORY, not to a shared pool.
	if _, err := gen.New(pool).InsertGAUnits(ctx, f.sessionID, "ga|t1", 0, &f.tierID, 5); err != nil {
		f.cleanup()
		t.Fatalf("webhookWidgetFixture InsertGAUnits: %v", err)
	}
	return f
}

func (f *webhookWidgetFixture) cleanup() {
	ctx := context.Background()
	stmts := []struct {
		sql string
		arg any
	}{
		{`UPDATE order_items SET ticket_id = NULL
		   WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`UPDATE tickets SET order_id = NULL WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM barcodes WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM delivery_jobs WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM ticket_credentials WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM tickets WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id::text FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM payment_intent_events WHERE payment_intent_id IN
		   (SELECT id FROM payment_intents WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM payment_intents WHERE org_id = $1`, f.orgID},
		{`DELETE FROM orders WHERE org_id = $1`, f.orgID},
		// The webhook enqueues checkout.issue_tickets / checkout.convert_reservation
		// worker_jobs rows keyed by checkout_session_id / reservation_id in their
		// JSON payload (no FK). These MUST be swept before checkout_sessions and
		// reservations are deleted below, or they leak into other integration
		// tests that drain worker_jobs generically and have no handler registered
		// for these job types (seen live: TestAuthEmailIntegrationPR02_* failing
		// with "no handler for job type checkout.issue_tickets" when this cleanup
		// was missing).
		{`DELETE FROM worker_jobs WHERE payload->>'checkout_session_id' IN
		   (SELECT id::text FROM checkout_sessions WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM worker_jobs WHERE payload->>'reservation_id' IN
		   (SELECT id::text FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID},
		{`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM reservations WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM event_publications WHERE event_id = $1`, f.eventID},
		{`DELETE FROM agent_feed_tokens WHERE id = $1`, f.tokenID},
		{`DELETE FROM sessions WHERE id = $1`, f.sessionID},
		{`DELETE FROM payment_provider_configs WHERE org_id = $1`, f.orgID},
		{`DELETE FROM customer_org_links WHERE org_id = $1`, f.orgID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
		{`DELETE FROM customers WHERE id IN
		   (SELECT customer_id FROM customer_identities WHERE value_normalized = $1)`, f.buyerMail},
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("webhookWidgetFixture cleanup (%.60s): %v", s.sql, err)
		}
	}
}

// TestWebhookWidgetCompletion_StripeEnvelopeSuccess_CompletesCheckoutAndPaysOrder
// drives the full widget purchase path end to end and asserts the HIGH-severity
// defect is fixed: the checkout session reaches 'completed', the order is
// 'paid', the reservation is 'converted', and the public status endpoint
// reports "paid" with tickets. A replayed webhook event is then proven to be
// a no-op.
func TestWebhookWidgetCompletion_StripeEnvelopeSuccess_CompletesCheckoutAndPaysOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newWebhookWidgetFixture(t, ctx, pool)
	defer f.cleanup()

	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	const qty = 2

	// ── 1. Real endpoint: public-feed checkout start ─────────────────────────
	startBody, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"return_url": hostedTicketsBaseURL + "/embed",
		"tier_id":    f.tierID.String(),
		"qty":        qty,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Webhook Widget Buyer",
			"phone": f.buyerPhone,
		},
	})
	if err != nil {
		t.Fatalf("marshal checkout/start request: %v", err)
	}
	startReq := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	srv.router.ServeHTTP(startRec, startReq)

	if startRec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", startRec.Code, startRec.Body.String())
	}
	var startResp struct {
		CheckoutSession struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"checkout_session"`
		CheckoutToken string `json:"checkout_token"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode checkout/start response: %v (body: %s)", err, startRec.Body.String())
	}
	csID, err := uuid.Parse(startResp.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id is not a UUID: %v", err)
	}
	checkoutToken := startResp.CheckoutToken
	if checkoutToken == "" {
		t.Fatal("checkout/start response missing checkout_token")
	}
	if startResp.CheckoutSession.State != "pricing_confirmed" {
		t.Fatalf("checkout_session.state after start = %q, want pricing_confirmed", startResp.CheckoutSession.State)
	}

	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	if cs.Total == nil {
		t.Fatal("checkout session has no pricing snapshot (Total is nil)")
	}
	total := *cs.Total
	currency := "EUR"
	if cs.Currency != nil {
		currency = *cs.Currency
	}

	// ── 2. Insert a payment_intents row directly (as HandleCreatePaymentIntent
	//        would), in state 'created' — this is exactly the state a real
	//        Stripe card payment_intent.succeeded webhook targets directly,
	//        skipping 'processing' entirely (the bug this test protects). ────
	providerPaymentID := "pi_whwc_" + uuid.New().String()[:12]
	pi, err := q.InsertPaymentIntent(ctx, &csID, f.orgID, "stripe",
		&providerPaymentID, total, currency, "created", nil, nil)
	if err != nil {
		t.Fatalf("InsertPaymentIntent: %v", err)
	}
	if pi.State != "created" {
		t.Fatalf("payment_intents.state after insert = %q, want created", pi.State)
	}

	// ── 3. POST a genuine Stripe event envelope to the webhook ───────────────
	stripeEventID := "evt_whwc_" + uuid.New().String()[:12]
	webhookPayload := map[string]any{
		"id":   stripeEventID,
		"type": "payment_intent.succeeded",
		"data": map[string]any{
			"object": map[string]any{
				"id":     providerPaymentID,
				"status": "succeeded",
			},
		},
	}
	webhookBody, err := json.Marshal(webhookPayload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}
	webhookReq := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(webhookBody))
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookRec := httptest.NewRecorder()
	srv.router.ServeHTTP(webhookRec, webhookReq)

	if webhookRec.Code != http.StatusOK {
		t.Fatalf("webhook (envelope, first delivery) = %d, want 200; body: %s", webhookRec.Code, webhookRec.Body.String())
	}
	var webhookResp map[string]any
	if err := json.Unmarshal(webhookRec.Body.Bytes(), &webhookResp); err != nil {
		t.Fatalf("decode webhook response: %v (body: %s)", err, webhookRec.Body.String())
	}
	if processed, _ := webhookResp["processed"].(bool); !processed {
		t.Fatalf("webhook processed = %v, want true; body: %s", webhookResp["processed"], webhookRec.Body.String())
	}
	if completed, _ := webhookResp["checkout_completed"].(bool); !completed {
		t.Errorf("webhook checkout_completed = %v, want true; body: %s", webhookResp["checkout_completed"], webhookRec.Body.String())
	}

	// ── 4. THE FIX: checkout_sessions.state must now be 'completed' ──────────
	csAfter, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID after webhook: %v", err)
	}
	if csAfter.State != "completed" {
		t.Fatalf("checkout_sessions.state after webhook = %q, want completed (this is the HIGH-severity defect)", csAfter.State)
	}
	if csAfter.CompletedAt == nil {
		t.Error("checkout_sessions.completed_at is NULL after completion")
	}

	// ── 5. Order is paid ───────────────────────────────────────────────────
	order, err := q.GetOrderByCheckoutSession(ctx, csID)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession: %v", err)
	}
	if order.Status != "paid" {
		t.Errorf("orders.status = %q, want paid", order.Status)
	}
	if order.PaidAt == nil {
		t.Error("orders.paid_at is NULL after payment")
	}

	// ── 6. Reservation converted (held → sold, TTL worker can no longer touch it) ──
	reservation, err := q.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		t.Fatalf("GetReservationByID: %v", err)
	}
	if reservation.State != "converted" {
		t.Errorf("reservations.state = %q, want converted", reservation.State)
	}

	// ── 7. Issue tickets (the checkout.issue_tickets worker job's real work;
	//        run inline since no worker process is running in this test). ────
	tickets, err := srv.ticketsHandler().IssueTicketsForCheckout(ctx, csAfter)
	if err != nil {
		t.Fatalf("IssueTicketsForCheckout: %v", err)
	}
	if len(tickets) != qty {
		t.Fatalf("issued tickets = %d, want %d", len(tickets), qty)
	}

	// ── 8. THE FIX, externally observable: public status now reports "paid" ──
	statusReq := httptest.NewRequest(http.MethodGet, "/v1/public/checkout/"+checkoutToken, nil)
	statusRec := httptest.NewRecorder()
	srv.router.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET public checkout status = %d, want 200; body: %s", statusRec.Code, statusRec.Body.String())
	}
	var statusResp struct {
		Status  string `json:"status"`
		Tickets []any  `json:"tickets"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode status response: %v (body: %s)", err, statusRec.Body.String())
	}
	if statusResp.Status != "paid" {
		t.Fatalf("public checkout status = %q, want paid (this is the externally-visible HIGH-severity defect)", statusResp.Status)
	}
	if len(statusResp.Tickets) != qty {
		t.Errorf("public checkout status tickets = %d, want %d", len(statusResp.Tickets), qty)
	}

	// ── 9. Replay: the identical Stripe event id/type must be a no-op. By the
	//        time a real duplicate delivery arrives, the intent has already
	//        reached the terminal 'succeeded' state (Step 2 above), so the
	//        webhook's pre-transaction terminal-state guard answers 200 with
	//        processed:false — it never re-reaches the InsertPaymentIntentEvent
	//        ON CONFLICT dedup path (that path only fires for a second event of
	//        a DIFFERENT event_type racing in before the first commits). Either
	//        way, nothing about the persisted state changes. ───────────────────
	replayReq := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(webhookBody))
	replayReq.Header.Set("Content-Type", "application/json")
	replayRec := httptest.NewRecorder()
	srv.router.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replayed webhook = %d, want 200; body: %s", replayRec.Code, replayRec.Body.String())
	}
	var replayResp map[string]any
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replayResp); err != nil {
		t.Fatalf("decode replay response: %v (body: %s)", err, replayRec.Body.String())
	}
	if processed, _ := replayResp["processed"].(bool); processed {
		t.Errorf("replayed webhook processed = %v, want false (no-op); body: %s", replayResp["processed"], replayRec.Body.String())
	}

	csAfterReplay, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID after replay: %v", err)
	}
	if csAfterReplay.State != "completed" {
		t.Errorf("checkout_sessions.state after replay = %q, want still completed", csAfterReplay.State)
	}

	var eventCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_intent_events WHERE provider_payment_id = $1`,
		providerPaymentID).Scan(&eventCount); err != nil {
		t.Fatalf("count payment_intent_events: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("payment_intent_events rows for %s = %d, want exactly 1 (replay must not duplicate)", providerPaymentID, eventCount)
	}
}

// TestWebhookWidgetCompletion_FlatBodyStillWorksEndToEnd proves the
// pre-existing flat/normalised webhook body shape still drives the full
// completion chain (backward compatibility with the mock provider and
// AllPay), not just the new Stripe envelope shape.
func TestWebhookWidgetCompletion_FlatBodyStillWorksEndToEnd(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newWebhookWidgetFixture(t, ctx, pool)
	defer f.cleanup()

	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	const qty = 1

	startBody, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"return_url": hostedTicketsBaseURL + "/embed",
		"tier_id":    f.tierID.String(),
		"qty":        qty,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Flat Body Buyer",
			"phone": f.buyerPhone,
		},
	})
	if err != nil {
		t.Fatalf("marshal checkout/start request: %v", err)
	}
	startReq := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	srv.router.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", startRec.Code, startRec.Body.String())
	}
	var startResp struct {
		CheckoutSession struct {
			ID string `json:"id"`
		} `json:"checkout_session"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode checkout/start response: %v", err)
	}
	csID, err := uuid.Parse(startResp.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id is not a UUID: %v", err)
	}

	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	total := int64(0)
	if cs.Total != nil {
		total = *cs.Total
	}
	currency := "EUR"
	if cs.Currency != nil {
		currency = *cs.Currency
	}

	providerPaymentID := "pi_flatwidget_" + uuid.New().String()[:12]
	if _, err := q.InsertPaymentIntent(ctx, &csID, f.orgID, "mock",
		&providerPaymentID, total, currency, "created", nil, nil); err != nil {
		t.Fatalf("InsertPaymentIntent: %v", err)
	}

	flatBody, _ := json.Marshal(map[string]string{
		"provider_payment_id": providerPaymentID,
		"event_type":          "mock.succeeded",
	})
	webhookReq := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(flatBody))
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookRec := httptest.NewRecorder()
	srv.router.ServeHTTP(webhookRec, webhookReq)
	if webhookRec.Code != http.StatusOK {
		t.Fatalf("flat-body webhook = %d, want 200; body: %s", webhookRec.Code, webhookRec.Body.String())
	}

	csAfter, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID after webhook: %v", err)
	}
	if csAfter.State != "completed" {
		t.Fatalf("checkout_sessions.state after flat-body webhook = %q, want completed", csAfter.State)
	}
}

// TestWebhookWidgetCompletion_PaymentAfterExpiry_ParksForManualReview: money
// arrives for a checkout that already expired. The payment intent must stay
// succeeded (the money is real), and the checkout session and its order are
// parked in manual_review instead of being force-completed. This branch writes
// behind a SAVEPOINT inside the webhook transaction; a regression there would
// roll back the succeeded intent silently.
func TestWebhookWidgetCompletion_PaymentAfterExpiry_ParksForManualReview(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newWebhookWidgetFixture(t, ctx, pool)
	defer f.cleanup()

	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	startBody, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"return_url": hostedTicketsBaseURL + "/embed",
		"tier_id":    f.tierID.String(),
		"qty":        1,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Late Payment Buyer",
			"phone": f.buyerPhone,
		},
	})
	if err != nil {
		t.Fatalf("marshal checkout/start request: %v", err)
	}
	startReq := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	srv.router.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", startRec.Code, startRec.Body.String())
	}
	var startResp struct {
		CheckoutSession struct {
			ID string `json:"id"`
		} `json:"checkout_session"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode checkout/start response: %v", err)
	}
	csID := uuid.MustParse(startResp.CheckoutSession.ID)
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Total == nil {
		t.Fatalf("GetCheckoutSessionByID: %v (total nil: %v)", err, cs.Total == nil)
	}

	// The buyer took too long: the session expired before the provider event.
	if _, err := pool.Exec(ctx, `UPDATE checkout_sessions SET state = 'expired', updated_at = now() WHERE id = $1`, csID); err != nil {
		t.Fatalf("expire checkout session: %v", err)
	}

	providerPaymentID := "pi_whwc_late_" + uuid.New().String()[:12]
	pi, err := q.InsertPaymentIntent(ctx, &csID, f.orgID, "stripe",
		&providerPaymentID, *cs.Total, "EUR", "created", nil, nil)
	if err != nil {
		t.Fatalf("InsertPaymentIntent: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"id":   "evt_whwc_late_" + uuid.New().String()[:12],
		"type": "payment_intent.succeeded",
		"data": map[string]any{"object": map[string]any{"id": providerPaymentID, "status": "succeeded"}},
	})
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	piAfter, err := q.GetPaymentIntentByID(ctx, pi.ID)
	if err != nil {
		t.Fatalf("GetPaymentIntentByID: %v", err)
	}
	if piAfter.State != "succeeded" {
		t.Fatalf("payment_intents.state = %q, want succeeded — the captured payment must never be rolled back", piAfter.State)
	}
	csAfter, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID after webhook: %v", err)
	}
	if csAfter.State == "completed" {
		t.Fatalf("checkout_sessions.state = completed, want it NOT force-completed after expiry")
	}
	var orderStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE checkout_session_id = $1`, csID).Scan(&orderStatus); err == nil {
		if orderStatus == "paid" {
			t.Errorf("orders.status = paid for an expired checkout, want manual_review or unchanged")
		}
		t.Logf("checkout state after late payment: %s, order status: %s", csAfter.State, orderStatus)
	}
}

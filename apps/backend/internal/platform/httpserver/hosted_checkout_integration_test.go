//go:build integration

// hosted_checkout_integration_test.go — live-DB, real-router coverage of the
// widget's Stripe-hosted payment flow, end to end:
//
//	POST /v1/public/feeds/{feed_token}/checkout/start   (→ stub Stripe)
//	  → payment_intents row keyed by the cs_… session id + a real redirect_url
//	  → POST /v1/payment-intents/webhook                (checkout.session.completed,
//	                                                     signed with the ORG's secret)
//	  → GET  /v1/public/checkout/{checkout_token}       (→ "paid", tickets enqueued)
//
// Before this flow existed, checkout/start answered a hardcoded dead
// redirect_url ("/checkout/<uuid>"), no provider was ever called, and the
// widget could not take a single payment.
//
// The second test is the one that matters for tomorrow's launch: TWO
// organizations, each with their OWN Stripe account and their OWN webhook
// signing secret. A webhook signed with org A's secret must be accepted for
// org A's payment and REJECTED for org B's. Until webhookSecretsFromOrgConfig
// learned to resolve the org from a provider event envelope, per-org secrets
// were never consulted at all and at most one organizer could have worked.
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0103).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestHostedCheckout
package httpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const hostedTicketsBaseURL = "https://tickets.arena-integration.test"

// ─────────────────────────────────────────────────────────────────────────────
// Stub Stripe
// ─────────────────────────────────────────────────────────────────────────────

// stubStripe answers POST /v1/checkout/sessions with a fresh cs_… session per
// call and records the form it was sent, so a test can assert the wire shape
// the buyer's payment actually depended on.
type stubStripe struct {
	server   *httptest.Server
	sessions []url.Values
}

func newStubStripe(t *testing.T) *stubStripe {
	t.Helper()
	s := &stubStripe{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/checkout/sessions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("stub stripe: body is not form-encoded: %v", err)
		}
		s.sessions = append(s.sessions, form)
		id := "cs_test_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:20]
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q,"url":"https://checkout.stripe.test/c/pay/%s","payment_intent":null,"expires_at":%d}`,
			id, id, time.Now().Add(31*time.Minute).Unix())
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubStripe) baseURL() string { return s.server.URL + "/v1" }

func (s *stubStripe) lastForm(t *testing.T) url.Values {
	t.Helper()
	if len(s.sessions) == 0 {
		t.Fatal("stub stripe received no checkout-session requests")
	}
	return s.sessions[len(s.sessions)-1]
}

// buildHostedCheckoutServer wires a real Server with the stub Stripe endpoint
// and a configured tickets base URL, so checkout/start can complete without
// reaching the internet.
func buildHostedCheckoutServer(t *testing.T, pool *pgxpool.Pool, stripeBase string) *Server {
	t.Helper()
	return New(Options{
		Config: &config.Config{
			AppEnv:               config.EnvDevelopment,
			AppName:              "test",
			AppVersion:           "0.0.0-dev",
			RequestTimeout:       10 * time.Second,
			BodyLimitBytes:       1 << 20,
			JWTSecretStub:        "integration-test-secret",
			DefaultLocale:        "en",
			ActiveLocales:        []string{"en"},
			CORSAllowedOrigins:   []string{hostedTicketsBaseURL},
			PublicTicketsBaseURL: hostedTicketsBaseURL,
			// Deliberately shorter than production so the assertions on the
			// hold expiry are unambiguous, but still above Stripe's own
			// 30-minute floor for a Checkout Session expiry.
			WidgetPaymentWindowSeconds: 1860,
			WidgetPaymentGraceSeconds:  120,
		},
		Pool:             pool,
		PgxPool:          pool,
		StripeAPIBaseURL: stripeBase,
	})
}

// signStripe builds the real Stripe-Signature header Stripe sends:
// "t=<unix>,v1=<hex HMAC-SHA256 of '<t>.<raw body>'>".
func signStripe(secret string, body []byte) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// enableStripeForChannel makes an existing fixture's sales channel able to
// take money: it points the channel at the stripe provider and gives the org
// its own (stub-backed) Stripe credentials.
//
// Since the hosted-checkout flow landed, a checkout/start for a cart with a
// total above zero is only confirmed when a real hosted payment page can be
// created for it — the endpoint no longer hands out a dead redirect_url. Any
// pre-existing integration test that drives a PAID public checkout therefore
// needs this.
//
// It returns its own teardown, which the caller must defer AFTER the
// fixture's (defers are LIFO, so it then runs BEFORE the fixture deletes the
// organization this row references):
//
//	f := newSomeFixture(...)
//	defer f.cleanup()
//	defer enableStripeForChannel(t, ctx, pool, f.orgID, f.channelID)()
func enableStripeForChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, channelID uuid.UUID) func() {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE sales_channels SET provider = 'stripe' WHERE id = $1`, channelID); err != nil {
		t.Fatalf("enableStripeForChannel: set provider: %v", err)
	}
	secrets := fmt.Sprintf(`{"api_key":"sk_test_%s","webhook_secret":""}`, orgID.String()[:8])
	if _, err := pool.Exec(ctx,
		`INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active)
		 VALUES ($1, 'stripe', 'test', $2::jsonb, 'configured', true)`, orgID, secrets); err != nil {
		t.Fatalf("enableStripeForChannel: insert payment config: %v", err)
	}
	return func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM payment_provider_configs WHERE org_id = $1`, orgID); err != nil {
			t.Logf("enableStripeForChannel cleanup: %v", err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixture
// ─────────────────────────────────────────────────────────────────────────────

// hostedOrgFixture is one organizer: their own org, event, GA session, sales
// channel on provider 'stripe', feed token — and critically their OWN Stripe
// api_key and webhook signing secret in payment_provider_configs.
type hostedOrgFixture struct {
	t             *testing.T
	pool          *pgxpool.Pool
	orgID         uuid.UUID
	venueID       uuid.UUID
	eventID       uuid.UUID
	sessionID     uuid.UUID
	tierID        uuid.UUID
	channelID     uuid.UUID
	tokenID       uuid.UUID
	configID      uuid.UUID
	feedToken     string
	webhookSecret string
	buyerMail     string
	buyerPhone    string
}

func newHostedOrgFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) *hostedOrgFixture {
	t.Helper()
	f := &hostedOrgFixture{
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
	// Every globally-unique literal is derived from a per-run UUID:
	// customer_identities_strong_uq and payment_intents_provider_payment_id
	// are BOTH global indexes, so a fixed literal collides with leftovers
	// from an interrupted earlier run against the shared dev stand.
	suffix := f.orgID.String()[:8]
	f.feedToken = "hoco-" + label + "-" + suffix
	f.webhookSecret = "whsec_hoco_" + label + "_" + suffix
	f.buyerMail = fmt.Sprintf("hoco-%s-%s@arena-integration.test", label, suffix)
	f.buyerPhone = fmt.Sprintf("+3632%07d", time.Now().UnixNano()%10_000_000)

	secrets := fmt.Sprintf(`{"api_key":"sk_test_%s","webhook_secret":%q}`, suffix, f.webhookSecret)

	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug, kyb_status) VALUES ($1, $2, $3, 'verified')`,
			[]any{f.orgID, "HOCO Org " + suffix, "hoco-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "HOCO Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "HOCO Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 2 hours', 100, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO ticket_tiers
		    (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'HOCO Tier', 'fixed', 2500, 'EUR', 0)`,
			[]any{f.tierID, f.sessionID}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, 100)`,
			[]any{f.sessionID}},
		// provider = 'stripe' is what makes checkout/start route here.
		{`INSERT INTO sales_channels (id, org_id, name, provider, fee_percent, collect_name, collect_phone)
		  VALUES ($1, $2, $3, 'stripe', 1.25, true, true)`,
			[]any{f.channelID, f.orgID, "HOCO Channel " + suffix}},
		{`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active)
		  VALUES ($1, $2, $3, 'hoco', true)`,
			[]any{f.tokenID, f.feedToken, f.channelID}},
		{`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`,
			[]any{f.eventID, f.tokenID}},
		// THE point of this wave: the organizer's own Stripe account.
		{`INSERT INTO payment_provider_configs
		    (id, org_id, provider, mode, secrets, status, is_active)
		  VALUES ($1, $2, 'stripe', 'test', $3::jsonb, 'configured', true)`,
			[]any{f.configID, f.orgID, secrets}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("hostedOrgFixture(%s) step %d failed: %v", label, i, err)
		}
	}

	// Migration 0101: a GA place belongs to its CATEGORY. A NULL-tier pool
	// unit is unsellable and the session would answer "sold out".
	if _, err := gen.New(pool).InsertGAUnits(ctx, f.sessionID, "ga|t1", 0, &f.tierID, 5); err != nil {
		f.cleanup()
		t.Fatalf("hostedOrgFixture(%s) InsertGAUnits: %v", label, err)
	}
	return f
}

func (f *hostedOrgFixture) cleanup() {
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
		// worker_jobs rows are keyed only by their JSON payload — no FK — so
		// they MUST be swept BEFORE their parents are deleted, or they leak
		// into any other integration test that drains worker_jobs generically
		// and then dies with "no handler for job type checkout.issue_tickets"
		// (AGENTS.md).
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
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM customer_org_links WHERE org_id = $1`, f.orgID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
		{`DELETE FROM customers WHERE id IN
		   (SELECT customer_id FROM customer_identities WHERE value_normalized = $1)`, f.buyerMail},
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("hostedOrgFixture cleanup (%.60s): %v", s.sql, err)
		}
	}
}

// startCheckout drives the real public checkout/start endpoint.
func (f *hostedOrgFixture) startCheckout(t *testing.T, srv *Server, returnURL string) (code int, body []byte) {
	t.Helper()
	payload := map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        2,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Hosted Checkout Buyer",
			"phone": f.buyerPhone,
		},
	}
	if returnURL != "" {
		payload["return_url"] = returnURL
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal checkout/start request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

type hostedStartResponse struct {
	CheckoutSession struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"checkout_session"`
	RedirectURL   string `json:"redirect_url"`
	CheckoutToken string `json:"checkout_token"`
	ExpiresAt     string `json:"expires_at"`
}

// sessionCompletedEvent builds a genuine Stripe checkout.session.completed
// envelope for the given cs_ id.
func sessionCompletedEvent(sessionID, paymentIntentID, paymentStatus string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"id":   "evt_hoco_" + uuid.New().String()[:12],
		"type": "checkout.session.completed",
		"data": map[string]any{
			"object": map[string]any{
				"id":             sessionID,
				"object":         "checkout.session",
				"payment_status": paymentStatus,
				"status":         "complete",
				"payment_intent": paymentIntentID,
			},
		},
	})
	return raw
}

func postSignedWebhook(t *testing.T, srv *Server, body []byte, secret string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signStripe(secret, body))
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestHostedCheckout_StartCreatesStripeSessionAndWebhookPaysTheOrder is the
// whole widget purchase, through the real router and real handlers, with only
// Stripe itself stubbed.
func TestHostedCheckout_StartCreatesStripeSessionAndWebhookPaysTheOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "a")
	defer f.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	q := gen.New(pool)

	// ── 1. checkout/start → a real hosted page ───────────────────────────────
	returnURL := hostedTicketsBaseURL + "/shows/summer"
	code, body := f.startCheckout(t, srv, returnURL)
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode checkout/start response: %v (body: %s)", err, body)
	}
	if !strings.HasPrefix(start.RedirectURL, "https://checkout.stripe.test/") {
		t.Fatalf("redirect_url = %q; want the provider-hosted page, not a dead internal path", start.RedirectURL)
	}
	csID, err := uuid.Parse(start.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id is not a UUID: %v", err)
	}

	// ── 2. The Stripe request carried what the buyer's payment depends on ────
	form := stripe.lastForm(t)
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	if cs.Total == nil {
		t.Fatal("checkout session has no pricing snapshot")
	}
	if got, want := form.Get("line_items[0][price_data][unit_amount]"), strconv.FormatInt(*cs.Total, 10); got != want {
		t.Errorf("stripe unit_amount = %q; want the platform-computed total %q", got, want)
	}
	if got := form.Get("line_items[0][price_data][currency]"); got != "eur" {
		t.Errorf("stripe currency = %q; want eur", got)
	}
	if got := form.Get("client_reference_id"); got != csID.String() {
		t.Errorf("stripe client_reference_id = %q; want the arena checkout session id %q", got, csID)
	}
	if got := form.Get("customer_email"); got != f.buyerMail {
		t.Errorf("stripe customer_email = %q; want %q", got, f.buyerMail)
	}
	// Both return URLs carry arena's checkout_token on the buyer's own page,
	// so whichever way they come back the widget resumes this order.
	wantReturn := returnURL + "?checkout_token=" + start.CheckoutToken
	if got := form.Get("success_url"); got != wantReturn {
		t.Errorf("stripe success_url = %q; want %q", got, wantReturn)
	}
	if got := form.Get("cancel_url"); got != wantReturn {
		t.Errorf("stripe cancel_url = %q; want %q", got, wantReturn)
	}
	if got := form.Get("metadata[arena_checkout_session_id]"); got != csID.String() {
		t.Errorf("stripe metadata[arena_checkout_session_id] = %q; want %q", got, csID)
	}
	if got := form.Get("metadata[arena_order_id]"); got == "" {
		t.Error("stripe metadata[arena_order_id] is empty; a dispute could not be traced to an order")
	}

	// ── 3. The payment window: Stripe dies BEFORE arena releases the seats ───
	stripeExpiry, err := strconv.ParseInt(form.Get("expires_at"), 10, 64)
	if err != nil {
		t.Fatalf("stripe expires_at is not a unix timestamp: %v", err)
	}
	if stripeExpiry < time.Now().Add(30*time.Minute).Unix() {
		t.Errorf("stripe expires_at is less than 30 minutes out; Stripe refuses such a session")
	}
	holdExpiry, err := time.Parse(time.RFC3339, start.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at is not RFC3339: %v", err)
	}
	if !holdExpiry.After(time.Unix(stripeExpiry, 0)) {
		t.Errorf("hold expires_at %s is not AFTER the Stripe session expiry %s; a buyer could pay for released seats",
			holdExpiry, time.Unix(stripeExpiry, 0))
	}
	reservation, err := q.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		t.Fatalf("GetReservationByID: %v", err)
	}
	if reservation.ExpiresAt.Unix() != holdExpiry.Unix() {
		t.Errorf("reservations.expires_at = %s; want it equal to the returned expires_at %s",
			reservation.ExpiresAt, holdExpiry)
	}
	order, err := q.GetOrderByCheckoutSession(ctx, csID)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession: %v", err)
	}
	if order.ExpiresAt == nil || order.ExpiresAt.Unix() != reservation.ExpiresAt.Unix() {
		t.Errorf("orders.expires_at = %v; want it copied from the reservation %s", order.ExpiresAt, reservation.ExpiresAt)
	}

	// ── 4. The payment_intents row is keyed by the cs_ id ────────────────────
	intents, err := q.ListPaymentIntentsByCheckout(ctx, csID)
	if err != nil {
		t.Fatalf("ListPaymentIntentsByCheckout: %v", err)
	}
	if len(intents) != 1 {
		t.Fatalf("payment_intents for this checkout = %d, want exactly 1", len(intents))
	}
	pi := intents[0]
	if pi.ProviderPaymentID == nil || !strings.HasPrefix(*pi.ProviderPaymentID, "cs_") {
		t.Fatalf("provider_payment_id = %v; want the cs_ hosted session id, which is what the webhook is keyed by", pi.ProviderPaymentID)
	}
	if pi.State != "created" {
		t.Errorf("payment_intents.state = %q; want created", pi.State)
	}
	if pi.HostedCheckoutURL == nil || *pi.HostedCheckoutURL != start.RedirectURL {
		t.Errorf("hosted_checkout_url = %v; want the redirect url %q", pi.HostedCheckoutURL, start.RedirectURL)
	}
	if pi.Amount != *cs.Total {
		t.Errorf("payment_intents.amount = %d; want the checkout total %d", pi.Amount, *cs.Total)
	}

	// ── 5. Still pending, and the buyer can get back to the payment page ─────
	status := f.getStatus(t, srv, start.CheckoutToken)
	if status.Status != "pending" {
		t.Errorf("status before payment = %q; want pending", status.Status)
	}
	if status.PaymentURL == nil || *status.PaymentURL != start.RedirectURL {
		t.Errorf("payment_url = %v; want the hosted page %q so a buyer who bounced can return",
			status.PaymentURL, start.RedirectURL)
	}

	// ── 6. An UNPAID checkout.session.completed must change nothing ──────────
	sessionID := *pi.ProviderPaymentID
	unpaid := sessionCompletedEvent(sessionID, "pi_hoco_unpaid", "unpaid")
	code, body = postSignedWebhook(t, srv, unpaid, f.webhookSecret)
	if code != http.StatusOK {
		t.Fatalf("unpaid webhook = %d, want 200; body: %s", code, body)
	}
	var unpaidResp map[string]any
	_ = json.Unmarshal(body, &unpaidResp)
	if processed, _ := unpaidResp["processed"].(bool); processed {
		t.Fatalf("an unpaid checkout.session.completed was PROCESSED; tickets would ship for money that never settled. body: %s", body)
	}
	if after, err := q.GetPaymentIntentByProviderID(ctx, sessionID); err == nil && after.State != "created" {
		t.Errorf("payment intent advanced to %q on an unpaid session", after.State)
	}

	// ── 7. The real thing: a PAID session, signed with the ORG's secret ──────
	chargeRef := "pi_hoco_" + uuid.New().String()[:12]
	paid := sessionCompletedEvent(sessionID, chargeRef, "paid")
	code, body = postSignedWebhook(t, srv, paid, f.webhookSecret)
	if code != http.StatusOK {
		t.Fatalf("paid webhook = %d, want 200; body: %s", code, body)
	}
	var paidResp map[string]any
	if err := json.Unmarshal(body, &paidResp); err != nil {
		t.Fatalf("decode webhook response: %v (body: %s)", err, body)
	}
	if processed, _ := paidResp["processed"].(bool); !processed {
		t.Fatalf("paid webhook processed = %v, want true; body: %s", paidResp["processed"], body)
	}
	if completed, _ := paidResp["checkout_completed"].(bool); !completed {
		t.Fatalf("paid webhook checkout_completed = %v, want true; body: %s", paidResp["checkout_completed"], body)
	}

	// ── 8. The pi_ was learned and stored for refunds ────────────────────────
	piAfter, err := q.GetPaymentIntentByProviderID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetPaymentIntentByProviderID after payment: %v", err)
	}
	if piAfter.State != "succeeded" {
		t.Errorf("payment_intents.state = %q; want succeeded", piAfter.State)
	}
	if piAfter.ProviderChargeRef == nil || *piAfter.ProviderChargeRef != chargeRef {
		t.Errorf("provider_charge_ref = %v; want %q — a refund cannot be driven through the cs_ id",
			piAfter.ProviderChargeRef, chargeRef)
	}

	// ── 9. Checkout completed, order paid, tickets enqueued ──────────────────
	csAfter, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID after payment: %v", err)
	}
	if csAfter.State != "completed" {
		t.Fatalf("checkout_sessions.state = %q; want completed", csAfter.State)
	}
	orderAfter, err := q.GetOrderByCheckoutSession(ctx, csID)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession after payment: %v", err)
	}
	if orderAfter.Status != "paid" {
		t.Errorf("orders.status = %q; want paid", orderAfter.Status)
	}

	var issueJobs int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs
		  WHERE job_type = 'checkout.issue_tickets'
		    AND payload->>'checkout_session_id' = $1`, csID.String()).Scan(&issueJobs); err != nil {
		t.Fatalf("count checkout.issue_tickets jobs: %v", err)
	}
	if issueJobs != 1 {
		t.Errorf("checkout.issue_tickets jobs = %d; want exactly 1", issueJobs)
	}

	// ── 10. The public status the widget polls now says paid, and stops
	//        offering a payment page for an order that is already settled ─────
	statusAfter := f.getStatus(t, srv, start.CheckoutToken)
	if statusAfter.Status != "paid" {
		t.Errorf("public status after payment = %q; want paid", statusAfter.Status)
	}
	if statusAfter.PaymentURL != nil {
		t.Errorf("payment_url = %q on a PAID order; it must be omitted", *statusAfter.PaymentURL)
	}
}

// TestHostedCheckout_TwoOrgsUseTheirOwnWebhookSecrets is the launch-blocking
// case: two organizers, two separate Stripe accounts, two signing secrets.
//
// Until webhookSecretsFromOrgConfig learned to resolve the org from a real
// provider event envelope (which carries none of arena's own ids), the
// per-org secrets were never consulted for a genuine Stripe webhook at all.
func TestHostedCheckout_TwoOrgsUseTheirOwnWebhookSecrets(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	orgA := newHostedOrgFixture(t, ctx, pool, "x")
	defer orgA.cleanup()
	orgB := newHostedOrgFixture(t, ctx, pool, "y")
	defer orgB.cleanup()

	if orgA.webhookSecret == orgB.webhookSecret {
		t.Fatal("fixture bug: the two orgs must have different webhook secrets")
	}

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	q := gen.New(pool)

	sessionFor := func(f *hostedOrgFixture) (csID uuid.UUID, providerID, token string) {
		t.Helper()
		code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/shows")
		if code != http.StatusCreated {
			t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
		}
		var start hostedStartResponse
		if err := json.Unmarshal(body, &start); err != nil {
			t.Fatalf("decode checkout/start: %v", err)
		}
		id, err := uuid.Parse(start.CheckoutSession.ID)
		if err != nil {
			t.Fatalf("checkout_session.id is not a UUID: %v", err)
		}
		intents, err := q.ListPaymentIntentsByCheckout(ctx, id)
		if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
			t.Fatalf("expected exactly one payment intent with a provider id; got %v (err %v)", intents, err)
		}
		return id, *intents[0].ProviderPaymentID, start.CheckoutToken
	}

	csA, providerA, tokenA := sessionFor(orgA)
	_, providerB, _ := sessionFor(orgB)

	if providerA == providerB {
		t.Fatal("stub stripe returned the same session id for two checkouts")
	}

	// Org A's payment signed with org B's secret must be REJECTED. If this
	// passes, any organizer could forge any other's payments.
	eventA := sessionCompletedEvent(providerA, "pi_wrong_secret", "paid")
	code, body := postSignedWebhook(t, srv, eventA, orgB.webhookSecret)
	if code != http.StatusUnauthorized {
		t.Fatalf("org A's event signed with org B's secret = %d, want 401; body: %s", code, body)
	}

	// And the same event signed with org A's OWN secret must be accepted.
	code, body = postSignedWebhook(t, srv, eventA, orgA.webhookSecret)
	if code != http.StatusOK {
		t.Fatalf("org A's event signed with org A's secret = %d, want 200; body: %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if processed, _ := resp["processed"].(bool); !processed {
		t.Fatalf("org A's own event was not processed; body: %s", body)
	}

	statusA := orgA.getStatus(t, srv, tokenA)
	if statusA.Status != "paid" {
		t.Errorf("org A public status = %q; want paid", statusA.Status)
	}

	// Org B is untouched by org A's payment.
	if piB, err := q.GetPaymentIntentByProviderID(ctx, providerB); err != nil {
		t.Fatalf("GetPaymentIntentByProviderID(org B): %v", err)
	} else if piB.State != "created" {
		t.Errorf("org B's payment intent state = %q; want created — org A's payment must not touch it", piB.State)
	}
	if csAfter, err := q.GetCheckoutSessionByID(ctx, csA); err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	} else if csAfter.State != "completed" {
		t.Errorf("org A checkout state = %q; want completed", csAfter.State)
	}
}

// TestHostedCheckout_RefusedReturnURLFallsBackToTheConfiguredBase proves the
// allow-list is applied for real, through the router — and that a refused
// origin degrades to PUBLIC_TICKETS_BASE_URL rather than failing the sale.
func TestHostedCheckout_RefusedReturnURLFallsBackToTheConfiguredBase(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "r")
	defer f.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	code, body := f.startCheckout(t, srv, "https://evil.example.com/steal")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode checkout/start: %v", err)
	}

	form := stripe.lastForm(t)
	for _, key := range []string{"success_url", "cancel_url"} {
		got := form.Get(key)
		if strings.Contains(got, "evil.example.com") {
			t.Fatalf("stripe %s = %q; a foreign origin reached the provider redirect", key, got)
		}
		if !strings.HasPrefix(got, hostedTicketsBaseURL) {
			t.Errorf("stripe %s = %q; want the configured PUBLIC_TICKETS_BASE_URL fallback", key, got)
		}
		if !strings.Contains(got, "checkout_token="+start.CheckoutToken) {
			t.Errorf("stripe %s = %q; want arena's checkout_token appended", key, got)
		}
	}
}

// TestHostedCheckout_UnconfiguredOrgIsRefusedLoudly proves an organizer who
// has not finished connecting Stripe gets a specific error instead of a dead
// redirect — the exact failure this wave exists to remove.
func TestHostedCheckout_UnconfiguredOrgIsRefusedLoudly(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "u")
	defer f.cleanup()

	// Deactivate the org's only Stripe config.
	if _, err := pool.Exec(ctx,
		`UPDATE payment_provider_configs SET is_active = false WHERE org_id = $1`, f.orgID); err != nil {
		t.Fatalf("deactivate payment config: %v", err)
	}

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/shows")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("checkout/start with an inactive payment config = %d, want 422; body: %s", code, body)
	}
	if !strings.Contains(string(body), "checkout.payment_not_configured") {
		t.Errorf("error body = %s; want the checkout.payment_not_configured code", body)
	}
	if len(stripe.sessions) != 0 {
		t.Error("a hosted session was created for an org with no usable payment config")
	}
}

// getStatus drives the real public order-status endpoint the widget polls.
func (f *hostedOrgFixture) getStatus(t *testing.T, srv *Server, checkoutToken string) hostedStatusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/checkout/"+checkoutToken, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET public checkout status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var out hostedStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode public checkout status: %v (body: %s)", err, rec.Body.String())
	}
	return out
}

type hostedStatusResponse struct {
	Status     string  `json:"status"`
	PaymentURL *string `json:"payment_url"`
	Tickets    []struct {
		TicketID string `json:"ticket_id"`
	} `json:"tickets"`
}

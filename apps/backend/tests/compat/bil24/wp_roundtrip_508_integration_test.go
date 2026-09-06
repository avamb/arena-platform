//go:build integration

// wp_roundtrip_508_integration_test.go — W1-B7e (feature #508), spec §15.3
// scenario 4, delivery half.
//
// Everything below the wpstub receiver is REAL: a seeded paid order in
// Postgres, the real `htickets.HandleCancelTicket` handler, the real
// `outbox.PGOutboxEventStore` + `outbox.OutboxEventsDispatcher` polling loop,
// and the real `bil24wire.Dispatcher` behind a `multiDispatcher` fan-out that
// mirrors cmd/arena-worker/main.go. Only the WordPress side is a stub, because
// that is the party we do not own.
//
// The four assertions the feature asks for, in order:
//
//  1. A paid order's `v1.order.paid` reaches the site as `order.paid` carrying
//     all N tickets (testdata/wp/wp_receiver/order_paid.json).
//  2. POST /v1/tickets/{id}/cancel produces exactly one `ticket.refunded`.
//  3. The receiver answering 503 once must be RETRIED, not dead-lettered:
//     next_attempt_at set, dead_lettered_at nil, success on delivery #2
//     (ticket_refunded.json → `retry`).
//  4. Replaying the same envelope is deduplicated by the receiver: still 200,
//     no additional stored occurrence (ticket_refunded.json → `replay`).
//
// The two scenario files are the SOURCE OF TRUTH for what a migrated site must
// observe; this test only binds their {{placeholders}} to the seeded facts.
// `data` is compared as a SUBSET — keys the scenario does not name are the
// encoder's business (tests/compat/bil24/encoder_binding_test.go pins the full
// key set), which keeps this test about DELIVERY rather than about encoding.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//
// Run with:
//
//	go test -tags integration -run TestCompatBil24_508 ./apps/backend/tests/compat/bil24/
package compat_bil24_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/tests/compat/bil24/wpstub"
)

// ─────────────────────────────────────────────────────────────────────────────
// Scenario files
// ─────────────────────────────────────────────────────────────────────────────

// wp508Scenario is one testdata/wp/wp_receiver/*.json document. Only the
// blocks a scenario actually declares are non-nil, so the same struct serves
// both the order and the ticket case.
type wp508Scenario struct {
	Type         string         `json:"type"`
	Data         map[string]any `json:"data"`
	ArrayLengths map[string]any `json:"arrayLengths"`
	TicketList   map[string]any `json:"ticketList"`
	Occurrences  int            `json:"occurrences"`
	Retry        *wp508Retry    `json:"retry"`
	Replay       *wp508Replay   `json:"replay"`
}

// wp508Retry drives the transient-outage assertions of spec §9.2.
type wp508Retry struct {
	FailFirstDelivery      bool `json:"failFirstDelivery"`
	DeliveriesUntilSuccess int  `json:"deliveriesUntilSuccess"`
	NextAttemptAtSet       bool `json:"nextAttemptAtSet"`
	DeadLettered           bool `json:"deadLettered"`
}

// wp508Replay drives the receiver-side deduplication assertions.
type wp508Replay struct {
	ExpectStatus          int `json:"expectStatus"`
	AdditionalOccurrences int `json:"additionalOccurrences"`
}

func wp508LoadScenario(t *testing.T, name string) wp508Scenario {
	t.Helper()
	path := filepath.Join("testdata", "wp", "wp_receiver", name+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scenario %s: %v", path, err)
	}
	var sc wp508Scenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse scenario %s: %v", path, err)
	}
	if sc.Type == "" {
		t.Fatalf("scenario %s declares no `type`", path)
	}
	return sc
}

// ─────────────────────────────────────────────────────────────────────────────
// Placeholder binding and subset comparison
// ─────────────────────────────────────────────────────────────────────────────

var wp508Placeholder = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// wp508Resolve substitutes {{name}} occurrences inside every string of a
// decoded scenario value. Unknown placeholders are left in place on purpose —
// they then fail the comparison loudly instead of silently matching nothing.
func wp508Resolve(v any, bind map[string]string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = wp508Resolve(val, bind)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = wp508Resolve(val, bind)
		}
		return out
	case string:
		return wp508Placeholder.ReplaceAllStringFunc(x, func(m string) string {
			if r, ok := bind[strings.Trim(m, "{}")]; ok {
				return r
			}
			return m
		})
	default:
		return v
	}
}

// wp508Norm renders a JSON scalar as the one canonical string both sides of a
// comparison are reduced to. It exists because a scenario file must spell an
// integer wire id as a "{{placeholder}}" STRING while the delivered document
// decodes it as a float64 — without a common normal form the two could never
// be compared. Format 'f' (never 'g') is deliberate: a nine-digit system id
// must read as 1000000123, not as 1.000000123e+09.
func wp508Norm(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// wp508Major renders a minor-unit amount the way the Bil24 wire carries it, so
// the expected side of a money assertion is produced by the same rule the
// encoder uses (float major units) rather than hand-written.
func wp508Major(minor int64) string {
	return strconv.FormatFloat(float64(minor)/100, 'f', -1, 64)
}

// wp508AssertSubset asserts every key the scenario names, recursing into
// nested objects. Keys present only in the delivered document are ignored:
// this test guards delivery, not the encoder's key set.
func wp508AssertSubset(t *testing.T, path string, want, got map[string]any) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("%s.%s: missing from the delivered payload", path, k)
			continue
		}
		if wm, isObj := wv.(map[string]any); isObj {
			gm, isGotObj := gv.(map[string]any)
			if !isGotObj {
				t.Errorf("%s.%s: want an object, delivered %T", path, k, gv)
				continue
			}
			wp508AssertSubset(t, path+"."+k, wm, gm)
			continue
		}
		if w, g := wp508Norm(wv), wp508Norm(gv); w != g {
			t.Errorf("%s.%s = %s, want %s", path, k, g, w)
		}
	}
}

// wp508Occurrences counts stored events of one site type in the receiver log.
func wp508Occurrences(recv *wpstub.Server, siteType string) int {
	n := 0
	for _, ev := range recv.Received() {
		if ev.Type == siteType {
			n++
		}
	}
	return n
}

// wp508Last returns the last stored event of one site type.
func wp508Last(recv *wpstub.Server, siteType string) (wpstub.Event, bool) {
	var (
		out   wpstub.Event
		found bool
	)
	for _, ev := range recv.Received() {
		if ev.Type == siteType {
			out, found = ev, true
		}
	}
	return out, found
}

// ─────────────────────────────────────────────────────────────────────────────
// Fan-out dispatcher — the test's copy of cmd/arena-worker's multiDispatcher
// ─────────────────────────────────────────────────────────────────────────────

// wp508MultiDispatcher mirrors the unexported multiDispatcher of
// cmd/arena-worker/main.go: every registered dispatcher sees every row, and the
// first error aborts the fan-out so the outbox retries the whole envelope. It
// is duplicated rather than exported because the production type lives in
// package main; keeping the semantics identical is what makes this test a
// proof about the worker and not just about bil24wire.
type wp508MultiDispatcher struct {
	dispatchers []outbox.Dispatcher
}

func (m *wp508MultiDispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	for _, d := range m.dispatchers {
		if err := d.Dispatch(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// wp508NoopDispatcher stands in for the base/MACS members of the production
// fan-out: it consumes every row without side effects, so the test proves the
// bil24_wp delivery survives being third in line.
type wp508NoopDispatcher struct{}

func (wp508NoopDispatcher) Dispatch(context.Context, outbox.Event) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Pool
// ─────────────────────────────────────────────────────────────────────────────

func wp508Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping the W1-B7e round-trip")
	}
	if !strings.HasPrefix(dsn, "post") {
		t.Skipf("DATABASE_URL %q is not a Postgres DSN; skipping", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ─────────────────────────────────────────────────────────────────────────────
// The round trip
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_508_WPReceiverRoundTrip(t *testing.T) {
	pool := wp508Pool(t)
	ctx := context.Background()

	orderScenario := wp508LoadScenario(t, "order_paid")
	refundScenario := wp508LoadScenario(t, "ticket_refunded")
	if refundScenario.Retry == nil || refundScenario.Replay == nil {
		t.Fatal("ticket_refunded.json must declare both `retry` and `replay`")
	}

	recv := wpstub.New()
	defer recv.Close()

	// ── Seeded facts ────────────────────────────────────────────────────────
	const (
		currency      = "CZK"
		buyerEmail    = "wp508-buyer@example.com"
		signingSecret = "wp508-signing-secret"
		ticketCount   = 3
		unitPrice     = int64(1000) // minor units
		orderSubtotal = unitPrice * ticketCount
		orderDiscount = int64(0)
		orderCharge   = int64(250)
		orderTotal    = orderSubtotal - orderDiscount + orderCharge
	)

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	cityID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	tierID := uuid.New()
	resID := uuid.New()
	csID := uuid.New()
	orderID := uuid.New()
	citySlug := "wp508-city-" + suffix
	channelName := "WP508 Channel " + suffix
	tierName := "WP508 Standard"
	eventName := "WP508 Event " + suffix
	venueName := "WP508 Venue " + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migrations not applied?): %v", err)
	}

	ticketIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id = ANY($1)`,
			[]string{orderID.String(), ticketIDs[0].String(), ticketIDs[1].String(), ticketIDs[2].String()})
		_, _ = pool.Exec(c, `DELETE FROM webhook_subscribers WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM orders WHERE id=$1`, orderID)
		_, _ = pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, csID)
		_, _ = pool.Exec(c, `DELETE FROM reservations WHERE id=$1`, resID)
		_, _ = pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM ticket_tiers WHERE id=$1`, tierID)
		_, _ = pool.Exec(c, `DELETE FROM compatibility_id_map WHERE platform_id = ANY($1)`,
			[]uuid.UUID{sessionID, eventID, venueID, cityID})
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		_, _ = pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1,$2,$3)`, cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value)
		VALUES ('geo.cities', $1, 'en', 'WP508 City')`, citySlug)
	mustExec(`INSERT INTO organizations (id, name, legal_name, slug, tax_id) VALUES ($1,$2,$3,$4,$5)`,
		orgID, "WP508 Org", "WP508 Legal s.r.o.", "wp508-"+suffix, "CZ12345678")
	mustExec(`INSERT INTO venues (id, org_id, name, city_id, timezone) VALUES ($1,$2,$3,$4,'Europe/Prague')`,
		venueID, orgID, venueName, cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1,$2,$3,'published','public')`, eventID, orgID, eventName)

	startAt := time.Date(2027, 5, 20, 17, 0, 0, 0, time.UTC)
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total,
			status, admission_mode, currency, currency_source)
		VALUES ($1,$2,$3,$4,$5,100,'scheduled','general_admission',$6,'override')`,
		sessionID, eventID, venueID, startAt, startAt.Add(3*time.Hour), currency)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1,$2,$3)`,
		channelID, orgID, channelName)
	mustExec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		VALUES ($1,$2,$3,'fixed',$4,$5,0)`, tierID, sessionID, tierName, unitPrice, currency)

	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold)
		VALUES ($1,$2,100,$3)`, sessionID, tierID, ticketCount)
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		resID, orgID, channelID, sessionID, ticketCount, time.Now().Add(30*time.Minute))
	completedAt := time.Date(2027, 5, 1, 9, 0, 0, 0, time.UTC)
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, total, currency, payment_provider, completed_at)
		VALUES ($1,$2,$3,$4,'completed',$5,$6,$7,$8,'yookassa',$9)`,
		csID, orgID, channelID, resID, orderSubtotal, orderDiscount, orderTotal, currency, completedAt)
	mustExec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id,
			reservation_id, source, status, currency, subtotal, discount, charge, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid',$8,$9,$10,$11,$12)`,
		orderID, orgID, channelID, eventID, sessionID, csID, resID,
		currency, orderSubtotal, orderDiscount, orderCharge, orderTotal)

	for i, tid := range ticketIDs {
		mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, order_id, tier_id,
				status, issued_at, ordinal, holder_email)
			VALUES ($1,$2,$3,$4,$5,'active',NOW(),$6,$7)`,
			tid, sessionID, csID, orderID, tierID, i+1, buyerEmail)
	}

	// The WordPress site: one active bil24_wp subscriber on the selling channel.
	mustExec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types,
			active, kind, org_id, channel_id)
		VALUES ('', $1, $2, '{}', TRUE, 'bil24_wp', $3, $4)`,
		recv.URL(), signingSecret, orgID, channelID)

	// The integer ids the site knows this sale by.
	var orderWireID int64
	if err := pool.QueryRow(ctx,
		`SELECT MIN(system_ticket_id) FROM tickets WHERE order_id=$1`, orderID,
	).Scan(&orderWireID); err != nil {
		t.Fatalf("read order wire id: %v", err)
	}
	if orderWireID <= 0 {
		t.Fatalf("order wire id = %d; want a minted system_ticket_id", orderWireID)
	}

	bind := map[string]string{
		"orderId":       strconv.FormatInt(orderWireID, 10),
		"currency":      currency,
		"buyerEmail":    buyerEmail,
		"orderSum":      wp508Major(orderSubtotal),
		"orderCharge":   wp508Major(orderCharge),
		"orderTotalSum": wp508Major(orderTotal),
		"ticketCount":   strconv.Itoa(ticketCount),
		"channelName":   channelName,
		"tierName":      tierName,
		"eventName":     eventName,
		"venueName":     venueName,
	}

	// ── The worker's dispatch chain, unmodified ─────────────────────────────
	wpDispatcher := bil24wire.NewDispatcher(pool)
	if wpDispatcher == nil {
		t.Fatal("bil24wire.NewDispatcher returned nil for a live pool")
	}
	fanOut := &wp508MultiDispatcher{dispatchers: []outbox.Dispatcher{
		wp508NoopDispatcher{}, // base outbox dispatcher
		wp508NoopDispatcher{}, // MACS dispatcher
		wpDispatcher,          // bil24_wp dispatcher
	}}
	dispatchOpts := outbox.OutboxEventsDispatcherOptions{
		Store:        outbox.NewPGOutboxEventStore(pool),
		Dispatcher:   fanOut,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		// A one-hour backoff makes the retry MANUAL: the test, not the clock,
		// decides when attempt #2 happens, so the 503 assertions cannot race a
		// spontaneous redelivery.
		BackoffFunc: func(int) time.Duration { return time.Hour },
	}

	// ── 1. order.paid reaches the site with all N tickets ───────────────────
	mustExec(`INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
		VALUES ('order', $1::text, $2,
			jsonb_build_object('order_id', $1::text, 'channel_id', $3::text), NOW())`,
		orderID.String(), bil24wire.EventOrderPaid, channelID.String())

	if !wp508Drain(t, dispatchOpts, func() bool {
		return wp508Occurrences(recv, bil24wire.SiteEventOrderPaid) >= 1
	}, 15*time.Second) {
		t.Fatal("order.paid never reached the WordPress receiver")
	}

	if got := wp508Occurrences(recv, bil24wire.SiteEventOrderPaid); got != orderScenario.Occurrences {
		t.Errorf("order.paid delivered %d time(s), want %d", got, orderScenario.Occurrences)
	}
	paid, ok := wp508Last(recv, bil24wire.SiteEventOrderPaid)
	if !ok {
		t.Fatal("no order.paid stored by the receiver")
	}

	wantOrderData, _ := wp508Resolve(orderScenario.Data, bind).(map[string]any)
	wp508AssertSubset(t, "order.paid.data", wantOrderData, paid.Data)

	wantLengths, _ := wp508Resolve(orderScenario.ArrayLengths, bind).(map[string]any)
	for key, wantLen := range wantLengths {
		arr, isArr := paid.Data[key].([]any)
		if !isArr {
			t.Errorf("order.paid.data.%s: want an array, delivered %T", key, paid.Data[key])
			continue
		}
		if w, g := wp508Norm(wantLen), wp508Norm(float64(len(arr))); w != g {
			t.Errorf("order.paid.data.%s has %s element(s), want %s", key, g, w)
		}
	}

	// Every ticket of the order must satisfy the per-ticket expectations —
	// "all N tickets" is the point of order.paid.
	wantTicket, _ := wp508Resolve(orderScenario.TicketList, bind).(map[string]any)
	ticketList, _ := paid.Data["ticketList"].([]any)
	if len(ticketList) != ticketCount {
		t.Fatalf("order.paid carried %d ticket(s), want %d", len(ticketList), ticketCount)
	}
	for i, raw := range ticketList {
		tk, isObj := raw.(map[string]any)
		if !isObj {
			t.Errorf("order.paid ticketList[%d] is %T, want an object", i, raw)
			continue
		}
		wp508AssertSubset(t, "order.paid.ticketList["+strconv.Itoa(i)+"]", wantTicket, tk)
	}

	// ── 2/3. Real cancel → ticket.refunded, surviving one 503 ───────────────
	//
	// Reset here (not earlier): the order.paid assertions above are done, and
	// the retry scenario counts DELIVERIES, which must start from zero.
	recv.Reset()

	cancelledTicket := ticketIDs[0]
	publishCancelled := func(cancelCtx context.Context, ticket gen.TicketRow, reason, refundMode string) {
		_, dbErr := pool.Exec(cancelCtx, `
			INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
			VALUES ('ticket', $1::text, $2,
				jsonb_build_object(
					'ticket_id',   $1::text,
					'session_id',  $3::text,
					'reason',      $4::text,
					'refund_mode', $5::text
				),
				NOW())`,
			ticket.ID.String(), bil24wire.EventTicketCancelled, ticket.SessionID.String(), reason, refundMode,
		)
		if dbErr != nil {
			t.Errorf("publishCancelled: insert outbox_events: %v", dbErr)
		}
	}

	genQ := gen.New(pool)
	cancelHandler := htickets.New(
		genQ, // ticketQueries
		nil,  // credentialQueries
		nil,  // complimentaryQueries
		genQ, // inventoryQueries
		nil,  // reservationQueries
		nil,  // barcodeQueries
		nil,  // deliveryJobQueries
		nil,  // feedTokenQueries
		nil,  // workerPool
		pool, // TxStarter
		nil,  // audit
		slog.Default(),
		nil, // publishTicketIssuedEvents
		nil, // publishTicketRevokedV1Events
		publishCancelled,
	)

	cancelBody, _ := json.Marshal(map[string]any{
		"reason":      "W1-B7e round-trip cancellation",
		"refund_mode": "manual",
	})
	cancelReq := httptest.NewRequest(http.MethodPost,
		"/v1/tickets/"+cancelledTicket.String()+"/cancel", bytes.NewReader(cancelBody))
	cancelReq.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", cancelledTicket.String())
	cancelReq = cancelReq.WithContext(context.WithValue(
		auth.WithActor(cancelReq.Context(), auth.Actor{ID: "wp508-test-actor", Type: "user"}),
		chi.RouteCtxKey, rctx,
	))

	cancelRec := httptest.NewRecorder()
	cancelHandler.HandleCancelTicket(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("HandleCancelTicket: want 200, got %d\nBody: %s", cancelRec.Code, cancelRec.Body.String())
	}

	var cancelledStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, cancelledTicket).Scan(&cancelledStatus); err != nil {
		t.Fatalf("read cancelled ticket status: %v", err)
	}
	if cancelledStatus != "cancelled" {
		t.Fatalf("ticket status = %q after cancel, want 'cancelled'", cancelledStatus)
	}

	var outboxRows int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM outbox_events
		WHERE aggregate_id=$1::text AND event_type=$2`,
		cancelledTicket.String(), bil24wire.EventTicketCancelled,
	).Scan(&outboxRows); err != nil {
		t.Fatalf("count v1.ticket.cancelled rows: %v", err)
	}
	if outboxRows != 1 {
		t.Fatalf("cancel wrote %d v1.ticket.cancelled outbox row(s), want exactly 1", outboxRows)
	}

	var ticketWireID int64
	if err := pool.QueryRow(ctx,
		`SELECT system_ticket_id FROM tickets WHERE id=$1`, cancelledTicket,
	).Scan(&ticketWireID); err != nil {
		t.Fatalf("read ticket wire id: %v", err)
	}
	bind["ticketId"] = strconv.FormatInt(ticketWireID, 10)

	// Attempt #1: the site is down.
	if refundScenario.Retry.FailFirstDelivery {
		recv.SetOnceFail()
	}
	if !wp508Drain(t, dispatchOpts, func() bool { return recv.Deliveries() >= 1 }, 15*time.Second) {
		t.Fatal("the first ticket.refunded delivery attempt never reached the receiver")
	}

	var nextAttemptAt, deadLetteredAt, processedAt *time.Time
	readOutbox := func() {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT next_attempt_at, dead_lettered_at, processed_at FROM outbox_events
			WHERE aggregate_id=$1::text AND event_type=$2`,
			cancelledTicket.String(), bil24wire.EventTicketCancelled,
		).Scan(&nextAttemptAt, &deadLetteredAt, &processedAt); err != nil {
			t.Fatalf("read outbox row: %v", err)
		}
	}
	readOutbox()
	if refundScenario.Retry.NextAttemptAtSet && nextAttemptAt == nil {
		t.Error("after the 503: next_attempt_at is nil; the outbox must schedule a retry")
	}
	if got := deadLetteredAt != nil; got != refundScenario.Retry.DeadLettered {
		t.Errorf("after the 503: dead_lettered = %v, want %v (a transient outage must not dead-letter)",
			got, refundScenario.Retry.DeadLettered)
	}
	if processedAt != nil {
		t.Error("after the 503: processed_at is set; a rejected delivery is not a success")
	}
	if n := wp508Occurrences(recv, bil24wire.SiteEventTicketRefunded); n != 0 {
		t.Errorf("the 503 attempt stored %d ticket.refunded event(s), want 0", n)
	}

	// Attempt #2: bring the retry forward and let the same chain deliver.
	if _, err := pool.Exec(ctx, `
		UPDATE outbox_events SET next_attempt_at = NOW() - '1 second'::interval
		WHERE aggregate_id=$1::text AND event_type=$2`,
		cancelledTicket.String(), bil24wire.EventTicketCancelled,
	); err != nil {
		t.Fatalf("bring the retry forward: %v", err)
	}
	if !wp508Drain(t, dispatchOpts, func() bool {
		return wp508Occurrences(recv, bil24wire.SiteEventTicketRefunded) >= 1
	}, 15*time.Second) {
		t.Fatal("the retried ticket.refunded never reached the WordPress receiver")
	}

	readOutbox()
	if processedAt == nil {
		t.Error("after the successful retry: processed_at is nil; MarkDispatched did not run")
	}
	if deadLetteredAt != nil {
		t.Error("after the successful retry: the row is dead-lettered")
	}
	if got, want := recv.Deliveries(), refundScenario.Retry.DeliveriesUntilSuccess; got != want {
		t.Errorf("ticket.refunded took %d delivery attempt(s), want %d", got, want)
	}
	if got := wp508Occurrences(recv, bil24wire.SiteEventTicketRefunded); got != refundScenario.Occurrences {
		t.Errorf("ticket.refunded stored %d time(s), want %d", got, refundScenario.Occurrences)
	}

	refunded, ok := wp508Last(recv, bil24wire.SiteEventTicketRefunded)
	if !ok {
		t.Fatal("no ticket.refunded stored by the receiver")
	}
	wantRefundData, _ := wp508Resolve(refundScenario.Data, bind).(map[string]any)
	wp508AssertSubset(t, "ticket.refunded.data", wantRefundData, refunded.Data)

	// ── 4. Replaying the same envelope is deduplicated ──────────────────────
	deliveriesUntilSuccess := recv.Deliveries()
	before := wp508Occurrences(recv, bil24wire.SiteEventTicketRefunded)
	replayBody, err := json.Marshal(map[string]any{"type": refunded.Type, "data": refunded.Data})
	if err != nil {
		t.Fatalf("marshal replay envelope: %v", err)
	}
	replayResp, err := http.Post(recv.URL(), "application/json", bytes.NewReader(replayBody))
	if err != nil {
		t.Fatalf("replay POST: %v", err)
	}
	replayResp.Body.Close() //nolint:errcheck
	if replayResp.StatusCode != refundScenario.Replay.ExpectStatus {
		t.Errorf("replay returned %d, want %d (a redelivery is accepted, not rejected)",
			replayResp.StatusCode, refundScenario.Replay.ExpectStatus)
	}
	if got := wp508Occurrences(recv, bil24wire.SiteEventTicketRefunded) - before; got != refundScenario.Replay.AdditionalOccurrences {
		t.Errorf("replay added %d stored occurrence(s), want %d",
			got, refundScenario.Replay.AdditionalOccurrences)
	}

	t.Logf("W1-B7e round-trip OK: order %d delivered with %d tickets; ticket %d refunded after %d attempt(s); replay deduplicated",
		orderWireID, ticketCount, ticketWireID, deliveriesUntilSuccess)
}

// wp508Drain runs a real OutboxEventsDispatcher until `until` reports true or
// the timeout expires, then stops it. Stop() waits for Run() to return, which
// is what guarantees the row's MarkFailed / MarkDispatched has already
// committed by the time the caller asserts on the database.
func wp508Drain(
	t *testing.T,
	opts outbox.OutboxEventsDispatcherOptions,
	until func() bool,
	timeout time.Duration,
) bool {
	t.Helper()
	oed, err := outbox.NewOutboxEventsDispatcher(opts)
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}
	// context.Background() (not a cancellable child): stopping the loop must
	// not cancel the in-flight MarkFailed/MarkDispatched queries.
	go func() { _ = oed.Run(context.Background()) }()

	deadline := time.Now().Add(timeout)
	satisfied := false
	for time.Now().Before(deadline) {
		if until() {
			satisfied = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = oed.Stop()
	return satisfied
}

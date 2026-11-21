//go:build integration

package conversions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// TestConversions_PaidOrderToStubs_LiveDB drives the real SQL: the leg queues
// one row per ready provider only for an order with a stored buyer context,
// the sweep sends them to stub Meta / GA4 servers with the consent read at
// send time, and a refused consent sends nothing.
func TestConversions_PaidOrderToStubs_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var (
		orgID, venueID, evtID, sessID, chanID = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		resvID, csID, orderID                 = uuid.New(), uuid.New(), uuid.New()
		resv2ID, cs2ID, order2ID              = uuid.New(), uuid.New(), uuid.New()
		nonce                                 = orgID.String()[:8]
	)
	defer func() {
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM orders WHERE org_id = $1`, orgID},
			{`DELETE FROM checkout_sessions WHERE org_id = $1`, orgID},
			{`DELETE FROM reservations WHERE org_id = $1`, orgID},
			{`DELETE FROM sales_channels WHERE id = $1`, chanID},
			{`DELETE FROM sessions WHERE id = $1`, sessID},
			{`DELETE FROM events WHERE id = $1`, evtID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM organizations WHERE id = $1`, orgID},
		} {
			if _, err := pool.Exec(context.Background(), step.sql, step.arg); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	}()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Conv Org "+nonce, "conv-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Conv Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Conv Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'ILS', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Channel "+nonce)
	for _, ids := range [][3]uuid.UUID{{resvID, csID, orderID}, {resv2ID, cs2ID, order2ID}} {
		exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, ids[0], orgID, chanID, sessID)
		exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		      VALUES ($1, $2, $3, $4, 'completed')`, ids[1], orgID, chanID, ids[0])
		exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'ILS', 1890, 0, 0, 1890, 'B', 'b@example.test', '+972501234567')`,
			ids[2], orgID, chanID, evtID, sessID, ids[1], ids[0])
	}
	// Both orders carry a context and both granted tracking consent at
	// checkout/start; the consent lives on checkout_sessions (P-20), never in
	// the context row. Order 2 withdraws it after the conversions are queued
	// (PATCH .../consent), so the sweep must read it at send time.
	exec(`INSERT INTO checkout_buyer_contexts (checkout_session_id, order_id, context)
	      VALUES ($1, $2, '{"fbp":"fb.1.1.2","ga_client_id":"9.9","client_ip":"203.0.113.9","user_agent":"UA"}')`, csID, orderID)
	exec(`INSERT INTO checkout_buyer_contexts (checkout_session_id, order_id, context)
	      VALUES ($1, $2, '{"fbp":"fb.1.9.9"}')`, cs2ID, order2ID)
	for _, id := range []uuid.UUID{csID, cs2ID} {
		exec(`UPDATE checkout_sessions SET tracking_consent_granted = true, tracking_consent_version = 'v1',
		             tracking_consent_at = now() - interval '1 hour', tracking_consent_received_at = now() WHERE id = $1`, id)
	}

	store := NewPGStore(pool)
	leg := NewDispatcher(store, slog.Default())

	// No settings yet: nothing is queued.
	_ = leg.Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: orderID.String()})
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM conversion_deliveries WHERE org_id = $1`, orgID).Scan(&n)
	if n != 0 {
		t.Fatalf("queued without settings: %d", n)
	}

	exec(`INSERT INTO org_conversion_settings (org_id, meta_pixel_id, meta_capi_token, ga4_measurement_id, ga4_api_secret)
	      VALUES ($1, '1352226752766944', 'meta-token-123', 'G-ABCD1234', 'ga-secret-123')`, orgID)
	for _, id := range []uuid.UUID{orderID, order2ID, orderID} { // the repeat must be a no-op
		_ = leg.Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: id.String(), Payload: map[string]any{"order_id": id.String()}})
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM conversion_deliveries WHERE org_id = $1`, orgID).Scan(&n)
	if n != 4 {
		t.Fatalf("deliveries = %d, want 4 (2 orders x 2 providers)", n)
	}

	var metaHits, gaHits int32
	var metaBody map[string]any
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&metaHits, 1)
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &metaBody)
		w.WriteHeader(200)
	}))
	defer meta.Close()
	ga := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gaHits, 1)
		w.WriteHeader(204)
	}))
	defer ga.Close()
	senders := Senders{Meta: MetaSender{BaseURL: meta.URL}, GA4: GA4Sender{BaseURL: ga.URL}}

	// Order 2's buyer says no after the deliveries were queued.
	if applied, err := trackingconsent.Store(ctx, pool, cs2ID, trackingconsent.Consent{Granted: false, Version: "v1", At: time.Now()}); err != nil || !applied {
		t.Fatalf("withdraw consent: applied=%v err=%v", applied, err)
	}

	// Make our rows due and run the sweep until it has nothing left of ours.
	exec(`UPDATE conversion_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE org_id = $1`, orgID)
	for i := 0; i < 5; i++ {
		if _, err := RunOnce(ctx, store, senders, slog.Default(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if metaHits < 1 || gaHits < 1 {
		t.Fatalf("hits meta=%d ga=%d", metaHits, gaHits)
	}
	rows, err := pool.Query(ctx, `SELECT order_id, provider, status, COALESCE(last_error, '') FROM conversion_deliveries WHERE org_id = $1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var oid uuid.UUID
		var prov, status, lerr string
		if err := rows.Scan(&oid, &prov, &status, &lerr); err != nil {
			t.Fatal(err)
		}
		seen++
		switch {
		case oid == orderID && status != "sent":
			t.Errorf("%s of the consenting order: %s %s", prov, status, lerr)
		case oid == order2ID && (status != "skipped" || lerr != "no_consent"):
			t.Errorf("%s of the refusing order: %s %s", prov, status, lerr)
		}
		if strings.Contains(lerr, "token") || strings.Contains(lerr, "secret") {
			t.Errorf("last_error leaks: %q", lerr)
		}
	}
	if seen != 4 {
		t.Fatalf("rows seen = %d", seen)
	}
	ev := metaBody["data"].([]any)[0].(map[string]any)
	if ev["event_id"] != orderID.String() {
		t.Fatalf("event_id = %v, want the order id", ev["event_id"])
	}

	// Every delivery of both orders is final (sent / skipped no_consent): the
	// stored IP, User-Agent and click ids are forgotten, the rows stay so a
	// repeated order.paid still queues nothing.
	for _, cs := range []uuid.UUID{csID, cs2ID} {
		var stored string
		if err := pool.QueryRow(ctx, `SELECT context::text FROM checkout_buyer_contexts WHERE checkout_session_id = $1`, cs).Scan(&stored); err != nil {
			t.Fatalf("context row of %s: %v", cs, err)
		}
		if stored != "{}" {
			t.Errorf("context of %s = %s after the final delivery, want {}", cs, stored)
		}
	}
	_ = leg.Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: orderID.String()})
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM conversion_deliveries WHERE org_id = $1`, orgID).Scan(&n)
	if n != 4 {
		t.Fatalf("a repeated order.paid after forgetting queued again: %d rows", n)
	}

	// A context is never kept past ContextRetention: an old one without any
	// delivery is emptied by the sweep, a fresh one is left alone.
	oldResv, oldCS, freshResv, freshCS := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, ids := range [][2]uuid.UUID{{oldResv, oldCS}, {freshResv, freshCS}} {
		exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		      VALUES ($1, $2, $3, $4, 1, 'expired', now())`, ids[0], orgID, chanID, sessID)
		exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		      VALUES ($1, $2, $3, $4, 'expired')`, ids[1], orgID, chanID, ids[0])
	}
	exec(`INSERT INTO checkout_buyer_contexts (checkout_session_id, context, created_at)
	      VALUES ($1, '{"client_ip":"203.0.113.10"}', now() - interval '31 days')`, oldCS)
	exec(`INSERT INTO checkout_buyer_contexts (checkout_session_id, context)
	      VALUES ($1, '{"client_ip":"203.0.113.11"}')`, freshCS)
	if _, err := RunOnce(ctx, store, senders, slog.Default(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for cs, want := range map[uuid.UUID]string{oldCS: "{}", freshCS: `{"client_ip": "203.0.113.11"}`} {
		var stored string
		if err := pool.QueryRow(ctx, `SELECT context::text FROM checkout_buyer_contexts WHERE checkout_session_id = $1`, cs).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != want {
			t.Errorf("context of %s = %s, want %s", cs, stored, want)
		}
	}
}

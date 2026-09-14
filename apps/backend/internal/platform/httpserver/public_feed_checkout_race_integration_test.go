//go:build integration

// public_feed_checkout_race_integration_test.go — live-server coverage for
// the LOW-severity concurrency defect fixed 2026-09-14 in the widget
// checkout path.
//
// createPublicOrder (hfeed/public_feed_checkout.go) resolves the buyer's
// customer identity inside the checkout-confirm transaction and treats the
// resolve as "non-fatal" — but before the fix, a losing race on
// customer_identities_strong_uq (migration 0091, a GLOBAL unique index)
// left the transaction ABORTED even though the Go code only logged a
// warning and carried on: the very next statement in that same
// transaction (or the final COMMIT) would then die with SQLSTATE 25P02,
// turning an already-priced, already-held cart into a 500. The fix wraps
// the whole best-effort section in a SAVEPOINT (Handler.bestEffort,
// mirroring hbil24.Handler.payBestEffort) and, independently, makes
// customers.Resolve itself race-safe so the conflict is usually resolved
// before it would ever reach that savepoint.
//
// This test drives two REAL concurrent HTTP requests through the mounted
// chi router for the SAME brand-new buyer identity (email AND phone), each
// against its OWN session so the unrelated
// orders_one_pending_per_customer_session_uq business rule (one open order
// per customer PER SESSION) cannot itself produce a legitimate conflict —
// the only thing this test wants to force a race on is the customer
// identity resolve.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// twoSessionFeedFixture is a minimal published-feed topology with TWO
// independent GA sessions sharing one event/channel/feed token, so two
// concurrent checkouts for the same buyer never collide on
// orders_one_pending_per_customer_session_uq.
type twoSessionFeedFixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	orgID      uuid.UUID
	venueID    uuid.UUID
	eventID    uuid.UUID
	sessionIDs [2]uuid.UUID
	tierIDs    [2]uuid.UUID
	channelID  uuid.UUID
	tokenID    uuid.UUID
	feedToken  string
	buyerMail  string
	buyerPhone string
}

func newTwoSessionFeedFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *twoSessionFeedFixture {
	t.Helper()
	f := &twoSessionFeedFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		tokenID:   uuid.New(),
	}
	f.sessionIDs = [2]uuid.UUID{uuid.New(), uuid.New()}
	f.tierIDs = [2]uuid.UUID{uuid.New(), uuid.New()}
	suffix := f.orgID.String()[:8]
	f.feedToken = "race-feed-" + suffix
	f.buyerMail = fmt.Sprintf("race-buyer-%s@arena-integration.test", suffix)
	f.buyerPhone = fmt.Sprintf("+3632%07d", time.Now().UnixNano()%10_000_000)

	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "Race Org " + suffix, "race-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "Race Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "Race Event " + suffix}},
		{`INSERT INTO sales_channels (id, org_id, name, fee_percent, collect_name, collect_phone)
		  VALUES ($1, $2, $3, 1.25, true, true)`,
			[]any{f.channelID, f.orgID, "Race Channel " + suffix}},
		{`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active)
		  VALUES ($1, $2, $3, 'race', true)`,
			[]any{f.tokenID, f.feedToken, f.channelID}},
		{`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`,
			[]any{f.eventID, f.tokenID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("newTwoSessionFeedFixture step %d failed: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
			    capacity_total, status, admission_mode, currency, currency_source)
			 VALUES ($1, $2, $3, now() + interval '30 days',
			    now() + interval '30 days 2 hours', 10, 'scheduled',
			    'general_admission', 'EUR', 'override')`,
			f.sessionIDs[i], f.eventID, f.venueID); err != nil {
			f.cleanup()
			t.Fatalf("newTwoSessionFeedFixture session %d insert failed: %v", i, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO ticket_tiers
			    (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
			 VALUES ($1, $2, 'Race Tier', 'fixed', 2500, 'EUR', 0)`,
			f.tierIDs[i], f.sessionIDs[i]); err != nil {
			f.cleanup()
			t.Fatalf("newTwoSessionFeedFixture tier %d insert failed: %v", i, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 10)`,
			f.sessionIDs[i]); err != nil {
			f.cleanup()
			t.Fatalf("newTwoSessionFeedFixture inventory %d insert failed: %v", i, err)
		}
		// AB-51: AllocateGAUnitsTx only claims EXISTING available ga_unit
		// rows. Migration 0101: they belong to the CATEGORY.
		if _, err := gen.New(pool).InsertGAUnits(ctx, f.sessionIDs[i], "ga|t1", 0, &f.tierIDs[i], 5); err != nil {
			f.cleanup()
			t.Fatalf("newTwoSessionFeedFixture InsertGAUnits %d failed: %v", i, err)
		}
	}
	return f
}

func (f *twoSessionFeedFixture) cleanup() {
	ctx := context.Background()
	stmts := []struct {
		sql string
		arg any
	}{
		{`DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id::text FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM payment_intents WHERE org_id = $1`, f.orgID},
		{`DELETE FROM orders WHERE org_id = $1`, f.orgID},
		{`DELETE FROM worker_jobs WHERE payload->>'checkout_session_id' IN
		   (SELECT id::text FROM checkout_sessions WHERE org_id = $1)`, f.orgID},
		{`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID},
		{`UPDATE session_seats SET reservation_id = NULL, status = 'available'
		   WHERE session_id = ANY($1::uuid[])`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM reservation_seats WHERE reservation_id IN
		   (SELECT id FROM reservations WHERE session_id = ANY($1::uuid[]))`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM reservation_ga_items WHERE reservation_id IN
		   (SELECT id FROM reservations WHERE session_id = ANY($1::uuid[]))`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM reservations WHERE session_id = ANY($1::uuid[])`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM session_seats WHERE session_id = ANY($1::uuid[])`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM inventory_ledger WHERE session_id = ANY($1::uuid[])`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM ticket_tiers WHERE session_id = ANY($1::uuid[])`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM event_publications WHERE event_id = $1`, f.eventID},
		{`DELETE FROM agent_feed_tokens WHERE id = $1`, f.tokenID},
		{`DELETE FROM sessions WHERE id = ANY($1::uuid[])`, []uuid.UUID{f.sessionIDs[0], f.sessionIDs[1]}},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
		{`DELETE FROM customers WHERE id IN
		   (SELECT customer_id FROM customer_identities WHERE value_normalized = $1)`, f.buyerMail},
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("twoSessionFeedFixture cleanup (%.60s): %v", s.sql, err)
		}
	}
}

// TestPublicFeedCheckout_ConcurrentSameNewBuyer_NoRaceAbortedTx drives two
// real concurrent POST /checkout/start requests for the identical
// brand-new buyer email+phone, each against its own session, and asserts
// neither answers 500. Before the fix this was NOT reliably reproducible
// (the race window is narrow and the loser's error was merely logged), so
// this test documents intent as much as it guards a regression — see the
// fallback assertion below for the deterministic half of the guarantee.
func TestPublicFeedCheckout_ConcurrentSameNewBuyer_NoRaceAbortedTx(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newTwoSessionFeedFixture(t, ctx, pool)
	defer f.cleanup()

	srv := buildIntegrationResetServer(t, pool)

	const n = 2
	var wg sync.WaitGroup
	codes := make([]int, n)
	bodies := make([]string, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			body, err := json.Marshal(map[string]any{
				"session_id": f.sessionIDs[i].String(),
				"tier_id":    f.tierIDs[i].String(),
				"qty":        1,
				"buyer": map[string]any{
					"email": f.buyerMail,  // SAME new buyer for both goroutines
					"name":  "Race Buyer", // SAME name — irrelevant to the race
					"phone": f.buyerPhone, // SAME new phone for both goroutines
				},
			})
			if err != nil {
				bodies[i] = "marshal error: " + err.Error()
				return
			}
			req := httptest.NewRequest(http.MethodPost,
				"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.router.ServeHTTP(rec, req)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if codes[i] != http.StatusCreated {
			t.Fatalf("goroutine %d: checkout/start = %d, want 201 (a customer-resolve race must never "+
				"abort the checkout transaction); body: %s", i, codes[i], bodies[i])
		}
	}

	// Both orders resolved to the SAME customer, and only one customer row
	// (plus one email identity row) exists for this brand-new buyer — the
	// deterministic, always-checkable half of the guarantee, independent of
	// whether this particular run actually won the race window.
	var customerCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(DISTINCT customer_id) FROM customer_identities WHERE kind = 'email' AND value_normalized = $1`,
		f.buyerMail).Scan(&customerCount); err != nil {
		t.Fatalf("count customers for %s: %v", f.buyerMail, err)
	}
	if customerCount != 1 {
		t.Fatalf("distinct customers for email %s = %d, want 1", f.buyerMail, customerCount)
	}

	var orderCustomerCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(DISTINCT customer_id) FROM orders WHERE org_id = $1 AND customer_id IS NOT NULL`,
		f.orgID).Scan(&orderCustomerCount); err != nil {
		t.Fatalf("count distinct order customers: %v", err)
	}
	if orderCustomerCount != 1 {
		t.Fatalf("distinct customer_id across the two orders = %d, want 1 (both must resolve to the same buyer)", orderCustomerCount)
	}
}

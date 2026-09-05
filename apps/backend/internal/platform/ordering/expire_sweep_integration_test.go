//go:build integration

package ordering_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// TestExpireSweep_LiveDB is the integration test feature #487 mandates for the
// order.expire_sweep job (spec §14.1). The unit tests in expire_sweep_test.go
// cover the handler's control flow against a fake store; what only a live
// database can prove is the candidate query itself — in particular the
// NOT EXISTS (succeeded payment_intent) clause and the status guard on the
// UPDATE, both of which are pure SQL and therefore invisible to the fakes.
//
// Four orders are seeded:
//
//	dead     — pending_payment, expires_at in the past, no payment intent
//	paid_pi  — pending_payment, expires_at in the past, but a succeeded intent
//	           exists (the webhook is in flight; the customer's money arrived)
//	future   — pending_payment, expires_at still ahead
//	settled  — already paid
//
// Only `dead` may be swept.
func TestExpireSweep_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newSweepFixture(t, ctx, pool)
	defer f.cleanup()

	dead := f.seedOrder(t, ctx, "dead", ordering.StatusPendingPayment, time.Now().UTC().Add(-10*time.Minute))
	paidPI := f.seedOrder(t, ctx, "paid_pi", ordering.StatusPendingPayment, time.Now().UTC().Add(-10*time.Minute))
	future := f.seedOrder(t, ctx, "future", ordering.StatusPendingPayment, time.Now().UTC().Add(30*time.Minute))
	settled := f.seedOrder(t, ctx, "settled", ordering.StatusPaid, time.Now().UTC().Add(-10*time.Minute))

	// The webhook already recorded a successful payment for paid_pi even though
	// the order status has not caught up yet.
	if _, err := pool.Exec(ctx, `
		INSERT INTO payment_intents (checkout_session_id, org_id, provider, amount, currency, state)
		VALUES ($1, $2, 'mock', 1000, 'EUR', 'succeeded')`,
		f.checkoutOf[paidPI], f.orgID,
	); err != nil {
		t.Fatalf("seed succeeded payment intent: %v", err)
	}

	q := gen.New(pool)

	// The candidate query is global, so scope the assertion to this fixture
	// rather than to the returned count — the shared stand may carry unrelated
	// expirable orders.
	candidates, err := q.ListExpirableOrders(ctx, time.Now().UTC(), 500)
	if err != nil {
		t.Fatalf("ListExpirableOrders: %v", err)
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range candidates {
		seen[c.ID] = true
	}
	if !seen[dead] {
		t.Fatal("the past-due unpaid order is not a sweep candidate")
	}
	if seen[paidPI] {
		t.Fatal("an order with a succeeded payment intent was offered as a sweep candidate")
	}
	if seen[future] {
		t.Fatal("an order whose hold has not run out was offered as a sweep candidate")
	}
	if seen[settled] {
		t.Fatal("an already-paid order was offered as a sweep candidate")
	}

	if _, err := ordering.RunExpireSweep(ctx, q, time.Now().UTC(), 500, nil); err != nil {
		t.Fatalf("RunExpireSweep: %v", err)
	}

	for id, want := range map[uuid.UUID]string{
		dead:    ordering.StatusExpired,
		paidPI:  ordering.StatusPendingPayment,
		future:  ordering.StatusPendingPayment,
		settled: ordering.StatusPaid,
	} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read back order %s: %v", id, err)
		}
		if got != want {
			t.Fatalf("order %s status = %s, want %s", f.nameOf[id], got, want)
		}
	}

	// The audit trail is the only durable record of why the order died.
	var events int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM order_events WHERE order_id = $1 AND type = $2`,
		dead, ordering.EventHoldExpired,
	).Scan(&events); err != nil {
		t.Fatalf("count hold_expired events: %v", err)
	}
	if events != 1 {
		t.Fatalf("hold_expired events = %d, want exactly 1", events)
	}

	// A second tick must be a no-op: the status guard means the order is no
	// longer a candidate, so no duplicate audit row appears.
	if _, err := ordering.RunExpireSweep(ctx, q, time.Now().UTC(), 500, nil); err != nil {
		t.Fatalf("second RunExpireSweep: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM order_events WHERE order_id = $1 AND type = $2`,
		dead, ordering.EventHoldExpired,
	).Scan(&events); err != nil {
		t.Fatalf("recount hold_expired events: %v", err)
	}
	if events != 1 {
		t.Fatalf("hold_expired events after a second sweep = %d, want still 1", events)
	}
}

// sweepFixture seeds the row graph an order needs: organization → venue →
// event → session → sales channel, plus one reservation and checkout session
// per order (orders.checkout_session_id is UNIQUE).
type sweepFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	channelID uuid.UUID

	orderIDs   []uuid.UUID
	checkoutOf map[uuid.UUID]uuid.UUID
	nameOf     map[uuid.UUID]string
}

func newSweepFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *sweepFixture {
	f := &sweepFixture{
		t: t, pool: pool,
		orgID:      uuid.New(),
		venueID:    uuid.New(),
		eventID:    uuid.New(),
		sessionID:  uuid.New(),
		channelID:  uuid.New(),
		checkoutOf: map[uuid.UUID]uuid.UUID{},
		nameOf:     map[uuid.UUID]string{},
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "Sweep Org " + suffix, "sweep-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "Sweep Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "Sweep Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '20 days',
		    now() + interval '20 days 2 hours', 100, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{f.channelID, f.orgID, "Sweep Channel " + suffix}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("sweep fixture step %d failed: %v", i, err)
		}
	}
	return f
}

// seedOrder inserts one reservation + checkout session + order in the given
// status with the given expires_at.
func (f *sweepFixture) seedOrder(t *testing.T, ctx context.Context, name, status string, expiresAt time.Time) uuid.UUID {
	t.Helper()

	q := gen.New(f.pool)
	res, err := q.InsertReservation(ctx, f.orgID, f.channelID, f.sessionID, nil, nil, 1, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("seed reservation for %s: %v", name, err)
	}

	checkoutID := uuid.New()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'pricing_confirmed')`,
		checkoutID, f.orgID, f.channelID, res.ID,
	); err != nil {
		t.Fatalf("seed checkout session for %s: %v", name, err)
	}

	orderID := uuid.New()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id,
		    checkout_session_id, reservation_id, source, status, currency,
		    subtotal, discount, charge, total, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', $8, 'EUR',
		    1000, 0, 0, 1000, $9)`,
		orderID, f.orgID, f.channelID, f.eventID, f.sessionID,
		checkoutID, res.ID, status, expiresAt,
	); err != nil {
		t.Fatalf("seed order %s: %v", name, err)
	}

	f.orderIDs = append(f.orderIDs, orderID)
	f.checkoutOf[orderID] = checkoutID
	f.nameOf[orderID] = name
	return orderID
}

func (f *sweepFixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("sweep cleanup (%s): %v", sql, err)
		}
	}
	// order_events / order_items cascade from orders.
	exec(`DELETE FROM orders WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM payment_intents WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM reservation_ga_items WHERE reservation_id IN
	      (SELECT id FROM reservations WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM reservations WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM sales_channels WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM sessions WHERE id = $1`, f.sessionID)
	exec(`DELETE FROM events WHERE id = $1`, f.eventID)
	exec(`DELETE FROM venues WHERE id = $1`, f.venueID)
	exec(`DELETE FROM organizations WHERE id = $1`, f.orgID)
}

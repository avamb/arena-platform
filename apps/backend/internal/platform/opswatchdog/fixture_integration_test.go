//go:build integration

package opswatchdog_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// wdFixture seeds the row graph an order/ticket needs: organization → venue
// → event → session → sales channel. Modelled on ordering's sweepFixture
// (apps/backend/internal/platform/ordering/expire_sweep_integration_test.go)
// — the same shape, reused here so the watchdog's checks (which read
// orders/tickets/payment_intents/checkout_sessions joined to organizations
// and events) have real rows to query.
type wdFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	channelID uuid.UUID
}

func newWDFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *wdFixture {
	t.Helper()
	f := &wdFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		channelID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "Watchdog Org " + suffix, "watchdog-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "Watchdog Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "Watchdog Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '20 days',
		    now() + interval '20 days 2 hours', 100, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{f.channelID, f.orgID, "Watchdog Channel " + suffix}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("watchdog fixture step %d failed: %v", i, err)
		}
	}
	return f
}

// seedOrder inserts one reservation + checkout session + order. paidAt nil
// leaves the order pending_payment; non-nil marks it paid.
func (f *wdFixture) seedOrder(t *testing.T, ctx context.Context, status string, paidAt *time.Time) uuid.UUID {
	t.Helper()

	q := gen.New(f.pool)
	res, err := q.InsertReservation(ctx, f.orgID, f.channelID, f.sessionID, nil, nil, 1, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("seed reservation: %v", err)
	}

	checkoutID := uuid.New()
	csState := "pricing_confirmed"
	if status == "paid" {
		csState = "completed"
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, $5)`,
		checkoutID, f.orgID, f.channelID, res.ID, csState,
	); err != nil {
		t.Fatalf("seed checkout session: %v", err)
	}

	orderID := uuid.New()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id,
		    checkout_session_id, reservation_id, source, status, currency,
		    subtotal, discount, charge, total, paid_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', $8, 'EUR',
		    1500, 0, 0, 1500, $9)`,
		orderID, f.orgID, f.channelID, f.eventID, f.sessionID,
		checkoutID, res.ID, status, paidAt,
	); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return orderID
}

// seedTicket inserts one active ticket linked to orderID.
func (f *wdFixture) seedTicket(t *testing.T, ctx context.Context, orderID, checkoutSessionID uuid.UUID) uuid.UUID {
	t.Helper()
	ticketID := uuid.New()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO tickets (id, checkout_session_id, session_id, status, order_id)
		VALUES ($1, $2, $3, 'active', $4)`,
		ticketID, checkoutSessionID, f.sessionID, orderID,
	); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return ticketID
}

func (f *wdFixture) checkoutSessionOf(t *testing.T, ctx context.Context, orderID uuid.UUID) uuid.UUID {
	t.Helper()
	var csID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id=$1`, orderID).Scan(&csID); err != nil {
		t.Fatalf("read checkout_session_id: %v", err)
	}
	return csID
}

func (f *wdFixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("watchdog fixture cleanup (%s): %v", sql, err)
		}
	}
	exec(`DELETE FROM refunds WHERE org_id = $1`, f.orgID)
	exec(`UPDATE tickets SET order_id = NULL WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM tickets WHERE session_id = $1`, f.sessionID)
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

// cleanupOpsWatchdogState removes ops_watchdog_state/ops_alerts rows this
// package's tests may have created, keyed by fingerprint/key prefix. The
// watchdog's own tables have no FK back to the fixture's org, so cleanup is
// by explicit key/fingerprint list rather than a cascading delete.
func cleanupOpsAlerts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fingerprintLike string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM ops_alerts WHERE fingerprint LIKE $1`, fingerprintLike); err != nil {
		t.Logf("cleanup ops_alerts: %v", err)
	}
}

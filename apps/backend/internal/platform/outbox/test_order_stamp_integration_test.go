//go:build integration

package outbox

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// TestPGEventsWriter_StampsTestOrders proves an event naming an order of a
// test sales channel (P-13) is stored with "is_test": true, and one of a
// real order is not. Everything runs in one rolled-back transaction.
func TestPGEventsWriter_StampsTestOrders(t *testing.T) {
	pool := pr04Pool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	orgID, venueID, evtID, sessID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	testCh, realCh := uuid.New(), uuid.New()
	testOrder, realOrder := uuid.New(), uuid.New()
	nonce := orgID.String()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Stamp Org "+nonce, "stamp-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'CZK', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider, is_test)
	      VALUES ($1, $2, 'Test '||$3, 'direct_merchant', 'stripe', true), ($4, $2, 'Real '||$3, 'direct_merchant', 'stripe', false)`,
		testCh, orgID, nonce, realCh)
	for _, o := range []struct{ id, ch uuid.UUID }{{testOrder, testCh}, {realOrder, realCh}} {
		resv, cs := uuid.New(), uuid.New()
		exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resv, orgID, o.ch, sessID)
		exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		      VALUES ($1, $2, $3, $4, 'completed')`, cs, orgID, o.ch, resv)
		exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                          source, status, currency, subtotal, discount, charge, total)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', 'paid', 'CZK', 100, 0, 0, 100)`,
			o.id, orgID, o.ch, evtID, sessID, cs, resv)
	}

	w := NewPGEventsWriter(nil)
	stored := func(ev Event) map[string]any {
		t.Helper()
		if err := w.Append(ctx, tx, ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM outbox_events WHERE aggregate_id = $1 ORDER BY occurred_at DESC, id DESC LIMIT 1`,
			ev.AggregateID).Scan(&raw); err != nil {
			t.Fatalf("read back: %v", err)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return p
	}

	p := stored(Event{AggregateType: "order", AggregateID: testOrder.String(), EventType: "v1.order.paid",
		Payload: map[string]any{"order_id": testOrder.String()}})
	if p[PayloadIsTest] != true || p["order_id"] != testOrder.String() {
		t.Errorf("test order payload = %v; want is_test true and the original keys", p)
	}
	p = stored(Event{AggregateType: "order", AggregateID: realOrder.String(), EventType: "v1.order.paid",
		Payload: map[string]any{"order_id": realOrder.String()}})
	if _, has := p[PayloadIsTest]; has {
		t.Errorf("real order payload = %v; must not carry is_test", p)
	}
	ticketAgg := uuid.New()
	p = stored(Event{AggregateType: "ticket", AggregateID: ticketAgg.String(), EventType: "v1.ticket.refunded",
		Payload: map[string]any{"ticket_id": ticketAgg.String()}})
	if _, has := p[PayloadIsTest]; has {
		t.Errorf("unknown ticket payload = %v; must not carry is_test", p)
	}
}

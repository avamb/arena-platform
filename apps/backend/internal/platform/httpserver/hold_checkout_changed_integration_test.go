//go:build integration

// hold_checkout_changed_integration_test.go — spec 67 section 4.4: a
// checkout/start with hold_token prices the hold's seats in one transaction
// and confirms in another. A seat added to the hold in between (POST /holds
// with the same hold_token) must not become an order at the price of the
// seats priced before: 409 hold.changed, no checkout, no order; the next
// checkout prices the new seat set.
package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func TestHoldCheckout_SeatAddedAfterPricingIsRefused(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newSeatHoldFixture(t, ctx, pool, "hcg")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	// Between the two transactions the hold takes a third seat the way
	// POST /holds does: the hold row first, then the seat.
	added := false
	srv.holdPricedHook = func(reservationID uuid.UUID) {
		if added || reservationID != f.reservationID {
			return
		}
		added = true
		bg := context.Background()
		tx, err := pool.Begin(bg)
		if err != nil {
			t.Errorf("begin add seat: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(bg) }()
		q := gen.New(pool).WithTx(tx)
		if _, err := q.LockPublicHoldByTokenHash(bg, holdpolicy.HashHoldToken(f.holdToken)); err != nil {
			t.Errorf("lock hold: %v", err)
			return
		}
		if _, conflicts, err := hcheckout.ExtendHoldPartialTx(bg, q, hcheckout.HoldMutationInput{
			ReservationID: reservationID, SeatKeys: []string{"S-A-3"}, Now: time.Now().UTC(),
		}); err != nil || len(conflicts) > 0 {
			t.Errorf("add seat: %v %v", err, conflicts)
			return
		}
		if err := tx.Commit(bg); err != nil {
			t.Errorf("commit add seat: %v", err)
		}
	}

	code, out, body := postHoldStart(t, srv, f, preparedHold)
	if !added {
		t.Fatalf("the hook never ran: %d %s", code, body)
	}
	if code != http.StatusConflict || prepErrCode(out) != "hold.changed" {
		t.Fatalf("start after the seats changed = %d %s, want 409 hold.changed", code, body)
	}
	var orders, checkouts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE reservation_id = $1`, f.reservationID).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM checkout_sessions WHERE reservation_id = $1`, f.reservationID).Scan(&checkouts); err != nil {
		t.Fatal(err)
	}
	if orders != 0 || checkouts != 0 {
		t.Fatalf("after hold.changed: %d orders, %d checkouts, want none", orders, checkouts)
	}
	h := f.hold(t, ctx)
	if h.started != nil {
		t.Fatalf("the payment step was given on a refused checkout: %v", h.started)
	}
	if n := f.seatsOfHold(t, ctx); n != 3 {
		t.Fatalf("seats of the hold = %d, want 3 (the refusal keeps them)", n)
	}

	// The next checkout prices the three seats and orders all of them.
	srv.holdPricedHook = nil
	code, _, body = postHoldStart(t, srv, f, preparedHold)
	if code != http.StatusCreated {
		t.Fatalf("start after the refusal = %d %s", code, body)
	}
	var items int
	var total int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM order_items oi WHERE oi.order_id = o.id), o.total
		FROM orders o WHERE o.reservation_id = $1`, f.reservationID).Scan(&items, &total); err != nil {
		t.Fatal(err)
	}
	var unit int64
	if err := pool.QueryRow(ctx, `SELECT price_amount FROM ticket_tiers WHERE id = $1`, f.tierID).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	if items != 3 || total < 3*unit {
		t.Fatalf("order of the changed hold: %d items, total %d, want 3 items and at least %d", items, total, 3*unit)
	}
}

//go:build integration

// hold_page_guard_integration_test.go — review findings of spec 67 stage T5:
// a hold of the click whose open Stripe attempt has no page_expires_at is
// not released by reservation.expire_sweep before its hard limit, and
// Release refuses a hold that got a payment attempt after the close read
// them.
package holdclose_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdclose"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func runReservationSweepClosing(t *testing.T, f *fixture) {
	t.Helper()
	q := gen.New(f.pool)
	p := hcheckout.NewReservationProcessor(f.pool, q, quiet()).WithCheckoutQueries(q).
		WithPaymentGrace(grace).WithPageClosingProviders([]string{"stripe"})
	if _, err := p.ProcessExpiredReservations(context.Background(), 1000); err != nil {
		t.Fatalf("reservation sweep: %v", err)
	}
}

func TestReservationSweep_MissingPageExpiry_NotSwept_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 8)
	// A hold of the click with an open Stripe attempt whose page expiry was
	// never written: left to hold.close_payment.
	missing := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: time.Minute})
	// The same past its hard limit (expires_at + the longest page + grace):
	// released, nothing sticks forever.
	limit := holdpolicy.MinProviderPageLifetime + grace
	stale := f.newPaymentHold(3, 2, holdOpts{clickPath: true, expiredAgo: limit + time.Minute})
	// The old path (no hold key) without page_expires_at: released as before.
	old := f.newPaymentHold(5, 2, holdOpts{expiredAgo: time.Minute})
	// A hold of the click whose attempt is of a provider that cannot close
	// its page: released as before.
	other := f.newPaymentHold(7, 2, holdOpts{clickPath: true, expiredAgo: time.Minute})
	if _, err := pool.Exec(context.Background(), `UPDATE payment_intents SET provider = 'flitt' WHERE id = $1`, other.intentID); err != nil {
		t.Fatal(err)
	}

	runReservationSweepClosing(t, f)

	f.assertSeats(missing, "held")
	if s := f.str(`SELECT state FROM reservations WHERE id = $1`, missing.reservationID); s != "draft" {
		t.Fatalf("hold without page expiry: state = %s, want draft", s)
	}
	f.assertSeats(stale, "available")
	f.assertSeats(old, "available")
	f.assertSeats(other, "available")
}

// The policy snapshot moves the hard limit: payment 1800 + paying 600 = 40
// minutes of page life, so a hold expired 35 minutes ago is still left alone.
func TestReservationSweep_MissingPageExpiry_PolicyLimit_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	ctx := context.Background()
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 35 * time.Minute})
	// payment 30 min + paying 10 min = 40 min of page life, + 2 min grace.
	if _, err := pool.Exec(ctx, `UPDATE reservations
		SET hold_policy = '{"payment_seconds":1800,"extend_seconds":0,"extend_count":0,"paying_seconds":600}'::jsonb
		WHERE id = $1`, h.reservationID); err != nil {
		t.Fatal(err)
	}
	runReservationSweepClosing(t, f)
	f.assertSeats(h, "held")

	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() - interval '43 minutes' WHERE id = $1`, h.reservationID); err != nil {
		t.Fatal(err)
	}
	runReservationSweepClosing(t, f)
	f.assertSeats(h, "available")
}

// Release refuses a hold whose open attempts are not all in the list it was
// given (a page recorded after OpenIntents): the mark is cleared, nothing is
// released, and the next pass closes both pages.
func TestHoldClose_ReleaseRefusesUnlistedAttempt_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	ctx := context.Background()
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	store := holdclose.NewPGStore(pool)

	mark, _, ok, err := store.Claim(ctx, h.reservationID, time.Now().Add(-holdclose.StaleClaim))
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	listed, err := store.OpenIntents(ctx, h.reservationID, []string{"stripe"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("open intents = %v, %v", listed, err)
	}
	// A second page recorded after the list was read.
	second := "cs_test_" + uuid.NewString()[:12]
	if _, err := pool.Exec(ctx, `
		INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state, page_expires_at)
		VALUES ($1, $2, 'stripe', $3, 2500, 'EUR', 'created', now() + interval '30 minutes')`,
		h.checkoutID, f.orgID, second); err != nil {
		t.Fatal(err)
	}

	released, err := store.Release(ctx, h.reservationID, mark, []uuid.UUID{listed[0].PaymentIntentID})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released {
		t.Fatal("released a hold with an unlisted open attempt")
	}
	if m := f.closingMark(h); m != nil {
		t.Fatalf("closing mark = %v, want cleared", m)
	}
	f.assertSeats(h, "held")
	res, order, checkout, intent := f.state(h)
	if res != "draft" || order != "pending_payment" || checkout != "pricing_confirmed" || intent != "created" {
		t.Fatalf("after refusal: %s %s %s %s", res, order, checkout, intent)
	}

	// The next pass closes both pages and releases the hold.
	sp := newFakeStripe()
	st := run(t, newProc(pool, sp))
	if st.Released != 1 {
		t.Fatalf("stats = %+v, want 1 released", st)
	}
	if sp.expired[h.sessionID] != 1 || sp.expired[second] != 1 {
		t.Fatalf("expired at Stripe = %v, want both pages", sp.expired)
	}
	f.assertSeats(h, "available")
}

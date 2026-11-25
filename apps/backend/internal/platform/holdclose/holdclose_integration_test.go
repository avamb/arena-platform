//go:build integration

// holdclose_integration_test.go drives hold.close_payment, the hold-aware
// reservation and order sweeps, the late-payment window bounds and the
// visitor hash purge against a live, migrated PostgreSQL (spec
// 08_architecture/67 sections 5.2 and 6.1). The provider is a fake; every
// database statement is the production one.
package holdclose_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdclose"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

const grace = 2 * time.Minute

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func livePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ── fixture ─────────────────────────────────────────────────────────────────

type fixture struct {
	t                                                   *testing.T
	pool                                                *pgxpool.Pool
	orgID, venueID, eventID, channelID, sessionID, tier uuid.UUID
	planID, versionID                                   uuid.UUID
}

func newFixture(t *testing.T, pool *pgxpool.Pool, seats int) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t, pool: pool, orgID: uuid.New(), venueID: uuid.New(), eventID: uuid.New(),
		channelID: uuid.New(), sessionID: uuid.New(), tier: uuid.New(), planID: uuid.New(), versionID: uuid.New()}
	sfx := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, []any{f.orgID, "HoldClose Org " + sfx, "holdclose-" + sfx}},
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, []any{f.venueID, f.orgID, "HoldClose Venue " + sfx}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{f.eventID, f.orgID, "HoldClose Event " + sfx}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode) VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`, []any{f.channelID, f.orgID, "HoldClose Channel " + sfx}},
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`, []any{f.planID, f.venueID, f.orgID, "HoldClose Plan " + sfx}},
		{`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, locked_at)
		  VALUES ($1, $2, 1, '{}'::jsonb, $3, $4, now())`, []any{f.versionID, f.planID, "holdclose-" + sfx, seats}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode,
		    seating_plan_version_id, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 3 hours', $4, 'scheduled',
		    'assigned_seats', $5, 'EUR', 'override')`, []any{f.sessionID, f.eventID, f.venueID, seats, f.versionID}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
		  VALUES ($1, $2, 'HoldClose Tier', 'fixed', 2500, 'EUR', $3, 1, true)`, []any{f.tier, f.sessionID, seats}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, $2)`, []any{f.sessionID, seats}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'Parter|A|' || gs::text, 'Parter', 'A', gs::text, $3, 'available', 'seat'
		  FROM generate_series(1, $2::int) gs`, []any{f.sessionID, seats, f.tier}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	ctx := context.Background()
	for _, s := range []string{
		`DELETE FROM late_payments WHERE org_id = $1`,
		`DELETE FROM orders WHERE org_id = $1`,
		`DELETE FROM payment_intents WHERE org_id = $1`,
		`DELETE FROM checkout_sessions WHERE org_id = $1`,
		`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = $1)`,
		`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = $1)`,
		`DELETE FROM session_seats WHERE session_id IN
		   (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
		`UPDATE reservations SET rehold_of = NULL WHERE org_id = $1`,
		`DELETE FROM reservations WHERE org_id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, s, f.orgID); err != nil {
			f.t.Logf("cleanup %q: %v", s, err)
		}
	}
	for _, s := range []struct {
		sql string
		id  uuid.UUID
	}{
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM sessions WHERE id = $1`, f.sessionID},
		{`DELETE FROM seating_plan_versions WHERE id = $1`, f.versionID},
		{`DELETE FROM seating_plans WHERE id = $1`, f.planID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.id); err != nil {
			f.t.Logf("cleanup %q: %v", s.sql, err)
		}
	}
}

// paymentHold is a hold of the click path on the payment step: seats held,
// a pending order, a checkout and an open Stripe attempt.
type paymentHold struct {
	reservationID, checkoutID, orderID, intentID uuid.UUID
	sessionID                                    string // Stripe cs_…
	seatKeys                                     []string
}

type holdOpts struct {
	clickPath   bool          // hold_token_hash set
	expiredAgo  time.Duration // expires_at = now - expiredAgo
	pageIn      *time.Duration
	payingIn    *time.Duration
	orderExpAgo time.Duration
}

func dur(d time.Duration) *time.Duration { return &d }

func (f *fixture) newPaymentHold(firstSeat, n int, o holdOpts) paymentHold {
	f.t.Helper()
	ctx := context.Background()
	q := gen.New(f.pool)
	keys := make([]string, 0, n)
	for i := firstSeat; i < firstSeat+n; i++ {
		keys = append(keys, "Parter|A|"+strconv.Itoa(i))
	}
	res, err := hcheckout.CreateSeatedHold(ctx, f.pool, q, hcheckout.SeatedHoldInput{
		OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID, SeatKeys: keys,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		f.t.Fatalf("CreateSeatedHold: %v", err)
	}
	h := paymentHold{reservationID: res.Reservation.ID, checkoutID: uuid.New(), orderID: uuid.New(),
		sessionID: "cs_test_" + uuid.NewString()[:12], seatKeys: keys}
	var tokenHash []byte
	if o.clickPath {
		tokenHash = make([]byte, 32)
		_, _ = rand.Read(tokenHash)
	}
	var paying *time.Time
	if o.payingIn != nil {
		p := time.Now().Add(*o.payingIn)
		paying = &p
	}
	if _, err := f.pool.Exec(ctx, `
		UPDATE reservations SET expires_at = now() - $2::interval, hold_token_hash = $3, paying_until = $4,
		       payment_started_at = now() - interval '15 minutes'
		WHERE id = $1`, h.reservationID, fmt.Sprintf("%d seconds", int(o.expiredAgo.Seconds())), tokenHash, paying); err != nil {
		f.t.Fatalf("set hold fields: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'pricing_confirmed')`, h.checkoutID, f.orgID, f.channelID, h.reservationID); err != nil {
		f.t.Fatalf("seed checkout: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		    source, status, currency, subtotal, discount, charge, total, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'pending_payment', 'EUR', 2500, 0, 0, 2500, now() - $8::interval)`,
		h.orderID, f.orgID, f.channelID, f.eventID, f.sessionID, h.checkoutID, h.reservationID,
		fmt.Sprintf("%d seconds", int(o.orderExpAgo.Seconds()))); err != nil {
		f.t.Fatalf("seed order: %v", err)
	}
	var page *time.Time
	if o.pageIn != nil {
		p := time.Now().Add(*o.pageIn)
		page = &p
	}
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state, page_expires_at)
		VALUES ($1, $2, 'stripe', $3, 2500, 'EUR', 'created', $4) RETURNING id`,
		h.checkoutID, f.orgID, h.sessionID, page).Scan(&h.intentID); err != nil {
		f.t.Fatalf("seed payment intent: %v", err)
	}
	return h
}

func (f *fixture) seatStatus(h paymentHold) map[string]string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT seat_key, status FROM session_seats WHERE session_id = $1 AND seat_key = ANY($2)`, f.sessionID, h.seatKeys)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	for rows.Next() {
		var k, s string
		if err := rows.Scan(&k, &s); err != nil {
			f.t.Fatal(err)
		}
		out[k] = s
	}
	return out
}

func (f *fixture) assertSeats(h paymentHold, want string) {
	f.t.Helper()
	for k, s := range f.seatStatus(h) {
		if s != want {
			f.t.Fatalf("seat %s = %s, want %s", k, s, want)
		}
	}
}

func (f *fixture) str(sql string, id uuid.UUID) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), sql, id).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (f *fixture) state(h paymentHold) (res, order, checkout, intent string) {
	return f.str(`SELECT state FROM reservations WHERE id = $1`, h.reservationID),
		f.str(`SELECT status FROM orders WHERE id = $1`, h.orderID),
		f.str(`SELECT state FROM checkout_sessions WHERE id = $1`, h.checkoutID),
		f.str(`SELECT state FROM payment_intents WHERE id = $1`, h.intentID)
}

func (f *fixture) closingMark(h paymentHold) *time.Time {
	var m *time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT closing_started_at FROM reservations WHERE id = $1`, h.reservationID).Scan(&m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

// ── fake Stripe ─────────────────────────────────────────────────────────────

type fakeStripe struct {
	mu      sync.Mutex
	status  map[string]string // session status
	payment map[string]string // PaymentIntent status
	expErr  map[string]error
	readErr map[string]error
	expired map[string]int
}

func newFakeStripe() *fakeStripe {
	return &fakeStripe{status: map[string]string{}, payment: map[string]string{}, expErr: map[string]error{},
		readErr: map[string]error{}, expired: map[string]int{}}
}

func (s *fakeStripe) ReadCheckoutSessionPayment(_ context.Context, _, _ uuid.UUID, _, sid string) (payments.CheckoutSessionPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readErr[sid]; err != nil {
		return payments.CheckoutSessionPayment{}, err
	}
	st := s.status[sid]
	if st == "" {
		st = payments.EmbeddedSessionOpen
	}
	return payments.CheckoutSessionPayment{ID: sid, Status: st, PaymentStatus: s.payment[sid]}, nil
}

func (s *fakeStripe) ExpireEmbeddedCheckout(_ context.Context, _, _ uuid.UUID, _, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expired[sid]++
	if err := s.expErr[sid]; err != nil {
		return err
	}
	s.status[sid] = payments.EmbeddedSessionExpired
	return nil
}

func newProc(pool *pgxpool.Pool, sp *fakeStripe) *holdclose.Processor {
	return holdclose.NewProcessor(holdclose.ProcessorOptions{
		Store: holdclose.NewPGStore(pool), Closer: sp, Providers: []string{"stripe"},
		Metrics: holdclose.NewMetrics(), Logger: quiet(),
	})
}

func run(t *testing.T, p *holdclose.Processor) holdclose.Stats {
	t.Helper()
	st, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return st
}

// ── hold.close_payment ──────────────────────────────────────────────────────

func TestHoldClose_Closed_SeatsFreeOrderExpired_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	sp := newFakeStripe()
	run(t, newProc(pool, sp))
	if sp.expired[h.sessionID] != 1 {
		t.Fatalf("expire calls = %d, want 1", sp.expired[h.sessionID])
	}
	res, ord, cs, pi := f.state(h)
	if res != "expired" || ord != "expired" || cs != "expired" || pi != "failed" {
		t.Fatalf("states reservation=%s order=%s checkout=%s intent=%s", res, ord, cs, pi)
	}
	if code := f.str(`SELECT failure_code FROM payment_intents WHERE id = $1`, h.intentID); code != holdclose.FailureCodeSessionExpired {
		t.Fatalf("failure_code = %s", code)
	}
	f.assertSeats(h, "available")
	var held int32
	if err := pool.QueryRow(context.Background(),
		`SELECT capacity_held FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`, f.sessionID).Scan(&held); err != nil || held != 0 {
		t.Fatalf("capacity_held = %d (%v), want 0", held, err)
	}
}

func TestHoldClose_Complete_SeatsKept_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	sp := newFakeStripe()
	sp.status[h.sessionID] = payments.EmbeddedSessionComplete
	sp.payment[h.sessionID] = payments.SessionPaymentSucceeded
	st := run(t, newProc(pool, sp))
	if st.Complete != 1 || sp.expired[h.sessionID] != 0 {
		t.Fatalf("stats %+v expire calls %d", st, sp.expired[h.sessionID])
	}
	res, ord, _, pi := f.state(h)
	if res != "draft" || ord != "pending_payment" || pi != "created" {
		t.Fatalf("states reservation=%s order=%s intent=%s, want untouched", res, ord, pi)
	}
	f.assertSeats(h, "held")
	if f.closingMark(h) != nil {
		t.Fatal("closing mark left behind")
	}
}

func TestHoldClose_AlreadyExpiredAtStripe_SeatsFree_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	// Read says expired at once: no expire call.
	first := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	// Read says open, expire is refused, the read after says expired.
	refused := f.newPaymentHold(3, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	sp := newFakeStripe()
	sp.status[first.sessionID] = payments.EmbeddedSessionExpired
	sp.expErr[refused.sessionID] = fmt.Errorf("stripe: %w", payments.ErrCheckoutSessionNotExpirable)
	p := holdclose.NewProcessor(holdclose.ProcessorOptions{
		Store: holdclose.NewPGStore(pool), Closer: &refuseThenExpired{fakeStripe: sp}, Providers: []string{"stripe"}, Logger: quiet(),
	})
	run(t, p)
	if sp.expired[first.sessionID] != 0 || sp.expired[refused.sessionID] != 1 {
		t.Fatalf("expire calls %v", sp.expired)
	}
	for _, h := range []paymentHold{first, refused} {
		res, ord, _, pi := f.state(h)
		if res != "expired" || ord != "expired" || pi != "failed" {
			t.Fatalf("states reservation=%s order=%s intent=%s", res, ord, pi)
		}
		f.assertSeats(h, "available")
	}
}

// refuseThenExpired refuses the expire and then reports the session expired.
type refuseThenExpired struct{ *fakeStripe }

func (r *refuseThenExpired) ExpireEmbeddedCheckout(ctx context.Context, o, c uuid.UUID, p, sid string) error {
	err := r.fakeStripe.ExpireEmbeddedCheckout(ctx, o, c, p, sid)
	r.mu.Lock()
	r.status[sid] = payments.EmbeddedSessionExpired
	r.mu.Unlock()
	return err
}

func TestHoldClose_RequiresAction_NotExpired_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	sp := newFakeStripe()
	sp.payment[h.sessionID] = payments.SessionPaymentRequiresAction
	st := run(t, newProc(pool, sp))
	if st.InProgress != 1 || sp.expired[h.sessionID] != 0 {
		t.Fatalf("stats %+v expire calls %d", st, sp.expired[h.sessionID])
	}
	res, ord, _, pi := f.state(h)
	if res != "draft" || ord != "pending_payment" || pi != "created" {
		t.Fatalf("states reservation=%s order=%s intent=%s", res, ord, pi)
	}
	f.assertSeats(h, "held")
	if f.closingMark(h) != nil {
		t.Fatal("closing mark not cleared while the payment is being confirmed")
	}
}

func TestHoldClose_NetworkError_Retried_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	sp := newFakeStripe()
	sp.expErr[h.sessionID] = errors.New("stripe: http: connection reset")
	p := newProc(pool, sp)
	st := run(t, p)
	if st.Retry != 1 {
		t.Fatalf("stats %+v, want one retry", st)
	}
	f.assertSeats(h, "held")
	if f.closingMark(h) != nil {
		t.Fatal("closing mark not cleared after a network error")
	}
	// The provider answers on the next pass: closed and released.
	delete(sp.expErr, h.sessionID)
	st = run(t, p)
	if st.Released != 1 {
		t.Fatalf("second pass %+v, want released", st)
	}
	f.assertSeats(h, "available")
}

func TestHoldClose_PayingUntil_Skipped_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute), payingIn: dur(5 * time.Minute)})
	sp := newFakeStripe()
	run(t, newProc(pool, sp))
	if sp.expired[h.sessionID] != 0 {
		t.Fatal("a paying hold was closed")
	}
	f.assertSeats(h, "held")
	// The claim itself refuses it too.
	if _, _, err := gen.New(pool).ClaimHoldForClose(context.Background(), h.reservationID, time.Now().Add(-holdclose.StaleClaim)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim of a paying hold: %v, want no rows", err)
	}
}

// Old path (no hold_token_hash): never a candidate of hold.close_payment.
func TestHoldClose_OldPath_NotACandidate_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: false, expiredAgo: 30 * time.Second})
	sp := newFakeStripe()
	run(t, newProc(pool, sp))
	if sp.expired[h.sessionID] != 0 {
		t.Fatal("an old-path hold was closed by hold.close_payment")
	}
	f.assertSeats(h, "held")
}

// After the claim the extend and paying routes see closing_started_at (they
// refuse with hold.expired); a second claim does not win either.
func TestHoldClose_ClaimBlocksExtend_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: 30 * time.Second, pageIn: dur(15 * time.Minute)})
	q := gen.New(pool)
	stale := time.Now().Add(-holdclose.StaleClaim)
	mark, _, err := q.ClaimHoldForClose(context.Background(), h.reservationID, stale)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if m := f.closingMark(h); m == nil || !m.Equal(mark) {
		t.Fatalf("closing_started_at = %v, want %v", m, mark)
	}
	if _, _, err := q.ClaimHoldForClose(context.Background(), h.reservationID, stale); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim: %v, want no rows", err)
	}
	// The hold row the public routes lock carries the mark: the extend and
	// paying routes answer hold.expired on a non-NULL closing_started_at.
	var open bool
	if err := pool.QueryRow(context.Background(), `
		SELECT closing_started_at IS NULL FROM reservations WHERE hold_token_hash IS NOT NULL AND id = $1 FOR UPDATE`,
		h.reservationID).Scan(&open); err != nil || open {
		t.Fatalf("route view: closing_started_at IS NULL = %v (%v), want false", open, err)
	}
	// Clearing only removes this claim's own mark.
	if err := q.ClearHoldCloseMark(context.Background(), h.reservationID, mark.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if f.closingMark(h) == nil {
		t.Fatal("a foreign mark was cleared")
	}
	if err := q.ClearHoldCloseMark(context.Background(), h.reservationID, mark); err != nil {
		t.Fatal(err)
	}
	if f.closingMark(h) != nil {
		t.Fatal("own mark not cleared")
	}
}

// ── reservation.expire_sweep and order.expire_sweep ─────────────────────────

func runReservationSweep(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	q := gen.New(pool)
	p := hcheckout.NewReservationProcessor(pool, q, quiet()).WithCheckoutQueries(q).WithPaymentGrace(grace)
	if _, err := p.ProcessExpiredReservations(context.Background(), 1000); err != nil {
		t.Fatalf("reservation sweep: %v", err)
	}
}

func TestReservationSweep_HardLimit_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 8)
	// Page still alive: page_expires_at in 10 minutes.
	alive := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: time.Minute, pageIn: dur(10 * time.Minute)})
	// Page expired 1 minute ago, but inside the grace: still left alone.
	inGrace := f.newPaymentHold(3, 2, holdOpts{clickPath: true, expiredAgo: 5 * time.Minute, pageIn: dur(-time.Minute)})
	// Past the hard limit: released by the old safety net.
	past := f.newPaymentHold(5, 2, holdOpts{clickPath: true, expiredAgo: 10 * time.Minute, pageIn: dur(-grace - time.Minute)})
	// Old row (no page_expires_at): released exactly as before.
	old := f.newPaymentHold(7, 2, holdOpts{expiredAgo: time.Minute})

	runReservationSweep(t, pool)

	f.assertSeats(alive, "held")
	f.assertSeats(inGrace, "held")
	f.assertSeats(past, "available")
	f.assertSeats(old, "available")
	if s := f.str(`SELECT state FROM reservations WHERE id = $1`, alive.reservationID); s != "draft" {
		t.Fatalf("alive hold state = %s", s)
	}
}

// The re-check under the row lock: a hold whose deadline moved after the poll
// is not released.
func TestReservationSweep_RecheckUnderLock_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	ctx := context.Background()
	// a expired first, b after it; both are polled in one batch.
	a := f.newPaymentHold(1, 1, holdOpts{expiredAgo: 10 * time.Minute})
	b := f.newPaymentHold(2, 1, holdOpts{expiredAgo: time.Minute})

	// Hold the sessions row: the sweep polls a and b, then blocks inside a's
	// per-item transaction (seat release bumps sessions). Meanwhile b is
	// extended, as a conversion or an extension between poll and lock would.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT 1 FROM sessions WHERE id = $1 FOR UPDATE`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	q := gen.New(pool)
	p := hcheckout.NewReservationProcessor(pool, q, quiet()).WithPaymentGrace(grace)
	done := make(chan error, 1)
	go func() {
		_, err := p.ProcessExpiredReservations(ctx, 1000)
		done <- err
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never blocked on the sessions row")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '10 minutes' WHERE id = $1`, b.reservationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.assertSeats(a, "available")
	f.assertSeats(b, "held")
	if s := f.str(`SELECT state FROM reservations WHERE id = $1`, b.reservationID); s != "draft" {
		t.Fatalf("extended hold state = %s, want draft", s)
	}
}

func TestOrderSweep_PayingAndHardLimit_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 8)
	paying := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: time.Minute, orderExpAgo: time.Minute, payingIn: dur(5 * time.Minute)})
	alive := f.newPaymentHold(3, 2, holdOpts{clickPath: true, expiredAgo: time.Minute, orderExpAgo: time.Minute, pageIn: dur(10 * time.Minute)})
	past := f.newPaymentHold(5, 2, holdOpts{clickPath: true, expiredAgo: 10 * time.Minute, orderExpAgo: 10 * time.Minute, pageIn: dur(-grace - time.Minute)})
	old := f.newPaymentHold(7, 2, holdOpts{expiredAgo: time.Minute, orderExpAgo: time.Minute})

	if _, err := ordering.RunExpireSweepWithGrace(context.Background(), gen.New(pool), time.Now().UTC(), grace, 1000, quiet()); err != nil {
		t.Fatalf("order sweep: %v", err)
	}
	for _, c := range []struct {
		name string
		h    paymentHold
		want string
	}{
		{"paying", paying, "pending_payment"},
		{"page alive", alive, "pending_payment"},
		{"past the hard limit", past, "expired"},
		{"old row", old, "expired"},
	} {
		if got := f.str(`SELECT status FROM orders WHERE id = $1`, c.h.orderID); got != c.want {
			t.Fatalf("%s: order status = %s, want %s", c.name, got, c.want)
		}
	}
}

// ── late payment window ─────────────────────────────────────────────────────

func TestLatePaymentWindowBounds_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 4)
	h := f.newPaymentHold(1, 2, holdOpts{clickPath: true, expiredAgo: time.Minute, pageIn: dur(5 * time.Minute), payingIn: dur(8 * time.Minute)})
	paying, page, err := gen.New(pool).GetLatePaymentWindowBounds(context.Background(), h.intentID, h.reservationID)
	if err != nil {
		t.Fatal(err)
	}
	if paying == nil || page == nil || !paying.After(*page) {
		t.Fatalf("paying_until %v page_expires_at %v", paying, page)
	}
	old := f.newPaymentHold(3, 1, holdOpts{expiredAgo: time.Minute})
	paying, page, err = gen.New(pool).GetLatePaymentWindowBounds(context.Background(), old.intentID, old.reservationID)
	if err != nil || paying != nil || page != nil {
		t.Fatalf("old row bounds %v %v (%v), want both NULL", paying, page, err)
	}
}

// ── visitor hash purge ──────────────────────────────────────────────────────

func TestVisitorHashPurge_LiveDB(t *testing.T) {
	pool := livePool(t)
	f := newFixture(t, pool, 6)
	ctx := context.Background()
	oldClosed := f.newPaymentHold(1, 1, holdOpts{clickPath: true, expiredAgo: time.Minute})
	newClosed := f.newPaymentHold(2, 1, holdOpts{clickPath: true, expiredAgo: time.Minute})
	stillOpen := f.newPaymentHold(3, 1, holdOpts{clickPath: true, expiredAgo: -10 * time.Minute})
	for _, h := range []paymentHold{oldClosed, newClosed, stillOpen} {
		if _, err := pool.Exec(ctx, `UPDATE reservations SET visitor_device_hash = '\x01', visitor_ip_hash = '\x02' WHERE id = $1`, h.reservationID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE reservations SET state = 'expired', expired_at = now() - interval '25 hours' WHERE id = $1`, oldClosed.reservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE reservations SET state = 'expired', expired_at = now() - interval '1 hour' WHERE id = $1`, newClosed.reservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := holdclose.RunPurge(ctx, gen.New(pool), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		h      paymentHold
		purged bool
	}{{"closed 25 h ago", oldClosed, true}, {"closed 1 h ago", newClosed, false}, {"open", stillOpen, false}} {
		var dev, ip []byte
		if err := pool.QueryRow(ctx, `SELECT visitor_device_hash, visitor_ip_hash FROM reservations WHERE id = $1`, c.h.reservationID).Scan(&dev, &ip); err != nil {
			t.Fatal(err)
		}
		if (dev == nil && ip == nil) != c.purged {
			t.Fatalf("%s: hashes %x %x, purged want %v", c.name, dev, ip, c.purged)
		}
	}
}

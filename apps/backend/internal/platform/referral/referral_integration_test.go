//go:build integration

// referral_integration_test.go — the "Refer a friend" core against a live
// database migrated to the referral migration, with a FAKE payment module (so
// every provider answer can be scripted) and the real refund engine.
//
// Run with:
//
//	DATABASE_URL=...arena_ci_refer... go test -tags integration ./apps/backend/internal/platform/referral/
package referral_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// ─── fake payment module ────────────────────────────────────────────────────

type fakeModule struct {
	mu      sync.Mutex
	calls   []payments.RefundRequest
	answers []func(payments.RefundRequest) (payments.RefundResult, error)
}

func (m *fakeModule) Descriptor() payments.Descriptor {
	return payments.Descriptor{Name: "referfake", Title: "Fake", Capabilities: payments.Capabilities{
		Refund: true, PartialRefund: true}}
}

func (m *fakeModule) Refund(_ context.Context, req payments.RefundRequest) (payments.RefundResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	if len(m.answers) == 0 {
		return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundSucceeded}, nil
	}
	a := m.answers[0]
	if len(m.answers) > 1 {
		m.answers = m.answers[1:]
	}
	return a(req)
}

func (m *fakeModule) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

type fakeSource struct{ m *fakeModule }

func (s fakeSource) Descriptor(p string) (payments.Descriptor, bool) {
	if p == "referfake" {
		return s.m.Descriptor(), true
	}
	return payments.Descriptor{}, false
}

func (s fakeSource) Build(context.Context, uuid.UUID, string) (payments.Module, error) {
	return s.m, nil
}

func pendingAnswer(req payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundPending}, nil
}

func declinedAnswer(payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, &payments.RefundDeclinedError{Code: "card_closed", Message: "the card is closed"}
}

// ─── recording notifier ─────────────────────────────────────────────────────

type recordingNotifier struct {
	mu   sync.Mutex
	seen []gen.ReferralRewardRow
}

func (n *recordingNotifier) FriendReversed(_ context.Context, r gen.ReferralRewardRow) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = append(n.seen, r)
	return nil
}

// ─── fixture ────────────────────────────────────────────────────────────────

type fixture struct {
	pool                            *pgxpool.Pool
	org, event, session, channel    uuid.UUID
	tier, payment, referrerOrder    uuid.UUID
	tickets                         [2]uuid.UUID
	suffix                          string
	orders                          []uuid.UUID
	program                         uuid.UUID
	module                          *fakeModule
	notifier                        *recordingNotifier
	svc                             *referral.Service
	engine                          *refunds.Engine
	referrerEmail, referrerBuyerNam string
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %.60q: %v", sql, err)
	}
}

// newFixture seeds a published event with one session and a programme, and a
// paid referrer order of two 2500-minor tickets paid by a 5000 payment of
// the fake provider. cap/cashback/friend discount are the programme's percents.
func newFixture(t *testing.T, friendPct, cashbackPct, capPct int) *fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "RF "+suffix, "rf-"+uuid.NewString(), "ES", "en", 1200)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	f := &fixture{pool: pool, org: org.ID, event: uuid.New(), session: uuid.New(), channel: uuid.New(),
		tier: uuid.New(), referrerOrder: uuid.New(), suffix: suffix, program: uuid.New(),
		referrerEmail: "anna-" + suffix + "@example.com", referrerBuyerNam: "Anna Petrova",
		module: &fakeModule{}, notifier: &recordingNotifier{}}
	f.tickets = [2]uuid.UUID{uuid.New(), uuid.New()}
	venue := uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute)
	t.Cleanup(func() { f.cleanup(t) })
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venue, f.org, "V " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{f.event, f.org, "E " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{f.session, f.event, venue, start}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
		  VALUES ($1, $2, 'Stalls', 'fixed', 2500, 'EUR', 1, true)`, []any{f.tier, f.session}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, $2, 100, 2)`, []any{f.session, f.tier}},
		{`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{}'::jsonb)`, []any{f.channel, f.org, "C " + suffix}},
		{`INSERT INTO referral_programs (id, org_id, scope, event_id, friend_discount_pct, cashback_pct, cashback_cap_pct,
		                                max_friends, terms_accepted_at)
		  VALUES ($1, $2, 'event', $3, $4, $5, $6, 5, now())`, []any{f.program, f.org, f.event, friendPct, cashbackPct, capPct}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture %.50q: %v", s.sql, err)
		}
	}

	cs := f.addOrder(t, f.referrerOrder, "paid", 5000, f.referrerEmail, f.referrerBuyerNam, nil)
	for i, tk := range f.tickets {
		f.exec(t, `INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		           VALUES ($1, $2, $3, $4, 'buyer@example.com', $5, $6)`, tk, cs, f.session, f.tier, f.referrerOrder, i)
		f.exec(t, `INSERT INTO order_items (order_id, ordinal, tier_id, ticket_id, unit_price, total) VALUES ($1, $2, $3, $4, 2500, 2500)`,
			f.referrerOrder, i, f.tier, tk)
	}
	ref := "pi_refer_" + uuid.NewString()
	pi, err := q.InsertPaymentIntent(ctx, &cs, f.org, "referfake", &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	f.payment = pi.ID
	f.exec(t, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, pi.ID, ref)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.engine = refunds.New(refunds.Options{DB: pool, Modules: fakeSource{m: f.module}, Audit: audit.NewPGWriter(pool),
		Logger: logger, SweepOrgs: []uuid.UUID{f.org}})
	f.svc = referral.NewService(referral.Options{Pool: pool, Engine: f.engine, Notifier: f.notifier, Logger: logger})
	return f
}

// addOrder inserts a paid-state order (with its checkout session and
// reservation) and returns its checkout session id.
func (f *fixture) addOrder(t *testing.T, id uuid.UUID, status string, total int64, email, name string, promo *uuid.UUID) uuid.UUID {
	t.Helper()
	cs, res := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	           VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, res, f.org, f.channel, f.session)
	f.exec(t, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		cs, f.org, f.channel, res)
	f.exec(t, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                               source, status, currency, subtotal, discount, charge, total, paid_at,
	                               buyer_name, buyer_email, promo_code_id)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', $8, 'EUR', $9, 0, 0, $9, now(), $10, $11, $12)`,
		id, f.org, f.channel, f.event, f.session, cs, res, status, total, name, email, promo)
	f.orders = append(f.orders, id)
	return cs
}

func (f *fixture) cleanup(t *testing.T) {
	ctx := context.Background()
	for _, s := range []string{
		`DELETE FROM referral_rewards WHERE org_id = $1`,
		`UPDATE tickets SET refund_id = NULL WHERE session_id IN (SELECT id FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1))`,
		`DELETE FROM worker_jobs WHERE job_type IN ('referral.payout', 'refund.buyer_email', 'refund.sweep')
		   AND (payload->>'reward_id' IS NOT NULL OR payload->>'batch_id' IN (SELECT id::text FROM refund_batches WHERE org_id = $1))`,
		`DELETE FROM refunds WHERE org_id = $1`,
		`DELETE FROM refund_batches WHERE org_id = $1`,
		`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
		`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
		`DELETE FROM tickets WHERE session_id IN (SELECT id FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1))`,
		`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
		`DELETE FROM orders WHERE org_id = $1`,
		`DELETE FROM payment_intents WHERE org_id = $1`,
		`DELETE FROM checkout_sessions WHERE org_id = $1`,
		`DELETE FROM reservations WHERE org_id = $1`,
		`DELETE FROM promo_codes WHERE org_id = $1`,
		`DELETE FROM referral_programs WHERE org_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $2`,
		`DELETE FROM ticket_tiers WHERE session_id = $2`,
		`DELETE FROM sessions WHERE id = $2`,
		`DELETE FROM events WHERE org_id = $1`,
		`DELETE FROM venues WHERE org_id = $1`,
		`DELETE FROM sales_channels WHERE org_id = $1`,
		`DELETE FROM organizations WHERE id = $1`,
	} {
		var err error
		if regexp.MustCompile(`\$2`).MatchString(s) {
			_, err = f.pool.Exec(ctx, s, f.org, f.session)
		} else {
			_, err = f.pool.Exec(ctx, s, f.org)
		}
		if err != nil {
			t.Logf("cleanup %.60q: %v", s, err)
		}
	}
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %.60q: %v", sql, err)
	}
	return n
}

// issue issues the referrer's personal code.
func (f *fixture) issue(t *testing.T) referral.Code {
	t.Helper()
	c, _, err := referral.IssueCode(context.Background(), f.pool, f.referrerOrder)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	return c
}

// friend adds a paid friend order that used code and records it the way the
// payment transaction does. It returns the order id.
func (f *fixture) friend(t *testing.T, code referral.Code, total int64, email string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	pc := code.PromoCodeID
	f.addOrder(t, id, "paid", total, email, "Friend", &pc)
	f.record(t, id)
	return id
}

func (f *fixture) record(t *testing.T, orderID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ord, err := gen.New(tx).GetOrderByID(ctx, orderID, f.org)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if err := referral.RecordFriendOrderTx(ctx, tx, ord); err != nil {
		t.Fatalf("RecordFriendOrderTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

type rewardView struct {
	ID      uuid.UUID
	State   string
	Reason  string
	Amount  int64
	Reverse string
}

func (f *fixture) rewardOf(t *testing.T, friendOrder uuid.UUID) rewardView {
	t.Helper()
	var v rewardView
	var reason, rev *string
	err := f.pool.QueryRow(context.Background(),
		`SELECT id, state, reason, amount_minor, friend_reversal FROM referral_rewards WHERE friend_order_id = $1`, friendOrder).
		Scan(&v.ID, &v.State, &reason, &v.Amount, &rev)
	if err != nil {
		t.Fatalf("reward of %s: %v", friendOrder, err)
	}
	if reason != nil {
		v.Reason = *reason
	}
	if rev != nil {
		v.Reverse = *rev
	}
	return v
}

func (f *fixture) ticketStatuses(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT status FROM tickets WHERE order_id = $1 ORDER BY ordinal`, f.referrerOrder)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// ─── IssueCode ──────────────────────────────────────────────────────────────

var codeShape = regexp.MustCompile(`^ANNA-[A-HJKMNP-Z2-9]{4}$`)

func TestIssueCode_CreatesOnceAndIsIdempotent(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	first, created, err := referral.IssueCode(ctx, f.pool, f.referrerOrder)
	if err != nil || !created {
		t.Fatalf("first IssueCode = (%+v, created=%v, %v); want a new code", first, created, err)
	}
	if !codeShape.MatchString(first.Code) || first.FriendDiscountPct != 10 || first.CashbackPct != 10 || first.Status != "active" {
		t.Fatalf("code = %+v; want ANNA-XXXX, 10 %%, 10 %%, active", first)
	}
	again, created, err := referral.IssueCode(ctx, f.pool, f.referrerOrder)
	if err != nil || created || again.Code != first.Code || again.PromoCodeID != first.PromoCodeID {
		t.Fatalf("second IssueCode = (%+v, created=%v, %v); want the same code, not created", again, created, err)
	}
	var origin string
	var value int64
	var maxUses, perCustomer *int32
	var sessions []string
	var program uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT origin, discount_value, max_uses, max_uses_per_customer, applies_to_session_ids,
	        referral_program_id FROM promo_codes WHERE id = $1`, first.PromoCodeID).
		Scan(&origin, &value, &maxUses, &perCustomer, &sessions, &program); err != nil {
		t.Fatal(err)
	}
	if origin != "referral" || value != 10 || maxUses == nil || *maxUses != 5 || perCustomer == nil || *perCustomer != 1 ||
		len(sessions) != 1 || sessions[0] != f.session.String() || program != f.program {
		t.Fatalf("promo row = %s %d %v %v %v %s; want a referral code wired to the programme", origin, value, maxUses, perCustomer, sessions, program)
	}
}

func TestIssueCode_ParallelCallsMakeOneCode(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	const n = 12
	codes := make([]referral.Code, n)
	created := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], created[i], errs[i] = referral.IssueCode(context.Background(), f.pool, f.referrerOrder)
		}()
	}
	wg.Wait()
	made := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if codes[i].Code != codes[0].Code {
			t.Fatalf("call %d got %q, call 0 got %q; one order has one code", i, codes[i].Code, codes[0].Code)
		}
		if created[i] {
			made++
		}
	}
	if made != 1 {
		t.Fatalf("%d calls report creating the code; want exactly 1", made)
	}
	if got := f.count(t, `SELECT count(*) FROM promo_codes WHERE org_id = $1 AND origin = 'referral'`, f.org); got != 1 {
		t.Fatalf("%d referral codes stored; want 1", got)
	}
}

func TestIssueCode_Refusals(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()

	// An unpaid order carries no code.
	pending := uuid.New()
	f.addOrder(t, pending, "pending_payment", 5000, "p@example.com", "P", nil)
	if _, _, err := referral.IssueCode(ctx, f.pool, pending); !errors.Is(err, referral.ErrNotEligible) {
		t.Errorf("pending order: err = %v; want ErrNotEligible", err)
	}
	// A free order neither.
	free := uuid.New()
	f.addOrder(t, free, "paid", 0, "f@example.com", "F", nil)
	if _, _, err := referral.IssueCode(ctx, f.pool, free); !errors.Is(err, referral.ErrNotEligible) {
		t.Errorf("free order: err = %v; want ErrNotEligible", err)
	}
	if _, _, err := referral.IssueCode(ctx, f.pool, uuid.New()); !errors.Is(err, referral.ErrOrderNotFound) {
		t.Errorf("unknown order: err = %v; want ErrOrderNotFound", err)
	}

	// A paused programme issues nothing new, but a code issued earlier stays.
	before := f.issue(t)
	f.exec(t, `UPDATE referral_programs SET status = 'paused' WHERE id = $1`, f.program)
	other := uuid.New()
	f.addOrder(t, other, "paid", 4000, "o@example.com", "Olga", nil)
	if _, _, err := referral.IssueCode(ctx, f.pool, other); !errors.Is(err, referral.ErrNoProgram) {
		t.Errorf("paused programme: err = %v; want ErrNoProgram", err)
	}
	if kept, created, err := referral.IssueCode(ctx, f.pool, f.referrerOrder); err != nil || created || kept.Code != before.Code {
		t.Errorf("existing code under a paused programme = (%+v, %v, %v); want it returned unchanged", kept, created, err)
	}

	// A programme past its end issues nothing either.
	f.exec(t, `UPDATE referral_programs SET status = 'active', starts_at = now() - interval '2 days', ends_at = now() - interval '1 day' WHERE id = $1`, f.program)
	if _, _, err := referral.IssueCode(ctx, f.pool, other); !errors.Is(err, referral.ErrNoProgram) {
		t.Errorf("ended period: err = %v; want ErrNoProgram", err)
	}
}

// ─── RecordFriendOrderTx ────────────────────────────────────────────────────

func TestRecordFriendOrder_DueRewardAndPayoutJob(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")

	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateDue || rw.Amount != 300 || rw.Reason != "" {
		t.Fatalf("reward = %+v; want due 300 (10 %% of what the friend paid)", rw)
	}
	var pct, cap int
	_ = f.pool.QueryRow(context.Background(), `SELECT cashback_pct, cap_pct FROM referral_rewards WHERE id = $1`, rw.ID).Scan(&pct, &cap)
	if pct != 10 || cap != 100 {
		t.Fatalf("snapshot = %d/%d; want 10/100", pct, cap)
	}
	jobs := func() int {
		return f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'referral.payout' AND payload->>'reward_id' = $1`, rw.ID.String())
	}
	if jobs() != 1 {
		t.Fatalf("%d payout jobs; want exactly 1 in the payment's transaction", jobs())
	}
	// A replayed payment is a no-op.
	f.record(t, friend)
	if n := f.count(t, `SELECT count(*) FROM referral_rewards WHERE org_id = $1`, f.org); n != 1 || jobs() != 1 {
		t.Fatalf("after a replay: %d rewards, %d jobs; want 1 and 1", n, jobs())
	}
	// An order with an ordinary code, or none, writes nothing.
	f.record(t, f.referrerOrder)
	if n := f.count(t, `SELECT count(*) FROM referral_rewards WHERE org_id = $1`, f.org); n != 1 {
		t.Fatalf("an order without a code wrote a reward")
	}
}

func TestRecordFriendOrder_SelfReferralAndCurrency(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)

	// Same e-mail as the referrer, only differently spelled.
	selfEmail := f.friend(t, code, 3000, "  "+f.referrerEmail+" ")
	if rw := f.rewardOf(t, selfEmail); rw.State != referral.StateCancelled || rw.Reason != "self_referral" || rw.Amount != 0 {
		t.Fatalf("same e-mail: reward = %+v; want cancelled self_referral", rw)
	}
	if n := f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'referral.payout' AND payload->>'reward_id' = $1`,
		f.rewardOf(t, selfEmail).ID.String()); n != 0 {
		t.Fatalf("a self-referral queued a payout")
	}

	// Same phone digits.
	f.exec(t, `UPDATE orders SET buyer_phone = '+34 600 111 222' WHERE id = $1`, f.referrerOrder)
	phoneID := uuid.New()
	pc := code.PromoCodeID
	f.addOrder(t, phoneID, "paid", 3000, "other-"+f.suffix+"@example.com", "X", &pc)
	f.exec(t, `UPDATE orders SET buyer_phone = '0034600111222' WHERE id = $1`, phoneID) // not the same digits: leading 00 differs
	f.exec(t, `UPDATE orders SET buyer_phone = '(34) 600-111-222' WHERE id = $1`, phoneID)
	f.record(t, phoneID)
	if rw := f.rewardOf(t, phoneID); rw.State != referral.StateCancelled || rw.Reason != "self_referral" {
		t.Fatalf("same phone digits: reward = %+v; want self_referral", rw)
	}

	// A real friend in another currency cannot be paid back (no rates).
	usdID := uuid.New()
	f.addOrder(t, usdID, "paid", 3000, "usd-"+f.suffix+"@example.com", "U", &pc)
	f.exec(t, `UPDATE orders SET currency = 'USD' WHERE id = $1`, usdID)
	f.record(t, usdID)
	if rw := f.rewardOf(t, usdID); rw.State != referral.StateNotPossible || rw.Reason != "currency_mismatch" {
		t.Fatalf("other currency: reward = %+v; want not_possible currency_mismatch", rw)
	}
}

func TestRecordFriendOrder_CapAtRecord(t *testing.T) {
	f := newFixture(t, 10, 10, 5) // cap: 5 % of 5000 = 250
	code := f.issue(t)
	a := f.friend(t, code, 3000, "a-"+f.suffix+"@example.com") // 300 -> reduced to 250
	if rw := f.rewardOf(t, a); rw.State != referral.StateDue || rw.Amount != 250 || rw.Reason != "cap_reduced" {
		t.Fatalf("first friend: reward = %+v; want due 250 cap_reduced", rw)
	}
	b := f.friend(t, code, 3000, "b-"+f.suffix+"@example.com") // nothing left
	if rw := f.rewardOf(t, b); rw.State != referral.StateCapped || rw.Reason != "cap_reached" {
		t.Fatalf("second friend: reward = %+v; want capped cap_reached", rw)
	}
	zero := f.friend(t, code, 5, "z-"+f.suffix+"@example.com") // 10 % of 5 is 0
	if rw := f.rewardOf(t, zero); rw.State != referral.StateCapped || rw.Reason != "zero_amount" {
		t.Fatalf("tiny friend: reward = %+v; want capped zero_amount", rw)
	}
}

// ─── Payout ─────────────────────────────────────────────────────────────────

func TestPayout_PartialMoneyOnlyRefund_TicketsStayValid_ScannedAllowed(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	// Both tickets were scanned at the door: R-RF3 allows the cashback.
	f.exec(t, `UPDATE tickets SET used_at = now() WHERE order_id = $1`, f.referrerOrder)
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)

	if err := f.svc.Payout(context.Background(), rw.ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	after := f.rewardOf(t, friend)
	if after.State != referral.StatePaid || after.Amount != 300 {
		t.Fatalf("reward = %+v; want paid 300", after)
	}
	if f.module.callCount() != 1 {
		t.Fatalf("%d provider calls; want 1", f.module.callCount())
	}
	call := f.module.calls[0]
	if call.AmountMinor != 300 || call.PaymentAmountMinor != 5000 || call.Currency != "EUR" {
		t.Fatalf("provider call = %+v; want a partial refund of 300 of 5000 EUR", call)
	}
	var origin string
	var cancels bool
	var state string
	var notify bool
	if err := f.pool.QueryRow(context.Background(), `SELECT r.origin, r.cancel_ticket, r.state, b.notify_buyer
		FROM referral_rewards w JOIN refunds r ON r.id = w.refund_id JOIN refund_batches b ON b.id = r.batch_id
		WHERE w.id = $1`, rw.ID).Scan(&origin, &cancels, &state, &notify); err != nil {
		t.Fatal(err)
	}
	if origin != "referral_cashback" || cancels || state != "succeeded" || notify {
		t.Fatalf("refund = origin %s cancel %v state %s notify %v; want a quiet money-only referral_cashback", origin, cancels, state, notify)
	}
	for i, st := range f.ticketStatuses(t) {
		if st != "active" {
			t.Errorf("ticket %d is %s; the cashback must not cancel it", i, st)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'refund.buyer_email' AND payload->>'batch_id' IN
		(SELECT id::text FROM refund_batches WHERE org_id = $1)`, f.org); n != 0 {
		t.Errorf("%d buyer letters queued; the cashback has its own letter", n)
	}

	// A repeated job is a no-op: no second call, no second refund.
	if err := f.svc.Payout(context.Background(), rw.ID); err != nil {
		t.Fatalf("second Payout: %v", err)
	}
	if f.module.callCount() != 1 || f.count(t, `SELECT count(*) FROM refunds WHERE org_id = $1`, f.org) != 1 {
		t.Fatalf("a repeated payout moved money again")
	}
}

func TestPayout_CapAppliedUnderTheLockAtPayTime(t *testing.T) {
	f := newFixture(t, 10, 10, 8) // cap: 8 % of 5000 = 400
	code := f.issue(t)
	a := f.friend(t, code, 3000, "a-"+f.suffix+"@example.com") // 300
	// A second reward recorded as if before the first was known: 300 again.
	b := uuid.New()
	pc := code.PromoCodeID
	f.addOrder(t, b, "paid", 3000, "b-"+f.suffix+"@example.com", "B", &pc)
	f.exec(t, `INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
	               amount_minor, currency, cashback_pct, cap_pct, state)
	           VALUES ($1, $2, $3, $4, $5, 300, 'EUR', 10, 8, 'due')`, f.org, f.program, code.PromoCodeID, f.referrerOrder, b)

	ctx := context.Background()
	if err := f.svc.Payout(ctx, f.rewardOf(t, a).ID); err != nil {
		t.Fatalf("payout a: %v", err)
	}
	if err := f.svc.Payout(ctx, f.rewardOf(t, b).ID); err != nil {
		t.Fatalf("payout b: %v", err)
	}
	if ra, rb := f.rewardOf(t, a), f.rewardOf(t, b); ra.State != referral.StatePaid || ra.Amount != 300 || rb.State != referral.StatePaid || rb.Amount != 100 {
		t.Fatalf("a = %+v, b = %+v; want 300 paid, then 100 (the cap's room left) paid", ra, rb)
	}
	// A third friend finds the cap used up.
	c := uuid.New()
	f.addOrder(t, c, "paid", 3000, "c-"+f.suffix+"@example.com", "C", &pc)
	f.exec(t, `INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
	               amount_minor, currency, cashback_pct, cap_pct, state)
	           VALUES ($1, $2, $3, $4, $5, 300, 'EUR', 10, 8, 'due')`, f.org, f.program, code.PromoCodeID, f.referrerOrder, c)
	if err := f.svc.Payout(ctx, f.rewardOf(t, c).ID); err != nil {
		t.Fatalf("payout c: %v", err)
	}
	if rc := f.rewardOf(t, c); rc.State != referral.StateCapped || rc.Reason != "cap_reached" {
		t.Fatalf("c = %+v; want capped cap_reached", rc)
	}
	if f.module.callCount() != 2 {
		t.Fatalf("%d provider calls; want 2 (the capped reward sends nothing)", f.module.callCount())
	}
}

func TestPayout_ProviderRefusalIsNotPossible_NoOtherCard(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){declinedAnswer}
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	if err := f.svc.Payout(context.Background(), f.rewardOf(t, friend).ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateNotPossible || rw.Reason != "card_closed" || rw.Amount != 0 {
		t.Fatalf("reward = %+v; want not_possible card_closed (R-RF5: the money stays with the organizer)", rw)
	}
	for i, st := range f.ticketStatuses(t) {
		if st != "active" {
			t.Errorf("ticket %d is %s after a refused cashback", i, st)
		}
	}
	if f.module.callCount() != 1 {
		t.Fatalf("%d provider calls; want 1 and no other card", f.module.callCount())
	}
}

func TestPayout_FriendRefundedBeforeThePayoutCancelsIt(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, friend)
	if err := f.svc.Payout(context.Background(), f.rewardOf(t, friend).ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	if rw := f.rewardOf(t, friend); rw.State != referral.StateCancelled || rw.Reason != "friend_refunded" {
		t.Fatalf("reward = %+v; want cancelled friend_refunded", rw)
	}
	if f.module.callCount() != 0 {
		t.Fatalf("money was sent for a friend who got theirs back")
	}
}

func TestPayout_PendingRefundIsSentThenReadBackAsPaid(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){pendingAnswer}
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	ctx := context.Background()
	if err := f.svc.Payout(ctx, f.rewardOf(t, friend).ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	if rw := f.rewardOf(t, friend); rw.State != referral.StateSent || rw.Amount != 300 {
		t.Fatalf("reward = %+v; want sent 300", rw)
	}
	// The engine's own sweep (or the provider) finished the refund.
	f.exec(t, `UPDATE refunds SET state = 'succeeded', succeeded_at = now() WHERE org_id = $1`, f.org)
	rep, err := f.svc.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.ReadBack != 1 {
		t.Fatalf("sweep report = %+v; want 1 read back", rep)
	}
	if rw := f.rewardOf(t, friend); rw.State != referral.StatePaid {
		t.Fatalf("reward = %+v; want paid after the read-back", rw)
	}
}

// ─── Sweep ──────────────────────────────────────────────────────────────────

func TestSweep_RequeuesAStuckDueRewardOnce(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	jobs := func() int {
		return f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'referral.payout' AND payload->>'reward_id' = $1`, rw.ID.String())
	}
	ctx := context.Background()

	// Young: its own job is still on the way.
	if rep, err := f.svc.Sweep(ctx); err != nil || rep.Requeued != 0 {
		t.Fatalf("young reward: report %+v err %v; want nothing requeued", rep, err)
	}
	// Old, with its job lost: queued again, once.
	f.exec(t, `DELETE FROM worker_jobs WHERE job_type = 'referral.payout' AND payload->>'reward_id' = $1`, rw.ID.String())
	f.exec(t, `UPDATE referral_rewards SET created_at = now() - interval '10 minutes' WHERE id = $1`, rw.ID)
	if rep, err := f.svc.Sweep(ctx); err != nil || rep.Requeued != 1 || jobs() != 1 {
		t.Fatalf("lost job: report %+v err %v jobs %d; want one new job", rep, err, jobs())
	}
	if rep, err := f.svc.Sweep(ctx); err != nil || rep.Requeued != 0 || jobs() != 1 {
		t.Fatalf("job pending: report %+v err %v jobs %d; want no second job", rep, err, jobs())
	}
}

func TestSweep_FriendReversalIsRecordedNotifiedAndTwoPauseTheCode(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	ctx := context.Background()
	a := f.friend(t, code, 3000, "a-"+f.suffix+"@example.com")
	b := f.friend(t, code, 3000, "b-"+f.suffix+"@example.com")
	for _, id := range []uuid.UUID{a, b} {
		if err := f.svc.Payout(ctx, f.rewardOf(t, id).ID); err != nil {
			t.Fatalf("Payout: %v", err)
		}
	}
	// Nothing happened to the friends yet.
	if rep, err := f.svc.Sweep(ctx); err != nil || rep.Reversed != 0 || rep.Paused != 0 {
		t.Fatalf("quiet sweep: %+v %v", rep, err)
	}
	// One friend gets the money back: recorded, the organizer is told, the code lives.
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, a)
	rep, err := f.svc.Sweep(ctx)
	if err != nil || rep.Reversed != 1 || rep.Notified != 1 || rep.Paused != 0 {
		t.Fatalf("first reversal: %+v %v; want 1 recorded, 1 notified, none paused", rep, err)
	}
	if rw := f.rewardOf(t, a); rw.Reverse != "refunded" || rw.State != referral.StatePaid {
		t.Fatalf("reward a = %+v; want paid with friend_reversal refunded", rw)
	}
	if len(f.notifier.seen) != 1 || f.notifier.seen[0].FriendOrderID == nil || *f.notifier.seen[0].FriendOrderID != a {
		t.Fatalf("notifier saw %d notices; want 1 for friend a", len(f.notifier.seen))
	}
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM promo_codes WHERE id = $1`, code.PromoCodeID).Scan(&status)
	if status != "active" {
		t.Fatalf("code is %s after one reversal; want active", status)
	}
	// The second one pauses the code. The first is not announced again.
	f.exec(t, `UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, b)
	rep, err = f.svc.Sweep(ctx)
	if err != nil || rep.Reversed != 1 || rep.Notified != 1 || rep.Paused != 1 {
		t.Fatalf("second reversal: %+v %v; want 1 recorded, 1 notified, 1 paused", rep, err)
	}
	if rw := f.rewardOf(t, b); rw.Reverse != "partially_refunded" {
		t.Fatalf("reward b = %+v; want friend_reversal partially_refunded", rw)
	}
	_ = f.pool.QueryRow(ctx, `SELECT status FROM promo_codes WHERE id = $1`, code.PromoCodeID).Scan(&status)
	if status != "paused" || len(f.notifier.seen) != 2 {
		t.Fatalf("code is %s with %d notices; want paused and 2", status, len(f.notifier.seen))
	}
	if rep, err = f.svc.Sweep(ctx); err != nil || rep.Reversed != 0 || rep.Notified != 0 || rep.Paused != 0 {
		t.Fatalf("settled sweep: %+v %v; want nothing new", rep, err)
	}
}

func TestSweep_DeactivatesTheCodeOfARefundedReferrerOrder(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	ctx := context.Background()
	f.exec(t, `UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, f.referrerOrder)
	if rep, err := f.svc.Sweep(ctx); err != nil || rep.Deactivated != 0 {
		t.Fatalf("partly refunded referrer: %+v %v; the code must live", rep, err)
	}
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, f.referrerOrder)
	rep, err := f.svc.Sweep(ctx)
	if err != nil || rep.Deactivated != 1 {
		t.Fatalf("refunded referrer: %+v %v; want the code deactivated", rep, err)
	}
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM promo_codes WHERE id = $1`, code.PromoCodeID).Scan(&status)
	if status != "inactive" {
		t.Fatalf("code is %s; want inactive", status)
	}
}

func TestSelfReferral_IdentitiesOfTheCustomerCount(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	ca, cb := uuid.New(), uuid.New()
	for _, c := range []uuid.UUID{ca, cb} {
		f.exec(t, `INSERT INTO customers (id) VALUES ($1)`, c)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `UPDATE orders SET customer_id = NULL WHERE org_id = $1`, f.org)
		_, _ = f.pool.Exec(ctx, `DELETE FROM customer_identities WHERE customer_id = ANY($1)`, []uuid.UUID{ca, cb})
		_, _ = f.pool.Exec(ctx, `DELETE FROM customers WHERE id = ANY($1)`, []uuid.UUID{ca, cb})
	})
	friend := uuid.New()
	f.addOrder(t, friend, "paid", 3000, "unrelated-"+f.suffix+"@example.com", "F", nil)
	same := func() bool {
		got, err := referral.IsSelfReferral(ctx, f.pool, f.referrerOrder, friend)
		if err != nil {
			t.Fatalf("IsSelfReferral: %v", err)
		}
		return got
	}

	if same() {
		t.Fatal("unrelated guests are not the same person")
	}
	// Two different customers with nothing in common.
	f.exec(t, `UPDATE orders SET customer_id = $2 WHERE id = $1`, f.referrerOrder, ca)
	f.exec(t, `UPDATE orders SET customer_id = $2 WHERE id = $1`, friend, cb)
	if same() {
		t.Fatal("two different customers are not the same person")
	}
	// The referrer's customer once paid with the e-mail the friend uses now.
	hidden := "hidden-" + f.suffix + "@example.com"
	f.exec(t, `INSERT INTO customer_identities (customer_id, kind, value_normalized) VALUES ($1, 'email', $2)`, ca, hidden)
	f.exec(t, `UPDATE orders SET buyer_email = $2 WHERE id = $1`, friend, " "+hidden+" ")
	if !same() {
		t.Fatal("the friend pays with an e-mail the referrer's customer owns: the same person")
	}
	// The friend's customer owns the referrer's phone.
	f.exec(t, `UPDATE orders SET buyer_email = $2 WHERE id = $1`, friend, "unrelated-"+f.suffix+"@example.com")
	if same() {
		t.Fatal("back to unrelated")
	}
	f.exec(t, `UPDATE orders SET buyer_phone = '+34 600 111 222' WHERE id = $1`, f.referrerOrder)
	f.exec(t, `INSERT INTO customer_identities (customer_id, kind, value_normalized) VALUES ($1, 'phone', '+34600111222')`, cb)
	if !same() {
		t.Fatal("the friend's customer owns the referrer's phone: the same person")
	}
	// The same customer on both orders.
	f.exec(t, `UPDATE orders SET customer_id = $2 WHERE id = $1`, friend, ca)
	f.exec(t, `DELETE FROM customer_identities WHERE customer_id = $1`, cb)
	f.exec(t, `UPDATE orders SET buyer_phone = NULL WHERE id = $1`, f.referrerOrder)
	if !same() {
		t.Fatal("the same customer on both orders is the same person")
	}
}

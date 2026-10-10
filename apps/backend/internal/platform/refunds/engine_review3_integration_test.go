//go:build integration

// engine_review3_integration_test.go — the findings of the THIRD PAY-03
// review, each pinned against a live database (same fixture and fake module
// as engine_integration_test.go). Every test here fails on the code before
// its fix.
package refunds_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// addTicket adds a third 2500 ticket to the fixture's order and grows the
// payment and the order to match, so a whole-order refund covers it.
func (f *fixture) addTicket(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM outbox_events WHERE aggregate_id = $1`, id.String())
	})
	f.exec(t, `INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		VALUES ($1, $2, $3, $4, 'buyer@example.com', $5, 2)`, id, f.cs, f.session, f.tier, f.order)
	f.exec(t, `INSERT INTO order_items (order_id, ordinal, tier_id, ticket_id, unit_price, total) VALUES ($1, 2, $2, $3, 2500, 2500)`,
		f.order, f.tier, id)
	f.exec(t, `UPDATE payment_intents SET amount = amount + 2500 WHERE id = $1`, f.payment)
	f.exec(t, `UPDATE orders SET subtotal = subtotal + 2500, total = total + 2500 WHERE id = $1`, f.order)
	f.exec(t, `UPDATE inventory_ledger SET capacity_sold = capacity_sold + 1 WHERE session_id = $1`, f.session)
	return id
}

// publishCounter counts v1.ticket.refunded per ticket.
type publishCounter struct {
	mu  sync.Mutex
	per map[string]int
}

func (p *publishCounter) fn(_ context.Context, ids []string, _, _, _ string, _ int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.per == nil {
		p.per = map[string]int{}
	}
	for _, id := range ids {
		p.per[id]++
	}
}

func (p *publishCounter) get(id uuid.UUID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.per[id.String()]
}

func (p *publishCounter) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.per {
		n += v
	}
	return n
}

// TestEngine_OrderRefundFinishesEveryTicketAfterAPartialCancel (H1): a
// whole-order refund cancels ticket 1, fails once on ticket 2 (a deadlock)
// and cancels ticket 3. The sweep's repair must still select it — ticket 1
// being linked to the refund does not mean the order is done — cancel
// ticket 2, and publish v1.ticket.refunded exactly once per ticket.
func TestEngine_OrderRefundFinishesEveryTicketAfterAPartialCancel(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	third := f.addTicket(t)
	m := &fakeModule{partial: true}
	pub := &publishCounter{}
	var failedOnce atomic.Bool
	e := f.engineWith(m, true, func(o *refunds.Options) {
		inner := o.CancelTicket
		o.CancelTicket = func(ctx context.Context, req refunds.CancelRequest) error {
			if req.TicketID == f.tickets[1] && failedOnce.CompareAndSwap(false, true) {
				return errors.New("ERROR: deadlock detected (SQLSTATE 40P01)")
			}
			return inner(ctx, req)
		}
		o.PublishRefunded = pub.fn
	})
	id := f.rawRefund(t, f.payment, 7500, "provider_pending", "")
	r, err := e.Drive(ctx, id)
	if err != nil || r.State != refunds.StateSucceeded {
		t.Fatalf("Drive: %v %s", err, r.State)
	}
	for i, want := range []string{"cancelled", "active"} {
		if st, _ := f.ticketStatus(t, f.tickets[i]); st != want {
			t.Fatalf("ticket %d = %s; want %s", i, st, want)
		}
	}
	if st, _ := f.ticketStatus(t, third); st != "cancelled" {
		t.Fatalf("ticket 3 = %s; the loop must go on past a failed ticket", st)
	}
	if pub.total() != 0 {
		t.Fatalf("published before every ticket was cancelled: %v", pub.per)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE id = $1`, id)
	rep, err := e.Sweep(ctx, time.Now(), nil)
	if err != nil || rep.Repaired != 1 {
		t.Fatalf("sweep: %+v %v; the repair must select the half-cancelled order", rep, err)
	}
	for _, tk := range []uuid.UUID{f.tickets[0], f.tickets[1], third} {
		if st, link := f.ticketStatus(t, tk); st != "cancelled" || link == nil || *link != id {
			t.Fatalf("ticket %s = %s link %v; want cancelled by the refund", tk, st, link)
		}
		if pub.get(tk) != 1 {
			t.Fatalf("ticket %s published %d times; want exactly once", tk, pub.get(tk))
		}
	}
	if rep, err := e.Sweep(ctx, time.Now(), nil); err != nil || rep.Repaired != 0 || pub.total() != 3 {
		t.Fatalf("second pass: %+v %v published %v", rep, err, pub.per)
	}
}

// TestEngine_OrderRefundPendingThenSucceededPublishes (M4): a whole-order
// refund the provider accepted as PENDING cancels the tickets at once; when
// the lookup later reports it succeeded, v1.ticket.refunded goes out for
// every ticket — once.
func TestEngine_OrderRefundPendingThenSucceededPublishes(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	pub := &publishCounter{}
	e := f.engineWith(m, true, func(o *refunds.Options) { o.PublishRefunded = pub.fn })
	id := f.rawRefund(t, f.payment, 5000, "provider_pending", "")
	r, err := e.Drive(ctx, id)
	if err != nil || r.State != refunds.StateProviderPending || r.ProviderRefundID == nil {
		t.Fatalf("Drive: %v %+v", err, r)
	}
	for i := range f.tickets {
		if st, _ := f.ticketStatus(t, f.tickets[i]); st != "cancelled" {
			t.Fatalf("ticket %d = %s; an accepted refund cancels it", i, st)
		}
	}
	if pub.total() != 0 {
		t.Fatalf("published while still pending: %v", pub.per)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '11 minutes' WHERE id = $1`, id)
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	if rep, err := e.Sweep(ctx, time.Now(), nil); err != nil || rep.LookedUp != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	for i := range f.tickets {
		if pub.get(f.tickets[i]) != 1 {
			t.Fatalf("ticket %d published %d times; want once when the refund succeeded", i, pub.get(f.tickets[i]))
		}
	}
	got, _ := e.GetRefund(ctx, id)
	if err := e.SettleForTest(ctx, got); err != nil || pub.total() != 2 {
		t.Fatalf("a repeated settle published again: %v %v", pub.per, err)
	}
}

// TestEngine_RePostCutoffCountsFromTheFirstAttempt (M2): a refund is never
// re-POSTed once its FIRST provider attempt is 23 hours old (Stripe keeps
// an idempotency key for 24 hours from the first request); a refund never
// attempted counts from its approval, or its creation when approved_at is
// NULL. 22h59m re-POSTs, 23h01m is parked.
func TestEngine_RePostCutoffCountsFromTheFirstAttempt(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	stale := `provider_attempted_at = now() - interval '2 minutes', provider_attempts = 1, provider_status = 'unknown_outcome', `
	rows := map[string]struct {
		set    string
		repost bool
	}{
		// Approved 30 hours ago, first sent 22h59m ago: still inside the key's life.
		"first attempt 22h59m":                      {stale + `approved_at = now() - interval '30 hours', first_attempted_at = now() - interval '22 hours 59 minutes'`, true},
		"first attempt 23h01m":                      {stale + `approved_at = now() - interval '23 hours 1 minute', first_attempted_at = now() - interval '23 hours 1 minute'`, false},
		"no attempt, approved NULL, created 22h59m": {`approved_at = NULL, created_at = now() - interval '22 hours 59 minutes'`, true},
		"no attempt, approved NULL, created 23h01m": {`approved_at = NULL, created_at = now() - interval '23 hours 1 minute'`, false},
	}
	ids := map[string]uuid.UUID{}
	for name, row := range rows {
		ids[name] = f.rawRefund(t, f.extraPayment(t), 100, "provider_pending", row.set)
	}
	if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	called := map[string]bool{}
	for _, c := range m.calls {
		called[c.key] = true
	}
	for name, row := range rows {
		got := f.refundState(t, ids[name])
		if row.repost {
			if !called[ids[name].String()] || got.State != refunds.StateSucceeded {
				t.Errorf("%s: called %v state %s; want re-POSTed and succeeded", name, called[ids[name].String()], got.State)
			}
		} else if called[ids[name].String()] || got.State != refunds.StateManualReview {
			t.Errorf("%s: called %v state %s; want parked, never re-POSTed", name, called[ids[name].String()], got.State)
		}
	}
}

// flakyNotifier fails its first delivery, then delivers.
type flakyNotifier struct {
	mu     sync.Mutex
	failed bool
	texts  []string
}

func (n *flakyNotifier) SendConfirmed(_ context.Context, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.failed {
		n.failed = true
		return errors.New("telegram: 502 Bad Gateway")
	}
	n.texts = append(n.texts, text)
	return nil
}

func (n *flakyNotifier) Send(ctx context.Context, text string) error {
	return n.SendConfirmed(ctx, text)
}

// TestEngine_UndeliveredAlertIsRetried (M1): an alert is cleared only after
// a CONFIRMED delivery — a Telegram failure leaves it owed for the next
// pass — and a pass sends at most 15.
func TestEngine_UndeliveredAlertIsRetried(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	e := f.engine(&fakeModule{partial: true}, true)
	for i := 0; i < 20; i++ {
		f.rawRefund(t, f.payment, 1, "manual_review", `failure_code = 'alert_probe', alert_due_at = now()`)
	}
	n := &flakyNotifier{}
	for pass, want := range []int{0, 15, 5, 0} {
		rep, err := e.Sweep(ctx, time.Now(), n)
		if err != nil || rep.Alerted != want {
			t.Fatalf("pass %d: %+v %v; want %d alerts", pass, rep, err, want)
		}
	}
	if len(n.texts) != 20 {
		t.Fatalf("delivered %d alerts; want all 20 after the outage", len(n.texts))
	}
	var owed int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE org_id = $1 AND alert_due_at IS NOT NULL`, f.org).Scan(&owed)
	if owed != 0 {
		t.Fatalf("%d alerts still owed", owed)
	}
}

// TestEngine_LateAcceptanceAfterAnAlertSendsAFollowUp (M5): an operator
// alerted about a parked refund hears again when the provider's late
// acceptance takes it out of manual_review.
func TestEngine_LateAcceptanceAfterAnAlertSendsAFollowUp(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	n := &recordingNotifier{}
	m.beforeRefund = func(req payments.RefundRequest) bool {
		f.exec(t, `UPDATE refunds SET approved_at = now() - interval '25 hours', created_at = now() - interval '25 hours',
			first_attempted_at = now() - interval '24 hours', provider_attempted_at = now() - interval '2 minutes' WHERE id = $1`, req.IdempotencyKey)
		if rep, err := e.Sweep(ctx, time.Now(), n); err != nil || rep.Stuck != 1 || rep.Alerted != 1 {
			t.Errorf("park during the call: %+v %v", rep, err)
		}
		return true
	}
	res, err := e.CreateBatch(ctx, f.batch("k-follow-up", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("CreateBatch: %v", err)
	}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	if len(n.texts) != 2 || !strings.Contains(n.texts[1], "accepted_late") || !strings.Contains(n.texts[1], res.Refunds[0].ID.String()) {
		t.Fatalf("alerts = %v; want the park alert and a follow-up about the late acceptance", n.texts)
	}
}

// TestEngine_SkippedRetriesAreNotUnansweredCalls (L1): a refund that is not
// claimable (another refund of its payment is mid-call) is skipped; it
// neither counts as an unanswered call nor eats the retry cap.
func TestEngine_SkippedRetriesAreNotUnansweredCalls(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	for i := 0; i < 6; i++ {
		pay := f.extraPayment(t)
		f.rawRefund(t, pay, 100, "provider_pending", `provider_attempted_at = now() - interval '2 minutes', provider_attempts = 1`)
		f.rawRefund(t, pay, 100, "provider_pending", `provider_attempted_at = now(), provider_attempts = 1`) // in flight
	}
	rep, err := e.Sweep(ctx, time.Now(), nil)
	if err != nil || rep.Calls != 0 || rep.Unknown != 0 || m.callCount() != 0 {
		t.Fatalf("sweep: %+v %v calls %d; skipped retries are neither calls nor unanswered", rep, err, m.callCount())
	}
}

// TestEngine_ConfigAndChargeRefRefusalsNeedReview (L3): no usable payment
// configuration, or a charge reference the provider cannot find, is not a
// provider verdict on the refund — it goes to manual_review with an alert
// even on the first attempt.
func TestEngine_ConfigAndChargeRefRefusalsNeedReview(t *testing.T) {
	pool := testPool(t)
	cases := map[string]func(m *fakeModule){
		"config": func(m *fakeModule) {
			m.buildErrs = []error{&refunds.ConfigError{Code: "payment.provider_not_configured", Message: "no config"}}
		},
		"charge ref": func(m *fakeModule) {
			m.resolveErr = &payments.RefundDeclinedError{Code: "charge_ref_missing", Message: "the checkout session has no payment intent"}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, pool, "pay03fake")
			m := &fakeModule{partial: true}
			setup(m)
			e := f.engine(m, true)
			res, err := e.CreateBatch(context.Background(), f.batch("k-l3", true, refunds.Item{TicketID: f.tickets[0]}))
			if err != nil {
				t.Fatal(err)
			}
			if r := res.Refunds[0]; r.State != refunds.StateManualReview || r.ProviderAttempts != 1 {
				t.Fatalf("refund = %s attempts %d; want manual_review on the first attempt", r.State, r.ProviderAttempts)
			}
			n := &recordingNotifier{}
			if _, err := e.Sweep(context.Background(), time.Now(), n); err != nil || len(n.texts) != 1 {
				t.Fatalf("alerts %v %v", n.texts, err)
			}
		})
	}
}

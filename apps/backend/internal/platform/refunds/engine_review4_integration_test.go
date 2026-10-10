//go:build integration

// engine_review4_integration_test.go — the findings of the FOURTH PAY-03
// review, each pinned against a live database (same fixture and fake module
// as engine_integration_test.go). Every test here fails on the code before
// its fix.
package refunds_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// ticketCancelRefund reproduces what POST /v1/tickets/{id}/cancel with
// refund_mode=automatic leaves behind (htickets cancel.go): the ticket is
// cancelled FIRST, then a ticket-less requested refund of amount is written
// with requested_by "ticket.cancel:<ticket>" and linked from the ticket.
func (f *fixture) ticketCancelRefund(t *testing.T, ticket uuid.UUID, amount int64) uuid.UUID {
	t.Helper()
	f.exec(t, `UPDATE tickets SET status = 'cancelled', updated_at = now() WHERE id = $1`, ticket)
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO refunds
		(payment_intent_id, org_id, amount, currency, reason, requested_by, cancelled_ticket_id)
		VALUES ($1, $2, $3, 'EUR', 'adult ticket cancelled', $4, $5) RETURNING id`,
		f.payment, f.org, amount, refunds.TicketCancelRequestedByPrefix+ticket.String(), ticket).Scan(&id); err != nil {
		t.Fatalf("ticket-cancel refund: %v", err)
	}
	f.exec(t, `UPDATE tickets SET refund_id = $1, refund_date = now(), refund_price = $2 WHERE id = $3`, id, amount, ticket)
	return id
}

// TestEngine_TicketCancelRefundCoversOnlyItsTicket (H-1): an adult ticket
// of 50 EUR carries the whole payment (its fee share included) and a free
// child ticket rides along in the same checkout. The operator cancels the
// adult ticket with an automatic refund of 50: the refund equals the whole
// payment, but it speaks for the adult ticket only. The child ticket must
// stay valid — not cancelled, not released, not announced as refunded —
// and the sweep must not chase it either.
func TestEngine_TicketCancelRefundCoversOnlyItsTicket(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	adult, child := f.tickets[0], f.tickets[1]
	f.exec(t, `UPDATE order_items SET total = CASE WHEN ticket_id = $1 THEN 5000 ELSE 0 END WHERE order_id = $2`, adult, f.order)
	id := f.ticketCancelRefund(t, adult, 5000)
	f.approveNow(t, id)

	pub := &publishCounter{}
	m := &fakeModule{partial: true}
	e := f.engineWith(m, true, func(o *refunds.Options) { o.PublishRefunded = pub.fn })
	r, err := e.Drive(ctx, id)
	if err != nil || r.State != refunds.StateSucceeded {
		t.Fatalf("Drive: %v %s", err, r.State)
	}
	if st, link := f.ticketStatus(t, child); st != "active" || link != nil {
		t.Fatalf("free child ticket = %s link %v; a refund of the adult ticket must leave it valid", st, link)
	}
	if pub.get(adult) != 1 || pub.get(child) != 0 {
		t.Fatalf("published = %v; want v1.ticket.refunded for the adult ticket only", pub.per)
	}
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&status)
	if status != "partially_refunded" {
		t.Fatalf("order = %s; want partially_refunded (the child ticket is still valid)", status)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE id = $1`, id)
	rep, err := e.Sweep(ctx, time.Now(), nil)
	if err != nil || rep.Repaired != 0 {
		t.Fatalf("sweep: %+v %v; the repair must not chase the child ticket", rep, err)
	}
	if st, _ := f.ticketStatus(t, child); st != "active" {
		t.Fatalf("free child ticket = %s after the sweep", st)
	}
	if pub.total() != 1 {
		t.Fatalf("published = %v after the sweep", pub.per)
	}
}

// TestEngine_PartialTicketCancelRefundHoldsNothing (H-1, the partial
// branch): a ticket-cancel refund of LESS than the payment is attributed to
// its ticket even when the link on the ticket failed (it is best-effort in
// the route), so the order's other tickets are not put on review hold.
func TestEngine_PartialTicketCancelRefundHoldsNothing(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	id := f.ticketCancelRefund(t, f.tickets[0], 2500)
	f.exec(t, `UPDATE tickets SET refund_id = NULL WHERE id = $1`, f.tickets[0]) // the link failed
	f.approveNow(t, id)
	r, err := f.engine(&fakeModule{partial: true}, true).Drive(ctx, id)
	if err != nil || r.State != refunds.StateSucceeded {
		t.Fatalf("Drive: %v %s", err, r.State)
	}
	var hold bool
	_ = f.pool.QueryRow(ctx, `SELECT review_hold FROM tickets WHERE id = $1`, f.tickets[1]).Scan(&hold)
	if hold {
		t.Fatal("the other ticket was put on review hold for a refund that names its own ticket")
	}
	if st, _ := f.ticketStatus(t, f.tickets[1]); st != "active" {
		t.Fatalf("other ticket = %s", st)
	}
}

// TestEngine_ReplacementIsNotSentWhenALateAcceptanceTookItsMoney (M-b):
// refund A of ticket 1 is marked failed while its provider call is in
// flight, an operator creates the replacement B (not approved yet), and
// then A's provider accepts after all — A is parked over budget. When B is
// approved and driven, it must NOT reach the provider: the buyer already
// got the money back through A.
func TestEngine_ReplacementIsNotSentWhenALateAcceptanceTookItsMoney(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	var (
		once sync.Once
		b    refunds.BatchResult
	)
	m.beforeRefund = func(req payments.RefundRequest) bool {
		once.Do(func() {
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator' WHERE id = $1`, req.IdempotencyKey)
			in := f.batch("k-replacement", true, refunds.Item{TicketID: f.tickets[0]})
			in.Approved = false // waits for an approval
			var err error
			if b, err = e.CreateBatch(ctx, in); err != nil {
				t.Errorf("replacement: %v", err)
			}
		})
		return true
	}
	a, err := e.CreateBatch(ctx, f.batch("k-original", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Refunds[0]; got.State != refunds.StateManualReview || deref(got.FailureCode) != "late_acceptance_over_budget" {
		t.Fatalf("A = %s / %s; want parked over budget", got.State, deref(got.FailureCode))
	}
	if len(b.Refunds) != 1 || b.Refunds[0].State != refunds.StateRequested {
		t.Fatalf("B = %+v; want one requested refund", b.Refunds)
	}
	bid := b.Refunds[0].ID
	f.approveNow(t, bid)
	got, err := e.Drive(ctx, bid)
	if err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 1 {
		t.Fatalf("provider calls = %d; B must not be sent: A's acceptance already returned the money", m.callCount())
	}
	if got.State != refunds.StateManualReview || deref(got.FailureCode) != "budget_taken_by_another_refund" {
		t.Fatalf("B = %s / %s; want parked for a human", got.State, deref(got.FailureCode))
	}
	// Fifth review, M-2: B was never sent, so A is the ONE refund of ticket
	// 1 and its money really went back — A takes the cancellation over and
	// the ticket stops admitting. B gives its cancellation up, so it blocks
	// nothing.
	aid := a.Refunds[0].ID
	if st, link := f.ticketStatus(t, f.tickets[0]); st != "cancelled" || link == nil || *link != aid {
		t.Fatalf("ticket 1 = %s link %v; want cancelled through A, whose money went back", st, link)
	}
	if ra := f.refundState(t, aid); ra.State != refunds.StateSucceeded || !ra.CancelTicket || ra.FailureCode != nil {
		t.Fatalf("A = %s cancel %v code %s; want succeeded and owning the cancellation", ra.State, ra.CancelTicket, deref(ra.FailureCode))
	}
	if rb := f.refundState(t, bid); rb.CancelTicket || strings.Contains(deref(rb.FailureReason), "still valid") {
		t.Fatalf("B cancel %v reason %q; want no cancellation and a reason naming the refund that cancels the ticket",
			rb.CancelTicket, deref(rb.FailureReason))
	}
	n := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(n.texts, "\n")
	if !strings.Contains(joined, bid.String()) || !strings.Contains(joined, "budget_taken_by_another_refund") {
		t.Fatalf("alerts = %v; want one about B", n.texts)
	}
	if !strings.Contains(joined, aid.String()) || !strings.Contains(joined, "accepted by the provider late") {
		t.Fatalf("alerts = %v; want the follow-up that A was accepted late and cancels its ticket", n.texts)
	}
	// An ordinary second refund of the OTHER ticket is unaffected.
	other, err := e.CreateBatch(ctx, f.batch("k-other", true, refunds.Item{TicketID: f.tickets[1]}))
	if err != nil || other.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("refund of ticket 2: %v %+v", err, other.Refunds)
	}
}

// TestEngine_SettleOutlivesItsCallerContext (M-a): the cancellations of a
// big order outlast the caller's context. The order projection and the
// v1.ticket.refunded publish must still happen — on their own deadline, or
// through the sweep's repair, which also picks up a succeeded refund whose
// publish or projection is missing.
func TestEngine_SettleOutlivesItsCallerContext(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	pub := &publishCounter{}
	m := &fakeModule{partial: true}
	e := f.engineWith(m, true, func(o *refunds.Options) {
		o.CallTimeout = 300 * time.Millisecond // the drive's whole apply budget
		inner := o.CancelTicket
		o.CancelTicket = func(ctx context.Context, req refunds.CancelRequest) error {
			time.Sleep(400 * time.Millisecond) // a slow cancellation
			return inner(ctx, req)
		}
		o.PublishRefunded = pub.fn
	})
	res, err := e.CreateBatch(ctx, f.batch("k-slow", true, refunds.Item{TicketID: f.tickets[0]}, refunds.Item{TicketID: f.tickets[1]}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Refunds {
		if r.State != refunds.StateSucceeded {
			t.Fatalf("refund %s = %s", r.ID, r.State)
		}
	}
	// Whatever the first settle managed, a sweep finishes the rest.
	for i := 0; i < 3; i++ {
		f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes', repair_attempted_at = NULL WHERE org_id = $1`, f.org)
		if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, tk := range f.tickets {
		if st, _ := f.ticketStatus(t, tk); st != "cancelled" {
			t.Fatalf("ticket %s = %s", tk, st)
		}
		if pub.get(tk) != 1 {
			t.Fatalf("ticket %s published %d times; want once", tk, pub.get(tk))
		}
	}
	var status string
	var events int
	_ = f.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&status)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'ticket_refunded'`, f.order).Scan(&events)
	if status != "refunded" || events != 2 {
		t.Fatalf("order = %s with %d ticket_refunded events; want refunded and 2", status, events)
	}
}

// poisonNotifier refuses every message that contains poison (Telegram's 400
// for a malformed message never goes away) and records the rest.
type poisonNotifier struct {
	poison string
	mu     sync.Mutex
	texts  []string
}

func (n *poisonNotifier) SendConfirmed(_ context.Context, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if strings.Contains(text, n.poison) {
		return errors.New("telegram responded with status 400")
	}
	n.texts = append(n.texts, text)
	return nil
}

func (n *poisonNotifier) Send(ctx context.Context, text string) error {
	return n.SendConfirmed(ctx, text)
}

// TestEngine_PoisonedAlertDoesNotBlockTheOthers (LOW 2): an alert Telegram
// keeps refusing must not stop the alerts behind it, its dynamic text is
// HTML-escaped, and it is given up after a bounded number of attempts.
func TestEngine_PoisonedAlertDoesNotBlockTheOthers(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	e := f.engine(&fakeModule{partial: true}, false)
	pay := f.extraPayment(t)
	poisoned := f.rawRefund(t, pay, 100, "manual_review", `failure_code = 'x<y&z', alert_due_at = now() - interval '5 minutes'`)
	healthy := f.rawRefund(t, pay, 100, "manual_review", `failure_code = 'stuck_provider_pending', alert_due_at = now() - interval '1 minute'`)
	n := &poisonNotifier{poison: poisoned.String()}
	for i := 0; i < 2; i++ {
		f.exec(t, `UPDATE refunds SET alert_lease_until = NULL WHERE org_id = $1`, f.org)
		if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.texts) != 1 || !strings.Contains(n.texts[0], healthy.String()) {
		t.Fatalf("delivered = %v; the healthy alert must get past the poisoned one", n.texts)
	}
	// The escape: a provider failure code reaches Telegram's HTML mode
	// escaped.
	ok := &recordingNotifier{}
	f.exec(t, `UPDATE refunds SET alert_lease_until = NULL WHERE id = $1`, poisoned)
	if _, err := e.Sweep(ctx, time.Now(), ok); err != nil {
		t.Fatal(err)
	}
	if len(ok.texts) != 1 || !strings.Contains(ok.texts[0], "x&lt;y&amp;z") {
		t.Fatalf("alert text = %v; want the failure code HTML-escaped", ok.texts)
	}
	// Given up after the cap — and only once it has also been owed for a
	// day, so a Telegram outage alone never drops an alert.
	again := f.rawRefund(t, pay, 100, "manual_review", `failure_code = 'poison', alert_due_at = now() - interval '1 minute'`)
	f.exec(t, `UPDATE refunds SET alert_attempts = $2 WHERE id = $1`, again, refunds.MaxAlertAttempts-1)
	n2 := &poisonNotifier{poison: again.String()}
	if _, err := e.Sweep(ctx, time.Now(), n2); err != nil {
		t.Fatal(err)
	}
	var due *time.Time
	var attempts int
	_ = f.pool.QueryRow(ctx, `SELECT alert_due_at, alert_attempts FROM refunds WHERE id = $1`, again).Scan(&due, &attempts)
	if due == nil || attempts != refunds.MaxAlertAttempts {
		t.Fatalf("young poisoned alert: due %v attempts %d; want still owed with %d attempts", due, attempts, refunds.MaxAlertAttempts)
	}
	f.exec(t, `UPDATE refunds SET alert_due_at = now() - interval '25 hours', alert_lease_until = NULL WHERE id = $1`, again)
	if _, err := e.Sweep(ctx, time.Now(), n2); err != nil {
		t.Fatal(err)
	}
	_ = f.pool.QueryRow(ctx, `SELECT alert_due_at, alert_attempts FROM refunds WHERE id = $1`, again).Scan(&due, &attempts)
	if due != nil || len(n2.texts) != 0 {
		t.Fatalf("old poisoned alert: due %v delivered %v; want given up", due, n2.texts)
	}
}

// TestEngine_NeverAttemptedStuckRefundSaysSo (LOW 6): a refund approved 24
// hours ago that never reached the provider is parked, and its reason does
// not claim an idempotency window that never opened.
func TestEngine_NeverAttemptedStuckRefundSaysSo(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e := f.engine(&fakeModule{partial: true}, false)
	id := f.rawRefund(t, f.extraPayment(t), 100, "provider_pending",
		`approved_at = now() - interval '24 hours', created_at = now() - interval '24 hours'`)
	if rep, err := e.Sweep(context.Background(), time.Now(), nil); err != nil || rep.Stuck != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	r := f.refundState(t, id)
	// Fifth review, LOW c: the threshold is 23 hours (StuckAfter), and no
	// path sends a parked refund again, so the reason must not promise it.
	if r.State != refunds.StateManualReview || strings.Contains(deref(r.FailureReason), "idempotency") ||
		!strings.Contains(deref(r.FailureReason), "never sent") || !strings.Contains(deref(r.FailureReason), "23 hours") ||
		strings.Contains(deref(r.FailureReason), "can be sent again") {
		t.Fatalf("parked %s: %q; want a reason saying it was never sent", r.State, deref(r.FailureReason))
	}
}

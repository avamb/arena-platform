//go:build integration

// engine_review5_integration_test.go — the findings of the FIFTH PAY-03
// review, each pinned against a live database (same fixture and fake module
// as engine_integration_test.go). Every test here fails on the code before
// its fix.
package refunds_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func amountPtr(v int64) *int64 { return &v }

// TestEngine_AttemptedRefundParkedForBudgetKeepsCounting (M-1): refund A of
// 1500 on ticket 1 is marked failed by an operator while its provider call
// is in flight; the operator's replacement B of the whole ticket (2500) is
// sent, and its call ends without an answer (a timeout may have created the
// refund at the provider). Then A's provider accepts after all. B's retry is
// parked for the budget — but B WAS attempted, so its money may be out: it
// must keep counting against the payment, or a new refund of ticket 2 lets
// the money going back exceed what was paid (1500 + 2500 + 2500 > 5000).
func TestEngine_AttemptedRefundParkedForBudgetKeepsCounting(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	// The first answer goes to B (sent from inside A's call), the second
	// to A.
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout, succeeded}}
	e := f.engine(m, true)
	var (
		mu    sync.Mutex
		fired bool
		bid   uuid.UUID
	)
	// Not sync.Once: B is driven from INSIDE this hook and calls it again.
	m.beforeRefund = func(req payments.RefundRequest) bool {
		mu.Lock()
		first := !fired
		fired = true
		mu.Unlock()
		if first {
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator' WHERE id = $1`, req.IdempotencyKey)
			b, err := e.CreateBatch(ctx, f.batch("k-replacement", true, refunds.Item{TicketID: f.tickets[0]}))
			if err != nil || len(b.Refunds) != 1 {
				t.Errorf("replacement: %v %+v", err, b.Refunds)
				return true
			}
			bid = b.Refunds[0].ID
		}
		return true
	}
	a, err := e.CreateBatch(ctx, f.batch("k-original", true, refunds.Item{TicketID: f.tickets[0], Amount: amountPtr(1500)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Refunds[0]; got.State != refunds.StateManualReview || deref(got.FailureCode) != "late_acceptance_over_budget" {
		t.Fatalf("A = %s / %s; want parked over budget", got.State, deref(got.FailureCode))
	}
	if bid == uuid.Nil {
		t.Fatal("no replacement")
	}
	if b := f.refundState(t, bid); b.State != refunds.StateProviderPending || b.ProviderAttempts != 1 {
		t.Fatalf("B = %s attempts %d; want waiting after an unanswered call", b.State, b.ProviderAttempts)
	}
	f.staleMarker(t, bid)
	b, err := e.Drive(ctx, bid)
	if err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 2 {
		t.Fatalf("provider calls = %d; B's retry must not be sent", m.callCount())
	}
	// B's money may be out: the payment has 5000 - 1500 - 2500 = 1000 left.
	_, err = e.CreateBatch(ctx, f.batch("k-ticket2", true, refunds.Item{TicketID: f.tickets[1]}))
	if refusalCode(err) != refunds.CodeAmountExceeds {
		t.Fatalf("refund of ticket 2: %v; want %s — B still counts", err, refunds.CodeAmountExceeds)
	}
	if b.State != refunds.StateManualReview || deref(b.FailureCode) == "budget_taken_by_another_refund" ||
		strings.Contains(deref(b.FailureReason), "never sent") {
		t.Fatalf("B = %s / %s %q; want parked with a code and reason that say it may have been sent",
			b.State, deref(b.FailureCode), deref(b.FailureReason))
	}
	if m.callCount() != 2 {
		t.Fatalf("provider calls = %d after the refused batch", m.callCount())
	}
	// Nobody knows whether B's money went back: the ticket stays valid for
	// a person to decide, and B's reason says so.
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" || !strings.Contains(deref(b.FailureReason), "STILL VALID") {
		t.Fatalf("ticket 1 = %s, B reason %q; want the ticket left for a person", st, deref(b.FailureReason))
	}
}

// TestEngine_HeldBackRefundNeverCancelsThroughAMoneyOnlyRefund (sixth
// review, HIGH-1): payment 5000, tickets T1 and T2 at 2500. P is a
// money-only refund of 500 on T1 (succeeded, cancels nothing), F a flat
// refund of 2500 waiting for approval, B 2000 on T1 WITH cancellation and
// never sent, X an earlier failed refund of T2 accepted late and parked
// over budget. B's claim holds B back for the budget — and must not cancel
// T1 through P: only 500 of T1's 2500 went back.
func TestEngine_HeldBackRefundNeverCancelsThroughAMoneyOnlyRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	p, err := e.CreateBatch(ctx, f.batch("k-p", false, refunds.Item{TicketID: f.tickets[0], Amount: amountPtr(500)}))
	if err != nil || p.Refunds[0].State != refunds.StateSucceeded || p.Refunds[0].CancelTicket {
		t.Fatalf("P: %v %+v", err, p.Refunds)
	}
	f.rawRefund(t, f.payment, 2500, "requested", "")
	in := f.batch("k-b", true, refunds.Item{TicketID: f.tickets[0], Amount: amountPtr(2000)})
	in.Approved = false
	b, err := e.CreateBatch(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	bid := b.Refunds[0].ID
	f.rawRefund(t, f.payment, 2500, "manual_review", `ticket_id = '`+f.tickets[1].String()+`', order_id = '`+f.order.String()+
		`', failure_code = 'late_acceptance_over_budget', provider_status = 'succeeded', provider_refund_id = 're_x_`+uuid.NewString()+`'`)
	f.approveNow(t, bid)
	got, err := e.Drive(ctx, bid)
	if err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 1 || got.State != refunds.StateManualReview || deref(got.FailureCode) != "budget_taken_by_another_refund" || got.CancelTicket {
		t.Fatalf("B = %s / %s cancel %v, calls %d; want held back unsent without cancel_ticket",
			got.State, deref(got.FailureCode), got.CancelTicket, m.callCount())
	}
	if st, link := f.ticketStatus(t, f.tickets[0]); st != "active" || link != nil {
		t.Fatalf("T1 = %s link %v; a money-only refund of 500 must never cancel it", st, link)
	}
	if rp := f.refundState(t, p.Refunds[0].ID); rp.CancelTicket || rp.State != refunds.StateSucceeded {
		t.Fatalf("P = %s cancel %v; want untouched", rp.State, rp.CancelTicket)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE org_id = $1`, f.org)
	if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("T1 = %s after the sweep", st)
	}
}

// TestEngine_NeverSentStuckRefundFreesItsTicket (sixth review, LOW-3): a
// refund of T1 approved a day ago and never sent is parked by the sweep as
// stuck_provider_pending. It moved no money, so — like a never-sent refund
// held back for its budget — it stops counting against the payment and the
// ticket and gives its cancel_ticket up: the operator's way forward is a
// NEW refund of the ticket, which must be possible.
func TestEngine_NeverSentStuckRefundFreesItsTicket(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	stuck := f.rawRefund(t, f.payment, 2500, "provider_pending", `ticket_id = '`+f.tickets[0].String()+`', order_id = '`+f.order.String()+
		`', cancel_ticket = true, approved_at = now() - interval '24 hours', created_at = now() - interval '24 hours'`)
	if rep, err := e.Sweep(ctx, time.Now(), nil); err != nil || rep.Stuck != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	r := f.refundState(t, stuck)
	if r.State != refunds.StateManualReview || deref(r.FailureCode) != "stuck_provider_pending" || r.CancelTicket {
		t.Fatalf("stuck = %s / %s cancel %v; want parked without cancel_ticket", r.State, deref(r.FailureCode), r.CancelTicket)
	}
	if !strings.Contains(deref(r.FailureReason), "no money") || !strings.Contains(deref(r.FailureReason), "new refund") {
		t.Fatalf("reason %q; want it to say no money moved and that a new refund is the way forward", deref(r.FailureReason))
	}
	if sum, _ := gen.New(f.pool).SumNonFailedRefundsByIntent(ctx, f.payment); sum != 0 {
		t.Fatalf("flat sum = %d; the never-sent stuck refund moved no money", sum)
	}
	res, err := e.CreateBatch(ctx, f.batch("k-again", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("new refund of T1: %v %+v; the stuck refund must not block it", err, res.Refunds)
	}
}

// TestEngine_ForgedTicketCancelPrefixIsAnOrdinaryFlatRefund (M-4):
// requested_by is free text a POST /v1/refunds client sends. A
// whole-payment flat refund whose requested_by merely LOOKS like the
// ticket-cancel route's ("ticket.cancel:<ticket>") is still an order-level
// refund: when the provider accepts it, every ticket of the order is
// cancelled and announced, and the order reads refunded — the money went
// back, the tickets must stop admitting.
func TestEngine_ForgedTicketCancelPrefixIsAnOrdinaryFlatRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	pub := &publishCounter{}
	e := f.engineWith(&fakeModule{partial: true}, true, func(o *refunds.Options) { o.PublishRefunded = pub.fn })
	id := f.rawRefund(t, f.payment, 5000, "provider_pending", "requested_by = '"+refunds.TicketCancelRequestedByPrefix+f.tickets[0].String()+"'")
	r, err := e.Drive(ctx, id)
	if err != nil || r.State != refunds.StateSucceeded {
		t.Fatalf("Drive: %v %s", err, r.State)
	}
	for _, tk := range f.tickets {
		if st, link := f.ticketStatus(t, tk); st != "cancelled" || link == nil || *link != id {
			t.Fatalf("ticket %s = %s link %v; a whole-payment refund cancels every ticket of the order", tk, st, link)
		}
		if pub.get(tk) != 1 {
			t.Fatalf("ticket %s published %d times; want once", tk, pub.get(tk))
		}
	}
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&status)
	if status != "refunded" {
		t.Fatalf("order = %s; want refunded", status)
	}
}

// TestEngine_DriveStopsCancellingWhenItsCallerGoesAway (LOW b): the
// provider's answer is recorded on a context detached from the caller, but
// once the caller is gone (a worker shutting down) no new ticket
// cancellation is started there: the sweep's repair finishes it.
func TestEngine_DriveStopsCancellingWhenItsCallerGoesAway(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.beforeRefund = func(payments.RefundRequest) bool { cancel(); return true }
	res, err := e.CreateBatch(ctx, f.batch("k-shutdown", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	if r := f.refundState(t, res.Refunds[0].ID); r.State != refunds.StateSucceeded {
		t.Fatalf("refund = %s; the acceptance must be recorded although the caller left", r.State)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s; no cancellation may start after the caller left", st)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE org_id = $1`, f.org)
	if _, err := e.Sweep(context.Background(), time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "cancelled" {
		t.Fatalf("ticket = %s after the repair", st)
	}
}

// TestEngine_FlatBudgetSumMatchesTheEngine (LOW d): the flat routes
// (POST /v1/refunds, approve, the ticket-cancel route) and the engine must
// count the same refunds against a payment. A refund held back for its
// budget BEFORE any provider call moved no money and counts for neither;
// once an operator resolves it to succeeded, or when it was attempted, it
// counts for both.
func TestEngine_FlatBudgetSumMatchesTheEngine(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	pay := f.extraPayment(t)
	f.rawRefund(t, pay, 300, "succeeded", `provider_refund_id = 're_`+uuid.NewString()+`'`)
	held := f.rawRefund(t, pay, 600, "manual_review", `failure_code = 'budget_taken_by_another_refund'`)
	q := gen.New(f.pool)
	if sum, err := q.SumNonFailedRefundsByIntent(ctx, pay); err != nil || sum != 300 {
		t.Fatalf("flat sum = %d %v; want 300 — the never-sent held refund moved no money", sum, err)
	}
	f.exec(t, `UPDATE refunds SET provider_attempts = 1 WHERE id = $1`, held)
	if sum, _ := q.SumNonFailedRefundsByIntent(ctx, pay); sum != 900 {
		t.Fatalf("flat sum = %d; want 900 — an attempted held refund may have moved money", sum)
	}
	f.exec(t, `UPDATE refunds SET provider_attempts = 0, state = 'succeeded' WHERE id = $1`, held)
	if sum, _ := q.SumNonFailedRefundsByIntent(ctx, pay); sum != 900 {
		t.Fatalf("flat sum = %d; want 900 — an operator resolved the held refund as paid out", sum)
	}
}

// TestEngine_WholeOrderRefundCutByTheDeadlineEndsRefunded (M-3): ONE
// whole-payment refund covers both tickets, and the caller goes away
// after the first cancellation. The first settle projects the order while a
// ticket is still active (partially_refunded); the sweep's repair cancels
// the second ticket later — and the order must then read refunded, not stay
// partially_refunded with no ticket left.
func TestEngine_WholeOrderRefundCutByTheDeadlineEndsRefunded(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	// Deterministic (sixth review, LOW-4): the caller goes away right after
	// the FIRST cancellation, so the loop starts no second one — no
	// real-time window a slow runner could miss.
	caller, leave := context.WithCancel(ctx)
	defer leave()
	e := f.engineWith(&fakeModule{partial: true}, true, func(o *refunds.Options) {
		inner := o.CancelTicket
		o.CancelTicket = func(ctx context.Context, req refunds.CancelRequest) error {
			err := inner(ctx, req)
			leave()
			return err
		}
	})
	id := f.rawRefund(t, f.payment, 5000, "provider_pending", "")
	r, err := e.Drive(caller, id)
	if err != nil || r.State != refunds.StateSucceeded {
		t.Fatalf("Drive: %v %s", err, r.State)
	}
	active := 0
	for _, tk := range f.tickets {
		if st, _ := f.ticketStatus(t, tk); st == "active" {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active tickets after the first settle = %d; the caller leaving must cut the loop after one", active)
	}
	for i := 0; i < 3; i++ {
		f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes', repair_attempted_at = NULL WHERE org_id = $1`, f.org)
		if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, tk := range f.tickets {
		if st, _ := f.ticketStatus(t, tk); st != "cancelled" {
			t.Fatalf("ticket %s = %s after the repair", tk, st)
		}
	}
	var status string
	var events int
	_ = f.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&status)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'refunded'`, f.order).Scan(&events)
	if status != "refunded" || events != 1 {
		t.Fatalf("order = %s with %d refunded events; want refunded and exactly one event", status, events)
	}
}

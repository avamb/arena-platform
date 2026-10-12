//go:build integration

package refunds_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// parkedNeverSent inserts a refund of ticket 0 parked before any provider
// call, with an operator alert owed.
func parkedNeverSent(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency,
		reason, state, settlement, provider, provider_attempts, failure_code, cancel_ticket, alert_due_at)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'parked', 'manual_review', 'provider', 'pay03fake', 0, 'provider_not_configured', true, now())
		RETURNING id`, f.payment, f.org, f.order, f.tickets[0]).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func retryInput(f *fixture, id uuid.UUID) refunds.RetryInput {
	return refunds.RetryInput{OrgID: f.org, RefundID: id, Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}}
}

// A retry whose replacement cannot be created leaves the parked refund
// exactly as it was, its owed alert included.
func TestEngine_FailedRetryKeepsTheParkedRefundAndItsAlert(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	id := parkedNeverSent(t, f)
	f.exec(t, `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, f.tickets[0])
	e := f.engine(&fakeModule{partial: true}, true)
	if _, err := e.Retry(ctx, retryInput(f, id)); err == nil {
		t.Fatal("a retry of a cancelled ticket's refund succeeded")
	}
	var state, code string
	var alert bool
	if err := f.pool.QueryRow(ctx, `SELECT state, failure_code, alert_due_at IS NOT NULL FROM refunds WHERE id = $1`, id).Scan(&state, &code, &alert); err != nil {
		t.Fatal(err)
	}
	if state != "manual_review" || code != "provider_not_configured" || !alert {
		t.Fatalf("after a failed retry: %s %s alert=%v; want manual_review provider_not_configured with its alert", state, code, alert)
	}
}

// Two retries at once make exactly one replacement.
func TestEngine_ConcurrentRetriesMakeOneReplacement(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	id := parkedNeverSent(t, f)
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.Retry(ctx, retryInput(f, id))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok == 0 {
		t.Fatalf("no retry succeeded: %v", errs)
	}
	var replacements int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE ticket_id = $1 AND id <> $2`, f.tickets[0], id).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if replacements != 1 {
		t.Fatalf("%d replacement refunds; want exactly 1", replacements)
	}
	var state string
	_ = f.pool.QueryRow(ctx, `SELECT state FROM refunds WHERE id = $1`, id).Scan(&state)
	if state != "failed" {
		t.Fatalf("replaced refund is %s; want failed (replaced)", state)
	}
}

// A retry goes through the payment of the refund it replaces, not the
// order's newest payment.
func TestEngine_RetryUsesTheOriginalRefundsPayment(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	id := parkedNeverSent(t, f)
	ref := "cs_pay03_newer_" + uuid.NewString()
	if _, err := gen.New(f.pool).InsertPaymentIntent(ctx, &f.cs, f.org, "pay03fake", &ref, 5000, "EUR", "succeeded", nil, nil); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE payment_intents SET succeeded_at = now() + interval '1 minute' WHERE provider_payment_id = $1`, ref)
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	res, err := f.engine(m, true).Retry(ctx, retryInput(f, id))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Refunds[0].PaymentIntentID; got != f.payment {
		t.Fatalf("retry went through payment %s; want the original %s", got, f.payment)
	}
}

// Round-2 review R2-6: a drive does not start a provider call that the
// caller's remaining budget would cut short; the refund waits for the sweep,
// never attempted, instead of ending as an unknown outcome.
func TestEngine_CreateLeavesTheCallToTheSweepWhenTheBudgetIsSpent(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engineWith(m, true, func(o *refunds.Options) { o.CallTimeout = 10 * time.Second })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.CreateBatch(ctx, f.batch("k-budget-spent", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n := m.callCount(); n != 0 {
		t.Fatalf("provider calls = %d; want none with 5 s left of a 10 s call", n)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateProviderPending || r.ProviderAttempts != 0 {
		t.Fatalf("refund = %s with %d attempt(s); want provider_pending and never attempted", r.State, r.ProviderAttempts)
	}
}

// failingRetryAudit refuses the retry's own audit event and writes the rest.
type failingRetryAudit struct{ audit.Writer }

func (w failingRetryAudit) WriteTx(ctx context.Context, tx pgx.Tx, ev audit.Event) error {
	if ev.Action == "v1.refund.retry" {
		return errors.New("audit store down")
	}
	return w.Writer.WriteTx(ctx, tx, ev)
}

// The record of a retry is written with the replacement it names: a retry
// whose audit cannot be written leaves no replacement and the parked refund
// as it was.
func TestEngine_RetryAuditIsWrittenWithTheReplacement(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	id := parkedNeverSent(t, f)
	e := f.engineWith(&fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}, true,
		func(o *refunds.Options) { o.Audit = failingRetryAudit{audit.NewPGWriter(f.pool)} })
	if _, err := e.Retry(ctx, retryInput(f, id)); err == nil {
		t.Fatal("a retry whose audit failed succeeded")
	}
	var n int
	var state string
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM refunds WHERE payment_intent_id = $1 AND id <> $2), state
		FROM refunds WHERE id = $2`, f.payment, id).Scan(&n, &state); err != nil {
		t.Fatal(err)
	}
	if n != 0 || state != "manual_review" {
		t.Fatalf("after a retry whose audit failed: %d other refund(s), parked refund %s; want 0 and manual_review", n, state)
	}
}

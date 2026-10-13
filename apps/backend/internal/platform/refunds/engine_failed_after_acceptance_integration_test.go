//go:build integration

// engine_failed_after_acceptance_integration_test.go - round 4 review B: a
// refund the provider accepted as pending and later reported failed moved no
// money. It is parked under one stable code and leaves the payment's budget
// in every copy of the budget rule, so neither a new refund nor a refund made
// in the provider's dashboard is refused or flagged as a double refund.
package refunds_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// failAfterAcceptance makes the pending refund r fail at the provider.
func failAfterAcceptance(t *testing.T, f *fixture, e *refunds.Engine, m *fakeModule, r refunds.Refund) refunds.Refund {
	t.Helper()
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "expired_or_canceled_card"}
	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundFailed}); err != nil || !found {
		t.Fatalf("ApplyProviderRefund = %v %v", found, err)
	}
	got, err := e.GetRefund(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != refunds.StateManualReview || got.FailureCode == nil || *got.FailureCode != "failed_after_acceptance" ||
		got.FailureReason == nil || !strings.Contains(*got.FailureReason, "expired_or_canceled_card") {
		t.Fatalf("refund = %s %v %v; want manual_review failed_after_acceptance naming the provider's code", got.State, got.FailureCode, got.FailureReason)
	}
	return got
}

// The failed refund leaves the payment's budget: the engine lets the other
// ticket be refunded beside half of the payment that went back elsewhere,
// and the flat routes' sum (the gen twin) leaves it out too.
func TestEngine_FailedAfterAcceptanceLeavesThePaymentsBudget(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-fail-after-accept")
	ctx := context.Background()
	failAfterAcceptance(t, f, e, m, r)
	f.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement,
		provider, provider_refund_id, provider_status, succeeded_at, settled_at)
		SELECT id, org_id, $2, 2500, currency, 'order level', 'succeeded', 'provider', 'pay03fake', 're_half_'||gen_random_uuid(), 'succeeded', now(), now()
		FROM payment_intents WHERE id = $1`, f.payment, f.order)
	if got, err := gen.New(f.pool).SumNonFailedRefundsByIntent(ctx, f.payment); err != nil || got != 2500 {
		t.Fatalf("flat routes' budget sum = %d (%v); want 2500 (the failed refund moved nothing)", got, err)
	}
	m.answers = append(m.answers, succeeded)
	if _, err := e.CreateBatch(ctx, f.batch("k-after-fail-after-accept", true, refunds.Item{TicketID: f.tickets[1]})); err != nil {
		t.Fatalf("refund of the other ticket: %v (the failed refund still counts)", err)
	}
}

// A refund made in the provider's dashboard after an arena refund failed
// there is the buyer's money going back for the first time: it is recorded,
// never parked as "the buyer may have been refunded twice".
func TestEngine_DashboardRefundAfterAFailedAcceptanceIsNotADoubleRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	f.cleanProviderEvents(t)
	e, r, m := pendingRefundWith(t, f, "k-fail-then-dashboard")
	ctx := context.Background()
	failAfterAcceptance(t, f, e, m, r)
	m.answers = append(m.answers, succeeded)
	if _, err := e.CreateBatch(ctx, f.batch("k-fail-then-dashboard-2", true, refunds.Item{TicketID: f.tickets[1]})); err != nil {
		t.Fatalf("refund of the other ticket: %v", err)
	}
	ev := payments.NormalizedEvent{ProviderRefundID: "re_dash_" + uuid.NewString()[:8], ProviderChargeRef: f.paymentRef(t),
		RefundStatus: payments.RefundSucceeded}
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded, AmountMinor: 2500, Currency: "EUR", ProviderChargeRef: f.paymentRef(t)}
	res, err := e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil)
	if err != nil || !res.Found || !res.Created {
		t.Fatalf("dashboard refund = %+v %v; want it recorded", res, err)
	}
	if res.Parked || res.Capped || deref(res.Refund.FailureCode) == refunds.DashboardOverPayment {
		t.Fatalf("dashboard refund parked=%v capped=%v code=%s; want it recorded in full", res.Parked, res.Capped, deref(res.Refund.FailureCode))
	}
}

// A reversed refund leaves the flat routes' sum too (the gen twin of the
// budget rule).
func TestEngine_ReversedRefundLeavesTheFlatRoutesSum(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-reversed-flat-sum")
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID, payments.RefundResult{Status: payments.RefundSucceeded}); err != nil {
		t.Fatal(err)
	}
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "canceled"}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID, payments.RefundResult{Status: payments.RefundFailed}); err != nil {
		t.Fatal(err)
	}
	if got, err := gen.New(f.pool).SumNonFailedRefundsByIntent(ctx, f.payment); err != nil || got != 0 {
		t.Fatalf("flat routes' budget sum = %d (%v); want 0 (the reversed refund moved nothing)", got, err)
	}
}

// Two different refunds of one payment made in the provider's dashboard and
// delivered at once are judged one after the other under the payment's lock:
// together they never record more than was paid.
func TestEngine_ParallelDashboardRefundsStayInsideThePayment(t *testing.T) {
	for round := 0; round < 5; round++ {
		f := newFixture(t, testPool(t), "pay03fake")
		f.cleanProviderEvents(t)
		ctx := context.Background()
		ref := f.paymentRef(t)
		a, b := "re_par_a_"+uuid.NewString()[:8], "re_par_b_"+uuid.NewString()[:8]
		var arrived sync.WaitGroup
		arrived.Add(2)
		m := &fakeModule{partial: true, lookupByID: map[string]payments.RefundResult{
			a: {Status: payments.RefundSucceeded, AmountMinor: 2500, Currency: "EUR", ProviderChargeRef: ref},
			b: {Status: payments.RefundSucceeded, AmountMinor: 3000, Currency: "EUR", ProviderChargeRef: ref},
		}}
		// Both deliveries finish reading the refund back before either
		// judges the budget.
		m.onLookup = func() { arrived.Done(); arrived.Wait() }
		e := f.engine(m, true)
		var wg sync.WaitGroup
		for _, id := range []string{a, b} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				ev := payments.NormalizedEvent{ProviderRefundID: id, ProviderChargeRef: ref, RefundStatus: payments.RefundSucceeded}
				if _, err := e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil); err != nil {
					t.Errorf("dashboard refund %s: %v", id, err)
				}
			}(id)
		}
		wg.Wait()
		var total int64
		var reviewed int
		if err := f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount) FILTER (WHERE state = 'succeeded'), 0)::bigint,
			count(*) FILTER (WHERE state = 'manual_review' OR failure_code = $2)
			FROM refunds WHERE payment_intent_id = $1`, f.payment, refunds.DashboardCapped).Scan(&total, &reviewed); err != nil {
			t.Fatal(err)
		}
		if total > 5000 {
			t.Fatalf("round %d: dashboard refunds recorded %d of a 5000 payment", round, total)
		}
		if reviewed != 1 {
			t.Fatalf("round %d: %d capped or parked rows; want exactly 1", round, reviewed)
		}
	}
}

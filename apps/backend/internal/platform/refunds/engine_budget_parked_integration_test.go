//go:build integration

package refunds_test

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// A refund parked before any provider call moved no money, whatever parked it
// (here a broken payment configuration): it does not count against the
// payment, so the organizer can still refund it.
func TestEngine_RefundParkedBeforeAnyCallDoesNotCountAgainstThePayment(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement,
		provider, provider_attempts, failure_code)
		SELECT id, org_id, amount, currency, 'parked: configuration', 'manual_review', 'provider', 'pay03fake', 0, 'provider_not_configured'
		FROM payment_intents WHERE id = $1`, f.payment); err != nil {
		t.Fatal(err)
	}
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(ctx, f.batch("k-parked-budget", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatalf("CreateBatch beside a refund parked before any call: %v", err)
	}
	if len(res.Refunds) != 1 || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("refunds = %+v; want one succeeded", res.Refunds)
	}
}

// A refund that names no ticket (made in the provider dashboard, say) reduces
// what is left of the payment; no ticket is offered more than that.
func TestEngine_OptionsCapTicketsAtWhatIsLeftOfThePayment(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement,
		provider, provider_refund_id, provider_status, succeeded_at, settled_at)
		SELECT id, org_id, $2, 4000, currency, 'order level', 'succeeded', 'provider', 'pay03fake', 're_order_level_'||gen_random_uuid(), 'succeeded', now(), now()
		FROM payment_intents WHERE id = $1`, f.payment, f.order); err != nil {
		t.Fatal(err)
	}
	e := f.engine(&fakeModule{partial: true}, true)
	opts, err := e.Options(ctx, f.org, f.order)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range opts.Tickets {
		if tk.Refundable != 1000 {
			t.Errorf("ticket %s refundable %d; want 1000 (5000 paid, 4000 already back)", tk.TicketID, tk.Refundable)
		}
	}
}

// A pre-engine row migration 0138 moved to manual_review has no provider and
// never went through the engine: it may stand for money an organizer returned
// by hand, so it keeps counting against the payment until an operator closes
// it. Only rows the engine itself parked (they always name a provider) are
// left out of the budget.
func TestEngine_PreEngineManualReviewRowStillCountsAgainstThePayment(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement,
		provider_attempts)
		SELECT id, org_id, amount, currency, 'moved by 0138', 'manual_review', 'provider', 0
		FROM payment_intents WHERE id = $1`, f.payment); err != nil {
		t.Fatal(err)
	}
	var sum int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0) FROM refunds WHERE payment_intent_id = $1`, f.payment).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	got, err := gen.New(f.pool).SumNonFailedRefundsByIntent(ctx, f.payment)
	if err != nil {
		t.Fatal(err)
	}
	if got != sum {
		t.Errorf("flat routes' budget sum = %d; want %d (the pre-engine row counts)", got, sum)
	}
	e := f.engine(&fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}, true)
	if _, err := e.CreateBatch(ctx, f.batch("k-legacy-review-budget", true, refunds.Item{TicketID: f.tickets[0]})); err == nil {
		t.Fatal("CreateBatch refunded a ticket whose payment is fully taken by a pre-engine manual_review row")
	}
}

// The review-hold clear shows the money returned on the payment without a
// ticket by the same budget rule as the engine: a pre-engine parked row
// counts, a never-sent parked row and a reversed or failed-after-acceptance
// row do not.
func TestUnattributedForTicket_UsesTheBudgetRule(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	insert := func(amount int64, state, extra, extraValues string) {
		t.Helper()
		f.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement`+extra+`)
			SELECT id, org_id, $2, currency, 'unattributed', $3, 'provider'`+extraValues+`
			FROM payment_intents WHERE id = $1`, f.payment, amount, state)
	}
	insert(1000, "manual_review", ", provider_attempts", ", 0")
	insert(700, "manual_review", ", provider, provider_attempts", ", 'pay03fake', 0")
	insert(300, "manual_review", ", provider, provider_attempts, provider_refund_id, failure_code",
		", 'pay03fake', 1, 're_u1_'||gen_random_uuid(), 'reversed_after_success'")
	insert(400, "manual_review", ", provider, provider_attempts, provider_refund_id, failure_code",
		", 'pay03fake', 1, 're_u2_'||gen_random_uuid(), 'failed_after_acceptance'")
	insert(200, "succeeded", ", provider, provider_refund_id", ", 'pay03fake', 're_u3_'||gen_random_uuid()")
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	got, _, err := refunds.UnattributedForTicket(ctx, tx, f.tickets[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != 1200 {
		t.Fatalf("unattributed = %d; want 1200 (pre-engine 1000 + succeeded 200)", got)
	}
}

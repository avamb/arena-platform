//go:build integration

package refunds_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

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

// A pre-engine row (no provider) is closed by hand by the owner, never through
// the resolve route: the route answers 409 and the row stays as it was.
func TestEngine_ResolveRefusesAPreEngineRow(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement,
		provider_attempts)
		SELECT id, org_id, amount, currency, 'moved by 0138', 'manual_review', 'provider', 0
		FROM payment_intents WHERE id = $1 RETURNING id`, f.payment).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e := f.engine(&fakeModule{partial: true}, true)
	for _, outcome := range []string{refunds.ResolveRefunded, refunds.ResolveNotRefunded} {
		_, err := e.Resolve(ctx, refunds.ResolveInput{OrgID: f.org, RefundID: id, Outcome: outcome, Note: "checked",
			Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}, Via: "admin_web"})
		var re *refunds.Error
		if !errors.As(err, &re) || re.Code != refunds.CodeNotInReview {
			t.Fatalf("Resolve(%s) of a pre-engine row: %v; want %s", outcome, err, refunds.CodeNotInReview)
		}
	}
	if got := f.refundState(t, id); got.State != refunds.StateManualReview {
		t.Errorf("pre-engine row state = %s; want manual_review (untouched)", got.State)
	}
}

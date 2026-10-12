//go:build integration

package refunds_test

import (
	"context"
	"testing"

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

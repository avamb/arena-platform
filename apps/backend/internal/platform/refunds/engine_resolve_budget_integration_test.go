//go:build integration

package refunds_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// A parked refund that left the budget (the provider reversed it, or failed it
// after accepting it) counts again once it is closed as succeeded. Resolve
// must judge the payment's total WITH it: closing it as refunded on a payment
// it would push over what was paid is refused, and closing it as beyond the
// payment is allowed.
func TestEngine_ResolveCountsAParkedRowThatLeftTheBudget(t *testing.T) {
	for _, code := range []string{"failed_after_acceptance", "reversed_after_success"} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t, testPool(t), "pay03fake")
			ctx := context.Background()
			var paid int64
			if err := f.pool.QueryRow(ctx, `SELECT amount FROM payment_intents WHERE id = $1`, f.payment).Scan(&paid); err != nil {
				t.Fatal(err)
			}
			f.rawRefund(t, f.payment, paid/2, "succeeded", `provider_refund_id = 're_rb_ok_'||gen_random_uuid(), provider_attempts = 1`)
			parked := f.rawRefund(t, f.payment, paid, "manual_review",
				`provider_refund_id = 're_rb_p_'||gen_random_uuid(), provider_attempts = 1, failure_code = '`+code+`'`)
			e := f.engine(&fakeModule{partial: true}, true)
			actor := refunds.Actor{Type: "user", ID: uuid.NewString()}
			_, err := e.Resolve(ctx, refunds.ResolveInput{OrgID: f.org, RefundID: parked, Outcome: refunds.ResolveRefunded,
				Note: "dashboard shows it", Actor: actor})
			var re *refunds.Error
			if !errors.As(err, &re) || re.Code != refunds.CodeOverBudget {
				t.Fatalf("resolve as refunded over the payment: %v; want %s", err, refunds.CodeOverBudget)
			}
			if got := f.refundState(t, parked); got.State != refunds.StateManualReview {
				t.Fatalf("refused resolve changed the row to %s", got.State)
			}
			out, err := e.Resolve(ctx, refunds.ResolveInput{OrgID: f.org, RefundID: parked, Outcome: refunds.ResolveRefundedBeyondPayment,
				Note: "refunded twice at the provider", Actor: actor})
			if err != nil || out.State != refunds.StateSucceeded {
				t.Fatalf("resolve as beyond the payment: %+v %v; want succeeded", out.State, err)
			}
		})
	}
}

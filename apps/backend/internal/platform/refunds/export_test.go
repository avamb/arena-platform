package refunds

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SettleForTest runs the after-acceptance steps of r again, as a request
// and a sweep repair racing each other would.
func (e *Engine) SettleForTest(ctx context.Context, r Refund) error {
	var pay Payment
	if err := e.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		pay, err = getPayment(ctx, tx, r.PaymentIntentID)
		return err
	}); err != nil {
		return err
	}
	e.settle(ctx, r, pay)
	return nil
}

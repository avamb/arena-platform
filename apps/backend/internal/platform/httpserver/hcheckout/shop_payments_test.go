package hcheckout

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type fakeShopPayments struct{ fulfilled, failed, review int }

func (f *fakeShopPayments) FulfillPaidOrderTx(context.Context, pgx.Tx, uuid.UUID, map[string]any) (bool, error) {
	f.fulfilled++
	return true, nil
}

func (f *fakeShopPayments) MarkPaymentFailedTx(context.Context, pgx.Tx, uuid.UUID) (bool, error) {
	f.failed++
	return true, nil
}

func (f *fakeShopPayments) MarkPaymentReviewTx(context.Context, pgx.Tx, uuid.UUID, map[string]any) (bool, error) {
	f.review++
	return true, nil
}

// SA-09: a payment parked in manual_review (amount mismatch, an untrusted
// payment status) is handed to the shop, so the order is parked with it
// instead of staying pending for the expiry sweep.
func TestApplyShopPayment_RoutesEveryStateToTheShop(t *testing.T) {
	for _, c := range []struct {
		state                  string
		fulfilled, failed, rev int
	}{
		{"succeeded", 1, 0, 0},
		{"failed", 0, 1, 0},
		{"manual_review", 0, 0, 1},
		{"processing", 0, 0, 0},
	} {
		fake := &fakeShopPayments{}
		h := &Handler{shopPayments: fake}
		handled, err := h.applyShopPayment(context.Background(), nil, uuid.New(), gen.PaymentIntentRow{State: c.state}, webhookPaymentIntentRequest{})
		if err != nil {
			t.Fatalf("%s: %v", c.state, err)
		}
		if want := c.fulfilled+c.failed+c.rev > 0; handled != want {
			t.Errorf("%s: handled = %v; want %v", c.state, handled, want)
		}
		if fake.fulfilled != c.fulfilled || fake.failed != c.failed || fake.review != c.rev {
			t.Errorf("%s: calls fulfilled=%d failed=%d review=%d; want %d/%d/%d", c.state,
				fake.fulfilled, fake.failed, fake.review, c.fulfilled, c.failed, c.rev)
		}
	}
}

package payments_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// PAY-03: RefundDeclined is how the refund engine decides "failed, tickets
// stay valid" against "unknown, retry".
func TestRefundDeclined(t *testing.T) {
	wrapped := fmt.Errorf("stripe: Refund: %w", &payments.RefundDeclinedError{Code: "card_closed", Message: "closed"})
	cases := []struct {
		err      error
		code     string
		declined bool
	}{
		{nil, "", false},
		{errors.New("timeout"), "", false},
		{wrapped, "card_closed", true},
		{&payments.RefundDeclinedError{}, "declined", true},
		{fmt.Errorf("x: %w", payments.ErrRefundAmountInvalid), "amount_invalid", true},
		{payments.ErrRefundExceedsPayment, "amount_exceeds_payment", true},
		{payments.ErrRefundChargeRefMissing, "charge_ref_missing", true},
	}
	for i, c := range cases {
		code, _, declined := payments.RefundDeclined(c.err)
		if declined != c.declined || code != c.code {
			t.Errorf("case %d: (%q, %v); want (%q, %v)", i, code, declined, c.code, c.declined)
		}
	}
}

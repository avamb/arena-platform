package hcheckout

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// PAY-14b review L-C: the core judges every module that owns its webhook
// normalization (Flitt, AllPay) the same way, and a paid event without an
// amount or a currency is never trusted with tickets.
func TestPaidAmountMismatch(t *testing.T) {
	cases := []struct {
		name         string
		evAmount     int64
		evCurrency   string
		wantMismatch bool
	}{
		{"agrees", 5245, "ILS", false},
		{"currency in another case agrees", 5245, "ils", false},
		{"other amount", 5000, "ILS", true},
		{"other currency", 5245, "EUR", true},
		{"no amount", 0, "ILS", true},
		{"no currency", 5245, "", true},
		{"blank currency", 5245, "  ", true},
		{"neither", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := payments.NormalizedEvent{Kind: payments.EventPaymentSucceeded, AmountMinor: tc.evAmount, Currency: tc.evCurrency}
			if got := paidAmountMismatch(ev, 5245, "ILS"); (got != "") != tc.wantMismatch {
				t.Fatalf("paidAmountMismatch = %q; want mismatch %v", got, tc.wantMismatch)
			}
		})
	}
}

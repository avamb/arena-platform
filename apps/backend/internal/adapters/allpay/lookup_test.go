package allpay

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// TestLookupHostedPayment: the checkout.payment_recover job's read-back of an
// order whose creation answer arena lost (П-19).
func TestLookupHostedPayment(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		state  payments.HostedPaymentState
		kind   payments.EventKind
		amount int64
	}{
		{"unknown order", `{"error_code":6,"error_msg":"Order not found"}`, payments.HostedPaymentUnknown, "", 0},
		{"unpaid", `{"order_id":"o-1","status":0,"amount":10,"currency_display":"ILS"}`, payments.HostedPaymentUnpaid, "", 0},
		{"authorized only", `{"order_id":"o-1","status":0,"preauth":"authorized","amount":10,"currency_display":"ILS"}`, payments.HostedPaymentPending, "", 0},
		{"paid", `{"order_id":"o-1","status":1,"amount":120.50,"currency_display":"ILS","currency_payment":"ILS"}`, payments.HostedPaymentSettled, payments.EventPaymentSucceeded, 12050},
		{"paid without amount", `{"order_id":"o-1","status":1,"currency_display":"ILS"}`, payments.HostedPaymentPending, "", 0},
		{"refunded before confirmed", `{"order_id":"o-1","status":3,"amount":10,"currency_display":"ILS"}`, payments.HostedPaymentSettled, payments.EventPaymentManualReview, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubAllPay(t, "pp100", "KEY")
			stub.answer[methodPaymentStatus] = tc.answer
			got, err := newTestAdapter(stub, Config{}).LookupHostedPayment(t.Context(), "o-1")
			if err != nil {
				t.Fatalf("LookupHostedPayment: %v", err)
			}
			if got.State != tc.state {
				t.Fatalf("state = %v; want %v", got.State, tc.state)
			}
			if tc.state != payments.HostedPaymentSettled {
				return
			}
			ev := got.Event
			if ev.Kind != tc.kind || ev.ProviderPaymentID != "o-1" || ev.Type != EventTypePaid || ev.AmountMinor != tc.amount {
				t.Fatalf("event = %+v", ev)
			}
			if len(ev.Raw) == 0 {
				t.Fatal("a settled event needs a raw payload to store")
			}
		})
	}
}

func TestLookupHostedPayment_Unreachable(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	stub.server.Close()
	if _, err := newTestAdapter(stub, Config{}).LookupHostedPayment(t.Context(), "o-1"); err == nil {
		t.Fatal("an unreachable AllPay must be an error, never a state")
	}
}

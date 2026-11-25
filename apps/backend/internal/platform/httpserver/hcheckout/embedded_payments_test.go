package hcheckout

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Spec 65 §3.6: a payment_intent.* event never moves a row created from a
// Checkout Session, whatever lookup found it.
func TestPaymentIntentEventOnCheckoutSession(t *testing.T) {
	cs, pi := "cs_test_1", "pi_test_1"
	for _, tc := range []struct {
		event string
		id    *string
		want  bool
	}{
		{"payment_intent.payment_failed", &cs, true},
		{"payment_intent.succeeded", &cs, true},
		{"payment_intent.requires_action", &cs, true},
		{"checkout.session.completed", &cs, false},
		{"checkout.session.expired", &cs, false},
		{"payment_intent.payment_failed", &pi, false},
		{"payment_intent.payment_failed", nil, false},
	} {
		got := paymentIntentEventOnCheckoutSession(tc.event, gen.PaymentIntentRow{ProviderPaymentID: tc.id})
		if got != tc.want {
			t.Errorf("%s on %v = %v, want %v", tc.event, tc.id, got, tc.want)
		}
	}
}

func TestCompletedByAnotherPayment(t *testing.T) {
	me := uuid.New()
	other := uuid.New().String()
	mine := me.String()
	empty := ""
	for _, tc := range []struct {
		paidBy *string
		want   bool
	}{
		{nil, false},
		{&empty, false},
		{&mine, false},
		{&other, true},
	} {
		if got := completedByAnotherPayment(gen.CheckoutSessionRow{PaymentIntentID: tc.paidBy}, me); got != tc.want {
			t.Errorf("paid by %v: %v, want %v", tc.paidBy, got, tc.want)
		}
	}
}

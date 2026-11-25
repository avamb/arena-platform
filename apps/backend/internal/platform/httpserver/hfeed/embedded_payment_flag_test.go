package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// availStarter is a payment starter whose embedded availability is scripted.
type availStarter struct {
	PaymentStarter
	EmbeddedPaymentStarter
	err error
}

func (a availStarter) EmbeddedCheckoutAvailable(context.Context, uuid.UUID, uuid.UUID) error {
	return a.err
}

// plainStarter has no embedded availability check at all.
type plainStarter struct{ PaymentStarter }

func TestEmbeddedPaymentAvailable(t *testing.T) {
	org, ch := uuid.New(), uuid.New()
	cases := []struct {
		name    string
		on      bool
		starter PaymentStarter
		org, ch uuid.UUID
		want    bool
	}{
		{"ok", true, availStarter{}, org, ch, true},
		{"switch off", false, availStarter{}, org, ch, false},
		{"hosted channel", true, availStarter{err: ErrEmbeddedCheckoutUnavailable}, org, ch, false},
		{"no key or config error", true, availStarter{err: errors.New("boom")}, org, ch, false},
		{"starter without the check", true, plainStarter{}, org, ch, false},
		{"no starter", true, nil, org, ch, false},
		{"no channel", true, availStarter{}, org, uuid.Nil, false},
	}
	for _, tc := range cases {
		h := &Handler{logger: slog.Default(), embeddedCheckout: tc.on, paymentStarter: tc.starter}
		if got := h.embeddedPaymentAvailable(context.Background(), tc.org, tc.ch); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

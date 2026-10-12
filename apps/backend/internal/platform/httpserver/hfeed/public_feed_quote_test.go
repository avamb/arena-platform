package hfeed

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// A buyer must not be able to tell WHY a code is refused where the reason would
// reveal a code they are not entitled to: unknown, paused, not yet valid,
// expired and the three scope mismatches all answer identically.
func TestQuotePromoError_NeutralWhereItMustBe(t *testing.T) {
	neutral := []string{
		"promo.not_found",
		"promo.not_active",
		"promo.not_yet_valid",
		"promo.expired",
		"promo.session_not_applicable",
		"promo.tier_not_applicable",
		"promo.currency_mismatch",
		"something.unforeseen",
	}
	wantCode, wantMsg := publicPromoError("promo.not_found")
	if wantCode != "promo.not_applicable" {
		t.Fatalf("unknown code = %q, want promo.not_applicable", wantCode)
	}
	for _, in := range neutral {
		code, msg := publicPromoError(in)
		if code != wantCode || msg != wantMsg {
			t.Errorf("%s → (%q, %q); want the one neutral answer (%q, %q)", in, code, msg, wantCode, wantMsg)
		}
	}
}

// Only verdicts reached AFTER the code is proven to fit the cart name their cause.
func TestQuotePromoError_NamesTheCauseOnlyForACodeThatFits(t *testing.T) {
	cases := map[string]string{
		hcheckout.PromoErrExhausted:        "promo.exhausted",
		hcheckout.PromoErrPerCustomerLimit: "promo.per_customer_limit",
		"promo.invalid_order_amount":       "promo.minimum_not_reached",
	}
	for in, want := range cases {
		if got, _ := publicPromoError(in); got != want {
			t.Errorf("%s → %q, want %q", in, got, want)
		}
	}
}

// A code that is named but cannot be checked is never ignored: charging the
// full price for a cart the buyer was promised a discount on is worse than
// refusing it.
func TestFindPromoCode_FailsClosedWithoutAStore(t *testing.T) {
	h := &Handler{}
	if row, err := h.findPromoCode(context.Background(), uuid.New(), "   "); row != nil || err != nil {
		t.Fatalf("blank code = (%v, %v), want no code and no error", row, err)
	}
	if _, err := h.findPromoCode(context.Background(), uuid.New(), "TEN"); !errors.Is(err, errPromoUnavailable) {
		t.Fatalf("named code without a store: err = %v, want errPromoUnavailable", err)
	}
}

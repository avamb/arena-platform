package refunds

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The alert of a dashboard refund says what actually happened to the order.
func TestAlertText_DashboardRefundSaysWhatHappened(t *testing.T) {
	provider, origin := "stripe", "provider_dashboard"
	row := func(code string) Refund {
		r := Refund{ID: uuid.New(), Amount: 2500, Currency: "EUR", Provider: &provider, RequestedBy: &origin}
		if code != "" {
			r.FailureCode = &code
		}
		return r
	}
	cases := []struct {
		code      string
		want, not string
	}{
		{DashboardPartial, "review hold", "cancelled"},
		{"", "review hold", "cancelled"},
		{DashboardCapped, "tickets were cancelled", "review hold"},
		{DashboardOverPayment, "refunded twice", "review hold"},
		{DashboardLegacyParked, "parked for review from before", "review hold"},
	}
	for _, c := range cases {
		got := alertText(row(c.code), "")
		if !strings.Contains(got, c.want) || strings.Contains(got, c.not) {
			t.Errorf("code %q: %q; want it to say %q and not %q", c.code, got, c.want, c.not)
		}
	}
}

// Round-3 review R3-2: a dashboard refund the provider reversed says so, not
// "partial, review hold".
func TestAlertText_DashboardRefundReversed(t *testing.T) {
	provider, origin, code, state := "stripe", "provider_dashboard", failureReversedAfterSuccess, "failed"
	r := Refund{ID: uuid.New(), Amount: 2500, Currency: "EUR", Provider: &provider, RequestedBy: &origin,
		FailureCode: &code, ProviderStatus: &state, State: StateManualReview}
	got := alertText(r, "")
	if !strings.Contains(got, "reversed") || strings.Contains(got, "review hold") {
		t.Errorf("reversed dashboard refund: %q", got)
	}
}

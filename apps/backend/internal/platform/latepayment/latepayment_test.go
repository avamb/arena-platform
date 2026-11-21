package latepayment

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	end := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)
	before, after := end.Add(-time.Minute), end.Add(time.Minute)
	cases := []struct {
		name, cs, order string
		paidAt          *time.Time
		want            string
		ok              bool
	}{
		{"buyer cancelled the checkout", "abandoned", "cancelled", nil, CaseBuyerCancelled, true},
		{"cancelled order, checkout lagging", "pricing_confirmed", "cancelled", nil, CaseBuyerCancelled, true},
		{"expired, no payment time", "expired", "expired", nil, CaseDelayedNotice, true},
		{"expired, paid in time", "expired", "pending_payment", &before, CaseDelayedNotice, true},
		{"expired, paid after the window", "expired", "expired", &after, CaseAfterWindow, true},
		{"operator review is not a late payment", "manual_review", "manual_review", nil, "", false},
		{"open checkout is not a late payment", "payment_started", "pending_payment", nil, "", false},
	}
	for _, c := range cases {
		got, ok := Classify(c.cs, c.order, c.paidAt, end)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Classify = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

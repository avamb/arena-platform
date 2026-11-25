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

// Spec 67 section 5.2: the window ends at the latest of the order's expiry,
// paying_until and the provider page's expiry; a payment inside it is never
// after_window.
func TestWindowEnd_PaymentInsidePayingIsNotAfterWindow(t *testing.T) {
	orderEnd := time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)
	paying := orderEnd.Add(10 * time.Minute)
	page := orderEnd.Add(5 * time.Minute)
	if got := WindowEnd(&orderEnd, &paying, &page); !got.Equal(paying) {
		t.Fatalf("WindowEnd = %v, want paying_until %v", got, paying)
	}
	if got := WindowEnd(&orderEnd, nil, nil); !got.Equal(orderEnd) {
		t.Fatalf("old row: WindowEnd = %v, want the order expiry", got)
	}
	if got := WindowEnd(nil, nil, nil); !got.IsZero() {
		t.Fatalf("no moments: WindowEnd = %v, want zero", got)
	}
	paidAt := orderEnd.Add(8 * time.Minute) // after the order expiry, inside paying
	if c, _ := Classify("expired", "expired", &paidAt, WindowEnd(&orderEnd, &paying, &page)); c != CaseDelayedNotice {
		t.Fatalf("payment inside paying_until classified %q, want %q", c, CaseDelayedNotice)
	}
	if c, _ := Classify("expired", "expired", &paidAt, WindowEnd(&orderEnd, nil, nil)); c != CaseAfterWindow {
		t.Fatalf("old row: payment after the order expiry classified %q, want %q", c, CaseAfterWindow)
	}
}

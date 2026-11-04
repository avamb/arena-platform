package refunds

import (
	"strings"
	"testing"
	"time"
)

// The SQL turns the ordinary windows into the cashback's by adding fixed
// intervals; they must stay equal to the Go constants (spec 52 section 6.2:
// Stripe holds an unfunded refund for 45 days, the cashback hears its verdict).
func TestCashbackPendingWindowsMatchTheSQL(t *testing.T) {
	if got := CashbackPendingParkAfter - PendingParkAfter; got != 43*24*time.Hour ||
		cashbackParkExtraSQL != `interval '43 days'` {
		t.Errorf("park extra %v vs SQL %s", got, cashbackParkExtraSQL)
	}
	if got := CashbackLookupEvery - LookupAfter; got != 350*time.Minute ||
		cashbackLookupExtraSQL != `interval '350 minutes'` {
		t.Errorf("lookup extra %v vs SQL %s", got, cashbackLookupExtraSQL)
	}
	if CashbackPendingParkAfter <= 45*24*time.Hour {
		t.Error("a cashback must outwait Stripe's 45 days")
	}
	if !strings.Contains(stuckWhere, cashbackOnlySQL) {
		t.Error("stuckWhere does not give the cashback its own window")
	}
}

func TestCashbackOnly(t *testing.T) {
	for _, c := range []struct {
		r    Refund
		want bool
	}{
		{Refund{Origin: OriginReferralCashback}, true},
		{Refund{Origin: OriginReferralCashback, CancelTicket: true}, false},
		{Refund{Origin: OriginArena}, false},
	} {
		if got := c.r.cashbackOnly(); got != c.want {
			t.Errorf("%+v: cashbackOnly = %v", c.r, got)
		}
	}
}

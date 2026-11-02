package referral

import "testing"

func TestLetterKindFor(t *testing.T) {
	cases := []struct {
		state, reason string
		kind          string
		ok            bool
	}{
		{StatePaid, "", LetterSent, true},
		{StatePaid, "partial", LetterSent, true},
		{StateNotPossible, "provider_declined", LetterUnavailable, true},
		{StateNotPossible, "card_closed", LetterUnavailable, true},
		{StateNotPossible, "no_refundable_ticket", LetterUnavailable, true},
		{StateNotPossible, "refund.seller_site_order", LetterUnavailable, true},
		{StateNotPossible, "refund.provider_not_supported", LetterUnavailable, true},
		// Nothing was promised, or the outcome is not final.
		{StateNotPossible, "currency_mismatch", "", false},
		{StateNotPossible, "shop_not_supported", "", false},
		{StateNotPossible, "manual_review", "", false},
		{StateSent, "", "", false},
		{StateDue, "", "", false},
		{StateCapped, "cap_reached", "", false},
		{StateCancelled, "friend_refunded", "", false},
		{StateCancelled, "self_referral", "", false},
		{StateFailed, "too_many_attempts", "", false},
	}
	for _, c := range cases {
		kind, ok := LetterKindFor(c.state, c.reason)
		if kind != c.kind || ok != c.ok {
			t.Errorf("LetterKindFor(%q, %q) = %q, %v; want %q, %v", c.state, c.reason, kind, ok, c.kind, c.ok)
		}
	}
}

func TestReversalFacts_CodePaused(t *testing.T) {
	if (ReversalFacts{CodeStatus: "active", ReversalsOnCode: 1}).CodePaused() {
		t.Error("one reversal does not pause a code")
	}
	if !(ReversalFacts{CodeStatus: "active", ReversalsOnCode: pauseAfterReversals}).CodePaused() {
		t.Error("the sweep pauses a code at the limit: the notice says so")
	}
	if !(ReversalFacts{CodeStatus: "paused"}).CodePaused() {
		t.Error("a paused code is paused")
	}
}

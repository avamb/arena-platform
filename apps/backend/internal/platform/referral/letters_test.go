package referral

import "testing"

func TestLetterKindFor(t *testing.T) {
	cases := []struct {
		state, reason, providerCode string
		kind                        string
		ok                          bool
	}{
		{StatePaid, "", "", LetterSent, true},
		{StatePaid, "partial", "", LetterSent, true},
		// The provider received the refund and refused it.
		{StateNotPossible, "provider_declined", "", LetterUnavailable, true},
		{StateNotPossible, "card_closed", "card_closed", LetterUnavailable, true},
		// Nothing was promised, or the outcome is not final.
		{StateSent, "", "", "", false},
		{StateDue, "", "", "", false},
		{StateCapped, "cap_reached", "", "", false},
		{StateCancelled, "friend_refunded", "", "", false},
		{StateCancelled, "self_referral", "", "", false},
		{StateFailed, "too_many_attempts", "", "", false},
	}
	for _, c := range cases {
		kind, ok := LetterKindFor(c.state, c.reason, c.providerCode)
		if kind != c.kind || ok != c.ok {
			t.Errorf("LetterKindFor(%q, %q, %q) = %q, %v; want %q, %v", c.state, c.reason, c.providerCode, kind, ok, c.kind, c.ok)
		}
	}
}

// Every reason the platform decided without asking the provider gets no letter,
// whatever else is stored on the reward, and so does any reason added later.
func TestLetterKindFor_PlatformReasonsSendNothing(t *testing.T) {
	for _, reason := range []string{
		"no_refundable_ticket", "refund.nothing_to_refund", "payment_not_succeeded", "no_payment",
		"refund.order_not_found", "provider_unsupported", "currency_mismatch", "manual_review",
		"other_event", "max_friends", "self_referral", "refund_not_sent", "provider_not_supported",
		"partial_not_supported", "shop_not_supported", "no_order", "refund.seller_site_order",
		"refund.provider_not_supported", "cap_reached", "a_reason_added_next_year",
	} {
		if kind, ok := LetterKindFor(StateNotPossible, reason, ""); ok {
			t.Errorf("reason %q: got the %q letter; the provider never refused anything", reason, kind)
		}
		// Even the provider's code of a parked refund does not make the reason
		// the provider's: the reason must be the code itself.
		if kind, ok := LetterKindFor(StateNotPossible, reason, "reversed_after_success"); ok {
			t.Errorf("reason %q with a stored provider code: got the %q letter", reason, kind)
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

package hcheckout

import (
	"encoding/json"
	"testing"
)

// charge.refund.updated is an update, not a success: a refund Stripe holds
// pending for lack of balance, or cancels after 45 days, must never be marked
// succeeded by the legacy refund webhook.
func TestRefundUpdatedTarget(t *testing.T) {
	cases := []struct {
		payload string
		target  string
		known   bool
	}{
		{`{"type":"charge.refund.updated","data":{"object":{"id":"re_1","status":"succeeded"}}}`, "succeeded", true},
		{`{"data":{"object":{"status":"pending","pending_reason":"insufficient_funds"}}}`, "", false},
		{`{"data":{"object":{"status":"failed","failure_reason":"insufficient_funds"}}}`, "failed", true},
		{`{"data":{"object":{"status":"canceled"}}}`, "failed", true},
		{`{"data":{"object":{"status":"requires_action"}}}`, "", false},
		{`{"id":"re_1","status":"succeeded"}`, "succeeded", true},
		{`{"id":"re_1"}`, "", false},
		{``, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		target, known := refundUpdatedTarget(json.RawMessage(c.payload))
		if target != c.target || known != c.known {
			t.Errorf("%s: got %q, %v; want %q, %v", c.payload, target, known, c.target, c.known)
		}
	}
}

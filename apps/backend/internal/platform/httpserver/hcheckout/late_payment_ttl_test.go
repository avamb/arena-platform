package hcheckout

import (
	"testing"
	"time"
)

// A re-acquired late-payment hold lives as long as a normal widget hold (the
// payment window plus its grace) and never less than lateReholdMinTTL.
func TestLateReholdTTL(t *testing.T) {
	cases := []struct {
		name          string
		window, grace time.Duration
		want          time.Duration
	}{
		{"default widget window", 1860 * time.Second, 120 * time.Second, 33 * time.Minute},
		{"not configured", 0, 0, lateReholdMinTTL},
		{"shorter than the floor", 5 * time.Minute, time.Minute, lateReholdMinTTL},
	}
	for _, c := range cases {
		h := (&Handler{}).WithLatePaymentHold(c.window, c.grace)
		if got := lateReholdTTL(h.lateHoldTTL); got != c.want {
			t.Errorf("%s: TTL = %v, want %v", c.name, got, c.want)
		}
	}
}

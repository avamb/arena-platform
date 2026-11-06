package hfeed

import (
	"testing"
	"time"
)

func TestEffectivePaymentWindow(t *testing.T) {
	global := 1860 * time.Second
	twenty, hour := int32(1200), int32(3600)
	cases := []struct {
		name     string
		global   time.Duration
		override *int32
		floor    int
		want     time.Duration
	}{
		{"no channel value", global, nil, 0, global},
		{"AllPay: the channel's 20 minutes", global, &twenty, 0, 20 * time.Minute},
		{"Stripe: never below the deployment window", global, &twenty, 1800, global},
		{"Stripe: a longer channel window is honoured", global, &hour, 1800, time.Hour},
		{"no deployment window: nothing changes", 0, &twenty, 0, 0},
	}
	for _, c := range cases {
		if got := EffectivePaymentWindow(c.global, c.override, c.floor); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestProviderPageFloor_StripeOnly(t *testing.T) {
	if providerPageFloor("stripe") < 1800 {
		t.Fatal("Stripe must declare its 30-minute floor")
	}
	if providerPageFloor("allpay") != 0 {
		t.Fatal("AllPay has no floor: the channel's window applies")
	}
}

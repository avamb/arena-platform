package httpserver

import "testing"

// One subscriber is given a whole /64, so the per-IP limits count an IPv6
// address by its /64 prefix: stepping through addresses of one prefix must not
// find a fresh budget each time.
func TestIPRateKey(t *testing.T) {
	same := [][2]string{
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ffff"},
		{"2001:db8:1:2::1", "2001:DB8:1:2:0:0:0:9"},
		{"::ffff:203.0.113.7", "203.0.113.7"}, // an IPv4-mapped address is the IPv4 address
	}
	for _, p := range same {
		if a, b := ipRateKey(p[0]), ipRateKey(p[1]); a != b {
			t.Errorf("ipRateKey(%q) = %q, ipRateKey(%q) = %q; want the same key", p[0], a, p[1], b)
		}
	}
	different := [][2]string{
		{"2001:db8:1:2::1", "2001:db8:1:3::1"},
		{"203.0.113.7", "203.0.113.8"},
	}
	for _, p := range different {
		if a, b := ipRateKey(p[0]), ipRateKey(p[1]); a == b {
			t.Errorf("ipRateKey(%q) = ipRateKey(%q) = %q; want different keys", p[0], p[1], a)
		}
	}
	if got := ipRateKey("not-an-ip"); got != "not-an-ip" {
		t.Errorf("ipRateKey of a non-address = %q, want it unchanged", got)
	}
	if got := ipRateKey("203.0.113.7"); got != "203.0.113.7" {
		t.Errorf("ipRateKey of an IPv4 address = %q, want it unchanged", got)
	}
}

func TestRateLimiter_IPv6AddressesOfOnePrefixShareTheIPBudget(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 2)
	if ok, _ := rl.checkIP("2001:db8:1:2::1"); !ok {
		t.Fatal("first request refused")
	}
	if ok, _ := rl.checkIP("2001:db8:1:2::2"); !ok {
		t.Fatal("second request refused")
	}
	if ok, _ := rl.checkIP("2001:db8:1:2::3"); ok {
		t.Error("third address of the same /64 got a fresh budget")
	}
	if ok, _ := rl.checkIP("2001:db8:1:3::1"); !ok {
		t.Error("an address of another /64 was refused")
	}
}

func TestRateLimiter_RefusedCodeBudgetCountsAnIPv6PrefixOnce(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 0)
	rl.promoFailIPLimit = 3
	for _, ip := range []string{"2001:db8:1:2::1", "2001:db8:1:2::2", "2001:db8:1:2::3"} {
		rl.RecordPromoFailure("tok", ip)
	}
	if blocked, _ := rl.PromoFailuresBlocked("tok", "2001:db8:1:2::99"); !blocked {
		t.Error("three refusals from one /64 did not lock out a fourth address of it")
	}
	if blocked, _ := rl.PromoFailuresBlocked("tok", "2001:db8:1:9::1"); blocked {
		t.Error("another /64 was locked out")
	}
}

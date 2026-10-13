package httpserver

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func promoLimiter(tokenLimit, ipLimit int) *publicFeedRateLimiter {
	return newPublicFeedRateLimiterFromConfig(&config.Config{
		PublicPromoFailTokenLimit: tokenLimit,
		PublicPromoFailIPLimit:    ipLimit,
	})
}

// refuse takes a slot and keeps it, as a refused code does. It reports whether
// the budget turned the attempt away instead.
func refuse(rl *publicFeedRateLimiter, token, ip string) (blocked bool, retry int) {
	_, blocked, retry = rl.ReservePromoAttempt(token, ip)
	return blocked, retry
}

// accept takes a slot and gives it back, as an accepted code does.
func accept(t *testing.T, rl *publicFeedRateLimiter, token, ip string) {
	t.Helper()
	release, blocked, _ := rl.ReservePromoAttempt(token, ip)
	if blocked {
		t.Fatalf("an attempt was turned away by %s/%s", token, ip)
	}
	release()
}

// Only a kept slot (a refusal) counts; an accepted code gives its slot back, and
// the budget is spent once the limit is reached.
func TestPromoFailLimiter_OnlyRefusalsCount(t *testing.T) {
	rl := promoLimiter(100, 3)
	for i := 0; i < 50; i++ {
		accept(t, rl, "tok", "1.2.3.4")
	}
	for i := 0; i < 2; i++ {
		if blocked, _ := refuse(rl, "tok", "1.2.3.4"); blocked {
			t.Fatalf("refusal %d turned away below the limit", i+1)
		}
	}
	if blocked, _ := refuse(rl, "tok", "1.2.3.4"); blocked {
		t.Fatal("the third refusal of three was turned away")
	}
	blocked, retry := refuse(rl, "tok", "1.2.3.4")
	if !blocked || retry < 1 || retry > 60 {
		t.Fatalf("at 3 of 3: blocked=%v retry=%d, want blocked with a Retry-After of 1-60 s", blocked, retry)
	}
}

// The slot is taken when the request names the code, so a burst cannot all pass
// a check made before any of them is counted: of N concurrent refusals exactly
// `limit` get through.
func TestPromoFailLimiter_ConcurrentAttemptsCannotOvershootTheLimit(t *testing.T) {
	const limit, burst = 3, 200
	rl := promoLimiter(0, limit)
	var admitted, turnedAway atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if blocked, _ := refuse(rl, "tok", "9.9.9.9"); blocked {
				turnedAway.Add(1)
			} else {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted.Load() != limit || turnedAway.Load() != burst-limit {
		t.Fatalf("admitted %d, turned away %d; want %d and %d", admitted.Load(), turnedAway.Load(), limit, burst-limit)
	}
}

// A slot given back is free again, and a release after the window ended does
// not eat into the new window.
func TestPromoFailLimiter_ReleaseFreesTheSlot(t *testing.T) {
	rl := promoLimiter(0, 1)
	release, blocked, _ := rl.ReservePromoAttempt("tok", "ip")
	if blocked {
		t.Fatal("first attempt turned away")
	}
	if _, blocked, _ := rl.ReservePromoAttempt("tok", "ip"); !blocked {
		t.Fatal("second attempt admitted while the first holds the only slot")
	}
	release()
	if _, blocked, _ := rl.ReservePromoAttempt("tok", "ip"); blocked {
		t.Fatal("slot not free after release")
	}

	// Window rollover: an old window's release must not touch the new one.
	rl2 := promoLimiter(0, 1)
	oldRelease, _, _ := rl2.ReservePromoAttempt("tok", "ip")
	rl2.mu.Lock()
	rl2.promoFailIPs["ip"].resetAt = time.Now().Add(-time.Second)
	rl2.mu.Unlock()
	if _, blocked, _ := rl2.ReservePromoAttempt("tok", "ip"); blocked {
		t.Fatal("new window did not start")
	}
	oldRelease() // belongs to the ended window
	if _, blocked, _ := rl2.ReservePromoAttempt("tok", "ip"); !blocked {
		t.Fatal("a stale release freed a slot of the new window")
	}
}

// The IP budget stops one visitor; the token budget is site-wide and stops
// anybody once spent; one IP's refusals do not block another IP.
func TestPromoFailLimiter_IPAndTokenAreIndependent(t *testing.T) {
	rl := promoLimiter(4, 2)
	refuse(rl, "tok", "1.1.1.1")
	refuse(rl, "tok", "1.1.1.1")
	if blocked, _ := refuse(rl, "tok", "1.1.1.1"); !blocked {
		t.Error("the visitor over the IP budget is not blocked")
	}
	if blocked, _ := refuse(rl, "tok", "2.2.2.2"); blocked {
		t.Error("another visitor on the same token is blocked by the first one's refusals")
	}
	// token window: 1.1.1.1 x2 + 2.2.2.2 x1 = 3 of 4
	refuse(rl, "tok", "3.3.3.3")
	if blocked, _ := refuse(rl, "tok", "4.4.4.4"); !blocked {
		t.Error("a fresh visitor is not blocked once the site-wide token budget is spent")
	}
	if blocked, _ := refuse(rl, "other-token", "4.4.4.4"); blocked {
		t.Error("another feed token is blocked by this token's budget")
	}
}

func TestPromoFailLimiter_ZeroDisablesAndWindowRenews(t *testing.T) {
	off := promoLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		if blocked, _ := refuse(off, "tok", "ip"); blocked {
			t.Fatal("a limit of 0 must disable the check")
		}
	}

	rl := promoLimiter(0, 1)
	refuse(rl, "tok", "ip")
	if blocked, _ := refuse(rl, "tok", "ip"); !blocked {
		t.Fatal("not blocked at the limit")
	}
	// Age the window out: the budget renews.
	rl.mu.Lock()
	rl.promoFailIPs["ip"].resetAt = time.Now().Add(-time.Second)
	rl.mu.Unlock()
	if blocked, _ := refuse(rl, "tok", "ip"); blocked {
		t.Fatal("still blocked after the window ended")
	}
}

func TestPromoFailLimiter_DefaultsApplyWithoutAConfig(t *testing.T) {
	rl := newPublicFeedRateLimiterFromConfig(nil)
	if rl.promoFailIPLimit != defaultPublicPromoFailIPLimit || rl.promoFailTokenLimit != defaultPublicPromoFailTokenLimit {
		t.Fatalf("defaults = %d/%d, want %d/%d", rl.promoFailTokenLimit, rl.promoFailIPLimit,
			defaultPublicPromoFailTokenLimit, defaultPublicPromoFailIPLimit)
	}
}

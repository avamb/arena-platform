package httpserver

import (
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

// Peeking counts nothing; only a recorded refusal does, and the budget is
// spent once the limit is reached.
func TestPromoFailLimiter_OnlyRefusalsCount(t *testing.T) {
	rl := promoLimiter(100, 3)
	for i := 0; i < 50; i++ {
		if blocked, _ := rl.PromoFailuresBlocked("tok", "1.2.3.4"); blocked {
			t.Fatalf("blocked after %d peeks and no refusal", i)
		}
	}
	for i := 0; i < 2; i++ {
		rl.RecordPromoFailure("tok", "1.2.3.4")
	}
	if blocked, _ := rl.PromoFailuresBlocked("tok", "1.2.3.4"); blocked {
		t.Fatal("blocked at 2 of 3 refusals")
	}
	rl.RecordPromoFailure("tok", "1.2.3.4")
	blocked, retry := rl.PromoFailuresBlocked("tok", "1.2.3.4")
	if !blocked || retry < 1 || retry > 60 {
		t.Fatalf("at 3 of 3: blocked=%v retry=%d, want blocked with a Retry-After of 1-60 s", blocked, retry)
	}
}

// The IP budget stops one visitor; the token budget is site-wide and stops
// anybody once spent; one IP's refusals do not block another IP.
func TestPromoFailLimiter_IPAndTokenAreIndependent(t *testing.T) {
	rl := promoLimiter(4, 2)
	rl.RecordPromoFailure("tok", "1.1.1.1")
	rl.RecordPromoFailure("tok", "1.1.1.1")
	if blocked, _ := rl.PromoFailuresBlocked("tok", "1.1.1.1"); !blocked {
		t.Error("the visitor over the IP budget is not blocked")
	}
	if blocked, _ := rl.PromoFailuresBlocked("tok", "2.2.2.2"); blocked {
		t.Error("another visitor on the same token is blocked by the first one's refusals")
	}
	rl.RecordPromoFailure("tok", "2.2.2.2")
	rl.RecordPromoFailure("tok", "3.3.3.3")
	if blocked, _ := rl.PromoFailuresBlocked("tok", "4.4.4.4"); !blocked {
		t.Error("a fresh visitor is not blocked once the site-wide token budget is spent")
	}
	if blocked, _ := rl.PromoFailuresBlocked("other-token", "4.4.4.4"); blocked {
		t.Error("another feed token is blocked by this token's budget")
	}
}

func TestPromoFailLimiter_ZeroDisablesAndWindowRenews(t *testing.T) {
	off := promoLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		off.RecordPromoFailure("tok", "ip")
	}
	if blocked, _ := off.PromoFailuresBlocked("tok", "ip"); blocked {
		t.Fatal("a limit of 0 must disable the check")
	}

	rl := promoLimiter(0, 1)
	rl.RecordPromoFailure("tok", "ip")
	if blocked, _ := rl.PromoFailuresBlocked("tok", "ip"); !blocked {
		t.Fatal("not blocked at the limit")
	}
	// Age the window out: the budget renews.
	rl.mu.Lock()
	rl.promoFailIPs["ip"].resetAt = time.Now().Add(-time.Second)
	rl.mu.Unlock()
	if blocked, _ := rl.PromoFailuresBlocked("tok", "ip"); blocked {
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

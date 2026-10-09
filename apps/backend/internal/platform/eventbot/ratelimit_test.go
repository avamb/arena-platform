package eventbot

import (
	"testing"
	"time"
)

// The 31st update within a minute is refused, and only the first refusal of
// the burst asks for a "wait a minute" answer.
func TestRateLimiter_ThirtyFirstIsRefusedAndFlaggedOnce(t *testing.T) {
	l := newUserRateLimiter(messageRateLimit, messageRateWindow)
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < messageRateLimit; i++ {
		if ok, first := l.Allow(1, t0.Add(time.Duration(i)*time.Second)); !ok || first {
			t.Fatalf("update %d refused (first=%v)", i+1, first)
		}
	}
	ok, first := l.Allow(1, t0.Add(31*time.Second))
	if ok || !first {
		t.Fatalf("31st: allowed=%v first=%v, want refused and flagged", ok, first)
	}
	for i := 0; i < 5; i++ {
		if ok, first := l.Allow(1, t0.Add(32*time.Second)); ok || first {
			t.Fatalf("later refusal %d: allowed=%v first=%v, want silent refusal", i, ok, first)
		}
	}
}

// The window slides: once the oldest update is a minute old a new one gets
// in, and a fresh burst is flagged again.
func TestRateLimiter_WindowSlides(t *testing.T) {
	l := newUserRateLimiter(messageRateLimit, messageRateWindow)
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < messageRateLimit; i++ {
		l.Allow(1, t0.Add(time.Duration(i)*time.Second))
	}
	if ok, _ := l.Allow(1, t0.Add(59*time.Second)); ok {
		t.Fatal("the window is still full at 59 s")
	}
	// At 60 s the first update (t0) has left the window; at 60.5 s the
	// second (t0+1s) has not.
	if ok, _ := l.Allow(1, t0.Add(60*time.Second)); !ok {
		t.Fatal("the oldest update must have left the window at 60 s")
	}
	ok, first := l.Allow(1, t0.Add(60*time.Second+500*time.Millisecond))
	if ok || !first {
		t.Fatalf("a new burst after an admitted update: allowed=%v first=%v", ok, first)
	}
}

// Two accounts have two windows.
func TestRateLimiter_AccountsAreIndependent(t *testing.T) {
	l := newUserRateLimiter(messageRateLimit, messageRateWindow)
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i <= messageRateLimit; i++ {
		l.Allow(1, t0)
	}
	if ok, _ := l.Allow(1, t0); ok {
		t.Fatal("account 1 is over its limit")
	}
	if ok, first := l.Allow(2, t0); !ok || first {
		t.Fatal("account 2 must not pay for account 1")
	}
}

// Messages and button presses have independent windows: a person whose 31st
// typed message is refused can still press buttons, up to their own wider
// budget.
func TestRateLimiter_MessagesAndCallbacksAreIndependent(t *testing.T) {
	b := &Bot{
		limiter:   newUserRateLimiter(messageRateLimit, messageRateWindow),
		cbLimiter: newUserRateLimiter(callbackRateLimit, messageRateWindow),
	}
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < messageRateLimit; i++ {
		if ok, _ := b.limiter.Allow(1, t0); !ok {
			t.Fatalf("message %d refused", i+1)
		}
	}
	if ok, first := b.limiter.Allow(1, t0); ok || !first {
		t.Fatalf("31st message: allowed=%v first=%v, want refused and flagged", ok, first)
	}
	for i := 0; i < callbackRateLimit; i++ {
		if ok, _ := b.cbLimiter.Allow(1, t0); !ok {
			t.Fatalf("button press %d refused while only the message window is full", i+1)
		}
	}
	if ok, first := b.cbLimiter.Allow(1, t0); ok || !first {
		t.Fatalf("press %d: allowed=%v first=%v, want refused and flagged", callbackRateLimit+1, ok, first)
	}
	if callbackRateLimit <= messageRateLimit {
		t.Fatal("buttons must have the wider budget")
	}
}

// Idle accounts are forgotten, and a limiter with no limit admits everything.
func TestRateLimiter_PrunesIdleAccountsAndCanBeOff(t *testing.T) {
	l := newUserRateLimiter(messageRateLimit, messageRateWindow)
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for id := int64(1); id <= 1000; id++ {
		l.Allow(id, t0)
	}
	later := t0.Add(2 * messageRateWindow)
	for i := 0; i < 1000+ratePruneMinCalls; i++ {
		l.Allow(5000, later)
	}
	l.mu.Lock()
	n := len(l.users)
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("after the prune %d accounts remain, want only the active one", n)
	}

	off := newUserRateLimiter(rateLimitFrom(-1), messageRateWindow)
	for i := 0; i < 10*messageRateLimit; i++ {
		if ok, _ := off.Allow(1, t0); !ok {
			t.Fatal("a switched-off limiter must admit everything")
		}
	}
	if rateLimitFrom(0) != messageRateLimit {
		t.Fatal("the default limit is the spec's 30")
	}
}

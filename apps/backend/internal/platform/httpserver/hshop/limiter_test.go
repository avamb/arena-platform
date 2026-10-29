package hshop

import (
	"testing"
	"time"
)

func TestMinuteLimiter_CountsPerKeyAndResetsAfterAMinute(t *testing.T) {
	l := newMinuteLimiter(2)
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("a", t0); !ok {
			t.Fatalf("request %d refused inside the budget", i+1)
		}
	}
	ok, retry := l.allow("a", t0.Add(20*time.Second))
	if ok || retry != 40 {
		t.Fatalf("third request = %v retry %d; want refused, retry 40", ok, retry)
	}
	if ok, _ := l.allow("b", t0.Add(20*time.Second)); !ok {
		t.Fatal("another address shares the budget")
	}
	if ok, _ := l.allow("a", t0.Add(61*time.Second)); !ok {
		t.Fatal("the budget did not come back after a minute")
	}
}

func TestMinuteLimiter_ReleaseGivesTheBudgetBack(t *testing.T) {
	l := newMinuteLimiter(1)
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if ok, _ := l.allow("order", t0); !ok {
		t.Fatal("first request refused")
	}
	if ok, _ := l.allow("order", t0.Add(time.Second)); ok {
		t.Fatal("second request inside the minute was allowed")
	}
	l.release("order")
	if ok, _ := l.allow("order", t0.Add(2*time.Second)); !ok {
		t.Fatal("a released request still cost the budget")
	}
	l.release("never-seen") // must not panic
}

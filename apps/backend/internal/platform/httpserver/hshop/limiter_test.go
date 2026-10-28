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

package ratelimit

import (
	"testing"
	"time"
)

// Take is a quota: a refused attempt is not recorded, so asking after the
// limit neither extends the window nor grows the stored slice.
func TestTake_RefusedAttemptIsNotRecorded(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	sw := New(Config{MaxAttempts: 2, Window: time.Hour, Now: func() time.Time { return now }})

	if !sw.Take("org") || !sw.Take("org") {
		t.Fatal("the first two takes must pass")
	}
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		if sw.Take("org") {
			t.Fatalf("take %d after the limit passed", i+3)
		}
	}
	if n := sw.Count("org"); n != 2 {
		t.Fatalf("count = %d after refused takes, want 2", n)
	}
	// One hour after the two successful takes the window is free again,
	// however often the key was refused in between.
	now = time.Date(2026, 10, 15, 13, 0, 1, 0, time.UTC)
	if !sw.Take("org") {
		t.Fatal("the window must reopen one hour after the recorded takes")
	}
	if !sw.Take("other") {
		t.Fatal("another key has its own quota")
	}
}

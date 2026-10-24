package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// Take is a quota: a refused attempt is not recorded, so asking after the
// limit neither extends the window nor grows the stored slice.
func TestTake_RefusedAttemptIsNotRecorded(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	sw := New(Config{MaxAttempts: 2, Window: time.Hour, Now: func() time.Time { return now }})

	for i := 0; i < 2; i++ {
		if !sw.Take("org") {
			t.Fatalf("take %d must pass", i+1)
		}
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

// Release gives the last slot back, so a caller whose second quota refused
// does not burn the first.
func TestRelease_GivesTheLastSlotBack(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	sw := New(Config{MaxAttempts: 2, Window: time.Hour, Now: func() time.Time { return now }})

	sw.Release("org") // no record: nothing happens
	first, second := sw.Take("org"), sw.Take("org")
	if !first || !second {
		t.Fatal("two takes must pass")
	}
	if sw.Take("org") {
		t.Fatal("the third take must be refused")
	}
	sw.Release("org")
	if n := sw.Count("org"); n != 1 {
		t.Fatalf("count after Release = %d, want 1", n)
	}
	if !sw.Take("org") {
		t.Fatal("the released slot must be free again")
	}
	// Records that expired are not what Release removes.
	now = now.Add(2 * time.Hour)
	sw.Release("org")
	if n := sw.Count("org"); n != 0 {
		t.Fatalf("count after the window passed = %d, want 0", n)
	}
	x, y, z := sw.Take("org"), sw.Take("org"), sw.Take("org")
	if !x || !y || z {
		t.Fatal("a Release on an empty window must not create extra room")
	}
}

// Many goroutines take and release the same key: the window never holds more
// than its limit, and in the end exactly the slots that were kept are used.
func TestTakeRelease_Concurrent(t *testing.T) {
	sw := New(Config{MaxAttempts: 10, Window: time.Hour})
	var wg sync.WaitGroup
	kept := make(chan struct{}, 200)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sw.Take("k") {
				return
			}
			if i%2 == 0 {
				sw.Release("k")
				return
			}
			kept <- struct{}{}
		}()
	}
	wg.Wait()
	close(kept)
	if got, want := sw.Count("k"), len(kept); got != want || got > 10 {
		t.Fatalf("count = %d, kept = %d", got, want)
	}
}

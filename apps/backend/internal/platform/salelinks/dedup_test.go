package salelinks

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var day1 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func TestDedup_SameClientSameDay(t *testing.T) {
	d := NewDedup(0, 0)
	if d.Seen("x7k2qpm", "1.2.3.4", "UA", day1) {
		t.Error("the first scan must not be a repeat")
	}
	if !d.Seen("x7k2qpm", "1.2.3.4", "UA", day1.Add(time.Hour)) {
		t.Error("the same client on the same day must be a repeat")
	}
}

func TestDedup_NextDayCountsAgain(t *testing.T) {
	d := NewDedup(0, 0)
	d.Seen("x7k2qpm", "1.2.3.4", "UA", day1)
	// 14 hours later is the next UTC day: a new key even inside the TTL.
	if d.Seen("x7k2qpm", "1.2.3.4", "UA", day1.Add(14*time.Hour)) {
		t.Error("a new day is a new scan")
	}
}

func TestDedup_DifferentClientOrCode(t *testing.T) {
	d := NewDedup(0, 0)
	d.Seen("x7k2qpm", "1.2.3.4", "UA", day1)
	for name, f := range map[string]func() bool{
		"other ua":   func() bool { return d.Seen("x7k2qpm", "1.2.3.4", "Other", day1) },
		"other ip":   func() bool { return d.Seen("x7k2qpm", "1.2.3.5", "UA", day1) },
		"other code": func() bool { return d.Seen("abcdefg", "1.2.3.4", "UA", day1) },
	} {
		if f() {
			t.Errorf("%s must not be a repeat", name)
		}
	}
}

func TestDedup_TTLExpires(t *testing.T) {
	d := NewDedup(time.Hour, 10)
	d.Seen("x7k2qpm", "1.2.3.4", "UA", day1)
	if !d.Seen("x7k2qpm", "1.2.3.4", "UA", day1.Add(30*time.Minute)) {
		t.Error("inside the TTL a repeat is expected")
	}
	if d.Seen("x7k2qpm", "1.2.3.4", "UA", day1.Add(61*time.Minute)) {
		t.Error("after the TTL the scan counts again")
	}
	if got := len(d.seen); got != 1 {
		t.Errorf("an expired record must be replaced, not kept beside the new one: %d", got)
	}
}

func TestDedup_CapEvictsTheOldest(t *testing.T) {
	d := NewDedup(24*time.Hour, 3)
	for i := 0; i < 5; i++ {
		d.Seen("x7k2qpm", fmt.Sprintf("10.0.0.%d", i), "UA", day1.Add(time.Duration(i)*time.Second))
	}
	if got := len(d.seen); got != 3 {
		t.Fatalf("records = %d, want the cap 3", got)
	}
	// The two oldest (ip 0 and 1) were evicted and count again, the newest are kept.
	if d.Seen("x7k2qpm", "10.0.0.4", "UA", day1.Add(10*time.Second)) == false {
		t.Error("the newest record must still be remembered")
	}
	if d.Seen("x7k2qpm", "10.0.0.0", "UA", day1.Add(11*time.Second)) {
		t.Error("the oldest record must have been evicted")
	}
}

func TestDedup_SaltIsPerInstance(t *testing.T) {
	a, b := NewDedup(0, 0), NewDedup(0, 0)
	if a.key("c", "ip", "ua", day1) == b.key("c", "ip", "ua", day1) {
		t.Error("two instances must not share a salt")
	}
	first, again := a.key("c", "ip", "ua", day1), a.key("c", "ip", "ua", day1)
	if first != again {
		t.Error("a key must be stable inside one instance")
	}
}

func TestDedup_ParallelUse(t *testing.T) {
	d := NewDedup(time.Hour, 1000)
	var firsts atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if !d.Seen("x7k2qpm", "1.1.1.1", "UA", day1) {
					firsts.Add(1)
				}
				d.Seen("abcdefg", fmt.Sprintf("2.2.2.%d", i%100), "UA", day1)
			}
		}()
	}
	wg.Wait()
	if firsts.Load() != 1 {
		t.Errorf("exactly one goroutine may see the first scan, got %d", firsts.Load())
	}
	if len(d.seen) != 101 {
		t.Errorf("records = %d, want 101", len(d.seen))
	}
}

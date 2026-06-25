// Package clock provides a time abstraction for testability.
//
// Production code should accept a Clock interface rather than calling
// time.Now() directly. Tests can inject a FakeClock to control time.
package clock

import (
	"sync"
	"time"
)

// Clock is the interface for time-related operations.
// Inject it into structs that need deterministic time for testing.
type Clock interface {
	// Now returns the current local time.
	Now() time.Time
	// Since returns the time elapsed since t.
	Since(t time.Time) time.Duration
}

// New returns a Clock backed by the real system clock.
func New() Clock {
	return &realClock{}
}

// realClock is the production implementation of Clock using the system clock.
type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }

// FakeClock is a test-only Clock implementation whose time is fully controlled.
// The zero value has its current time set to the zero time.Time; call
// SetTime or Advance before use if a non-zero starting point is required.
//
// FakeClock is safe for concurrent use.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a FakeClock with its current time set to t.
func NewFake(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

// SetTime sets the FakeClock's current time to t.
func (f *FakeClock) SetTime(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the FakeClock forward by d. Negative values move it backward.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Now returns the FakeClock's current time.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since returns the duration elapsed since t relative to the FakeClock's
// current time (i.e. FakeClock.Now().Sub(t)).
func (f *FakeClock) Since(t time.Time) time.Duration {
	return f.Now().Sub(t)
}

// Compile-time interface guards.
var (
	_ Clock = (*realClock)(nil)
	_ Clock = (*FakeClock)(nil)
)

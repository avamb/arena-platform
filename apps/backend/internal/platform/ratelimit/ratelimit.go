// Package ratelimit provides a simple in-memory sliding-window rate limiter
// used by the login endpoint (feature #115).
//
// Design notes:
//   - Keys are arbitrary strings (e.g. "ip:email" composites).
//   - A sliding window of size WindowSize is maintained per key; timestamps
//     of past hits within the window are stored in a slice.
//   - Allow() returns true when the request is permitted, false when the
//     key has exceeded MaxAttempts within the window.
//   - The limiter is safe for concurrent use.
//   - Entry cleanup: expired entries are pruned inside Allow() to prevent
//     unbounded memory growth. An explicit Purge() is also available for
//     testing.
//
// For production deployments this limiter should be replaced (or augmented)
// by a Redis-backed implementation so limits are enforced across multiple
// instances. The interface boundary (Limiter) enables that swap without
// changing call sites.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is the minimal interface satisfied by SlidingWindow. Wire this
// interface into handlers so tests can substitute a no-op or strict limiter.
type Limiter interface {
	// Allow returns true when the given key has not exceeded the configured
	// limit within the sliding window, and records the attempt. Returns
	// false (rate limited) otherwise — the attempt is still recorded.
	Allow(key string) bool

	// Count returns the number of attempts recorded for key within the
	// current window. Used by tests to verify the counter increments.
	Count(key string) int

	// Reset clears all recorded attempts for key. Used by tests and after
	// successful authentication to give the caller a clean slate.
	Reset(key string)
}

// SlidingWindow is a thread-safe, in-memory sliding window rate limiter.
type SlidingWindow struct {
	mu          sync.Mutex
	windows     map[string][]time.Time
	maxAttempts int
	window      time.Duration
	nowFn       func() time.Time // injectable for deterministic tests
}

// Config configures a SlidingWindow limiter.
type Config struct {
	// MaxAttempts is the number of allowed requests within Window before
	// subsequent requests are denied.
	MaxAttempts int

	// Window is the duration of the sliding window.
	Window time.Duration

	// Now is an optional clock function for deterministic tests. Defaults
	// to time.Now when nil.
	Now func() time.Time
}

// New constructs a SlidingWindow limiter with the supplied configuration.
// Panics when MaxAttempts <= 0 or Window <= 0.
func New(cfg Config) *SlidingWindow {
	if cfg.MaxAttempts <= 0 {
		// allow:panic: constructor-time configuration validation. New is called
		// from boot wiring with a literal Config; a non-positive value is a
		// programmer error, not a user-supplied input.
		panic("ratelimit: MaxAttempts must be > 0")
	}
	if cfg.Window <= 0 {
		// allow:panic: constructor-time configuration validation (see MaxAttempts).
		panic("ratelimit: Window must be > 0")
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &SlidingWindow{
		windows:     make(map[string][]time.Time),
		maxAttempts: cfg.MaxAttempts,
		window:      cfg.Window,
		nowFn:       nowFn,
	}
}

// Allow reports whether the key is within the allowed rate and records the
// current attempt. It returns false (and still records) when the limit is
// exceeded so that the counter correctly tracks all attempts.
func (sw *SlidingWindow) Allow(key string) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := sw.nowFn()
	cutoff := now.Add(-sw.window)

	// Prune expired entries.
	hits := sw.windows[key]
	valid := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	// Record this attempt.
	valid = append(valid, now)
	sw.windows[key] = valid

	// Allow if count (including this attempt) does not exceed the limit.
	return len(valid) <= sw.maxAttempts
}

// Count returns the number of attempts within the current sliding window for
// the given key. Does not modify state.
func (sw *SlidingWindow) Count(key string) int {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := sw.nowFn()
	cutoff := now.Add(-sw.window)
	count := 0
	for _, t := range sw.windows[key] {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// Reset removes all recorded attempts for the given key. Call this after a
// successful login to give the caller a fresh window.
func (sw *SlidingWindow) Reset(key string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	delete(sw.windows, key)
}

// Purge removes all entries from all keys. Useful in tests that need a clean
// state between sub-tests.
func (sw *SlidingWindow) Purge() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.windows = make(map[string][]time.Time)
}

package hshop

import (
	"sync"
	"time"
)

// minuteLimiter is a fixed one-minute window per key (a client address). It
// lives on the Handler, which the server builds once, so its counts survive
// between requests (AGENTS.md: per-request handlers lose such state).
type minuteLimiter struct {
	mu        sync.Mutex
	limit     int
	windows   map[string]*minuteWindow
	lastSweep time.Time
}

type minuteWindow struct {
	start time.Time
	n     int
}

func newMinuteLimiter(limit int) *minuteLimiter {
	return &minuteLimiter{limit: limit, windows: map[string]*minuteWindow{}}
}

// release gives back the budget of one request that was counted by allow but
// did nothing (a refused request must not cost the caller a minute).
func (l *minuteLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.windows[key]; w != nil && w.n > 0 {
		w.n--
	}
}

// allow counts one request for key and says whether it fits the budget; when
// it does not, retry is the whole seconds until the window ends (at least 1).
func (l *minuteLimiter) allow(key string, now time.Time) (ok bool, retry int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > time.Minute {
		for k, w := range l.windows {
			if now.Sub(w.start) >= time.Minute {
				delete(l.windows, k)
			}
		}
		l.lastSweep = now
	}
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &minuteWindow{start: now}
		l.windows[key] = w
	}
	if w.n >= l.limit {
		left := time.Minute - now.Sub(w.start)
		return false, max(1, int((left+time.Second-1)/time.Second))
	}
	w.n++
	return true, 0
}

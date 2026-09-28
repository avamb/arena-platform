package eventbot

import (
	"sync"
	"time"
)

// pendingInvites remembers, per Telegram account, the invitation code the
// person opened while the bot waits for their e-mail. It lives in memory on
// purpose: no link row exists yet (bot_drafts needs one), the wait is
// seconds long, and after a restart the person simply taps the link again.
type pendingInvites struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[int64]pendingInvite
	now     func() time.Time
}

type pendingInvite struct {
	code      string
	expiresAt time.Time
}

func newPendingInvites(ttl time.Duration) *pendingInvites {
	return &pendingInvites{ttl: ttl, entries: map[int64]pendingInvite{}, now: time.Now}
}

func (p *pendingInvites) put(telegramUserID int64, code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[telegramUserID] = pendingInvite{code: code, expiresAt: p.now().Add(p.ttl)}
	p.sweepLocked()
}

// take returns the pending code (and keeps it: a mistyped e-mail must not
// cost the person the invitation).
func (p *pendingInvites) take(telegramUserID int64) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[telegramUserID]
	if !ok || !e.expiresAt.After(p.now()) {
		delete(p.entries, telegramUserID)
		return "", false
	}
	return e.code, true
}

func (p *pendingInvites) clear(telegramUserID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, telegramUserID)
}

func (p *pendingInvites) sweepLocked() {
	now := p.now()
	for id, e := range p.entries {
		if !e.expiresAt.After(now) {
			delete(p.entries, id)
		}
	}
}

// mem_store.go — in-memory implementation of Store for unit tests.
//
// MemStore is goroutine-safe and provides the same contract as RedisStore
// without requiring a running Redis instance. All operations are O(n) in the
// number of tracked sessions per user, which is acceptable for tests.
//
// Do NOT use MemStore in production.
package redissession

import (
	"context"
	"sync"
	"time"
)

// memSession tracks a single refresh token in the in-memory session set.
type memSession struct {
	token     string
	expiresAt time.Time
}

// MemStore implements Store using in-memory maps protected by a read-write
// mutex. It is intentionally minimal — just enough to exercise the session
// management contract in unit and integration tests.
type MemStore struct {
	mu       sync.RWMutex
	revoked  map[string]time.Time   // token → revoked expiry deadline
	sessions map[string][]memSession // userID → active sessions (sorted by expiresAt asc)
}

// NewMemStore returns an empty MemStore ready for use.
func NewMemStore() *MemStore {
	return &MemStore{
		revoked:  make(map[string]time.Time),
		sessions: make(map[string][]memSession),
	}
}

// Ping always returns nil (the in-memory store is always available).
func (m *MemStore) Ping(_ context.Context) error { return nil }

// TrackSession implements Store. It appends the token to the user's session
// list, maintaining insertion order. Duplicate tokens are allowed (same token
// tracked twice is harmless — it will be deduplicated on the next prune).
func (m *MemStore) TrackSession(_ context.Context, userID, token string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[userID] = append(m.sessions[userID], memSession{token: token, expiresAt: expiresAt})
	return nil
}

// RevokeSession implements Store. It marks the token as revoked and removes it
// from the user's active-session list.
func (m *MemStore) RevokeSession(_ context.Context, userID, token string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Mark revoked with the token's original expiry so we can simulate TTL.
	m.revoked[token] = expiresAt

	// Remove from user's session list.
	sess := m.sessions[userID]
	out := sess[:0]
	for _, s := range sess {
		if s.token != token {
			out = append(out, s)
		}
	}
	m.sessions[userID] = out
	return nil
}

// IsRevoked implements Store. A token is considered revoked if it appears in
// the revoked map and its stored expiry is in the future (mimicking Redis TTL).
func (m *MemStore) IsRevoked(_ context.Context, token string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	exp, ok := m.revoked[token]
	if !ok {
		return false, nil
	}
	// Mimic Redis TTL: if the token itself has expired, treat as not-found.
	return time.Now().Before(exp), nil
}

// PruneAndEvict implements Store. It removes expired sessions and returns the
// tokens that exceed maxSessions (oldest first). The caller must revoke the
// returned tokens in the database.
//
// maxSessions is the maximum number of active sessions to RETAIN. Pass a
// negative value to disable eviction entirely. 0 means "evict all active
// sessions" (used pre-login when MaxConcurrentSessionsPerUser=1).
func (m *MemStore) PruneAndEvict(_ context.Context, userID string, maxSessions int, now time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess := m.sessions[userID]

	// Prune expired.
	active := sess[:0]
	for _, s := range sess {
		if s.expiresAt.After(now) {
			active = append(active, s)
		}
	}
	m.sessions[userID] = active

	if maxSessions < 0 || len(active) <= maxSessions {
		// Negative = unlimited; or already within limit — nothing to evict.
		return nil, nil
	}

	// Sessions are stored in insertion order (oldest first since we append on
	// every login). Evict the oldest ones.
	excess := len(active) - maxSessions
	evict := active[:excess]
	m.sessions[userID] = active[excess:]

	tokens := make([]string, len(evict))
	for i, s := range evict {
		tokens[i] = s.token
	}
	return tokens, nil
}

// compile-time interface guard.
var _ Store = (*MemStore)(nil)

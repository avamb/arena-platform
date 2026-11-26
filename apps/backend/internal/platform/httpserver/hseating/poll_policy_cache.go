// poll_policy_cache.go keeps the hold policy of a session in memory for 30
// seconds, so a seat-status poll does not read the profile from the database
// on every request (spec 08_architecture/67, section 7).
package hseating

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
)

const pollPolicyTTL = 30 * time.Second

// pollPolicyMaxEntries bounds the map; a full map is emptied, the next polls
// refill it.
const pollPolicyMaxEntries = 4096

type pollPolicyEntry struct {
	policy  holdpolicy.Policy
	expires time.Time
}

var pollPolicyCache = struct {
	mu sync.Mutex
	m  map[uuid.UUID]pollPolicyEntry
}{m: map[uuid.UUID]pollPolicyEntry{}}

// ResetPollPolicyCache empties the cache; tests that edit a profile call it.
func ResetPollPolicyCache() {
	pollPolicyCache.mu.Lock()
	pollPolicyCache.m = map[uuid.UUID]pollPolicyEntry{}
	pollPolicyCache.mu.Unlock()
}

// cachedPollPolicy returns holdpolicy.ForSession for the session, cached for
// pollPolicyTTL. A failed read is not cached.
func cachedPollPolicy(ctx context.Context, q holdpolicy.Querier, sessionID uuid.UUID) (holdpolicy.Policy, error) {
	now := time.Now()
	pollPolicyCache.mu.Lock()
	e, ok := pollPolicyCache.m[sessionID]
	pollPolicyCache.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.policy, nil
	}
	p, err := holdpolicy.ForSession(ctx, q, sessionID)
	if err != nil {
		return p, err
	}
	pollPolicyCache.mu.Lock()
	if len(pollPolicyCache.m) >= pollPolicyMaxEntries {
		pollPolicyCache.m = map[uuid.UUID]pollPolicyEntry{}
	}
	pollPolicyCache.m[sessionID] = pollPolicyEntry{policy: p, expires: now.Add(pollPolicyTTL)}
	pollPolicyCache.mu.Unlock()
	return p, nil
}

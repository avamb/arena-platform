// token_cache.go — in-process cache for Bil24 gateway token verification
// (perf fix: every /compat/bil24/json command ran a fresh bcrypt compare,
// never faster than ~50ms and up to ~190ms CPU on this host; a burst of
// concurrent identical requests multiplied that cost linearly).
//
// Both credential-check paths in auth.go — authenticateCommand's inline
// bcrypt call and the legacy validateGatewayToken helper — MUST go through
// Handler.verifyGatewayToken instead of calling bcrypt directly, so a
// channel's verification result is cached exactly once regardless of which
// path resolved it.
//
// Keying note: the cache is keyed by the bcrypt HASH string itself rather
// than the sales_channels.id, even though the two call sites differ in what
// context they have available — authenticateCommand resolves a full channel
// row (spec §5 fid→channel lookup) while the legacy validateGatewayToken
// path (RESERVATION, cart commands, GET_TICKETS_BY_ORDER, CREATE_ORDER_EXT,
// PAY_ORDER — several of those call sites live in files other agents are
// concurrently editing and must not be touched here) only ever receives the
// resolved settings blob, not the channel row. bcrypt embeds a random salt
// on every hash generation, so two channels' hashes never collide and a
// credential rotation (PUT .../gateway-credential) always produces a
// different hash string — keying by hash therefore gives the exact same
// invalidation property ("rotating the credential invalidates the cache
// immediately, with no explicit eviction step") as keying by channel ID
// would, without requiring a channel ID at every call site.
//
// Design:
//
//   - Only a SUCCESSFUL bcrypt compare is ever cached. A wrong guess always
//     re-runs bcrypt at full cost, so caching can never make brute-forcing
//     the token cheaper for an attacker.
//   - A hit requires: a live (non-expired) entry stored under the EXACT
//     hash string the caller just read from sales_channels.settings for
//     this request, and the SHA-256 digest of the presented token matches
//     the entry's digest under a constant-time compare. The plaintext token
//     is never retained.
//   - Bounded to maxTokenCacheEntries distinct hashes (~one per channel, the
//     rare moment of a mid-flight rotation aside); opportunistically evicts
//     expired entries on insert and, if still full, drops one arbitrary
//     entry (Go's randomized map iteration order makes this a cheap
//     approximation of random eviction).
//   - A cold-cache burst of identical (hash, token) requests coalesces onto
//     a single bcrypt call via singleflight (TokenCache.sf) — the load
//     test that surfaced this defect sent 200 simultaneous identical
//     reservation requests, which would otherwise run 200 bcrypt compares.
package hbil24

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"
)

const (
	// defaultTokenCacheTTL is how long a successful verification stays
	// cached when NewTokenCache gets no positive TTL (
	// / BIL24_TOKEN_CACHE_TTL).
	defaultTokenCacheTTL = 5 * time.Minute

	// maxTokenCacheEntries bounds memory: one entry per bcrypt hash
	// currently in use, which is effectively one per sales channel, so this
	// comfortably covers every deployed gateway channel with headroom.
	maxTokenCacheEntries = 10000
)

// bcryptCompare wraps bcrypt.CompareHashAndPassword so tests can substitute
// a counting/instrumented replacement without paying the real ~50-190ms CPU
// cost per invocation. Production code must never call
// bcrypt.CompareHashAndPassword directly for gateway token checks — always
// go through Handler.verifyGatewayToken, which calls this var.
var bcryptCompare = bcrypt.CompareHashAndPassword

// tokenCacheEntry is one cached successful verification, keyed by the bcrypt
// hash it was verified against (see the keying note above).
type tokenCacheEntry struct {
	// digest is sha256(token) for the token that produced this entry. The
	// plaintext token itself is never stored.
	digest [32]byte
	// expires is when this entry stops being honored.
	expires time.Time
}

// TokenCache is an in-process, concurrency-safe cache of successful Bil24
// gateway token verifications, keyed by the bcrypt hash string.
//
// It must outlive a single request. The httpserver builds a fresh
// hbil24.Handler per request (bil24_shims.go), so a cache owned by the
// Handler starts empty every time and never hits: the first version of this
// fix did exactly that and the load test showed no change. The Server owns
// one TokenCache and passes it in with Handler.WithTokenCache.
type TokenCache struct {
	mu      sync.RWMutex
	entries map[string]tokenCacheEntry
	ttl     time.Duration
	maxSize int
	// now is the clock the cache checks expiry against. Defaults to
	// time.Now; tests override it to exercise TTL expiry deterministically.
	now func() time.Time
	// sf coalesces concurrent cold-cache bcrypt compares for the same
	// (hash, token) onto a single call. Zero value is ready to use.
	sf singleflight.Group
}

// NewTokenCache builds a TokenCache with the given TTL (defaultTokenCacheTTL
// when ttl <= 0) and the default entry cap.
func NewTokenCache(ttl time.Duration) *TokenCache {
	if ttl <= 0 {
		ttl = defaultTokenCacheTTL
	}
	return &TokenCache{
		entries: make(map[string]tokenCacheEntry),
		ttl:     ttl,
		maxSize: maxTokenCacheEntries,
		now:     time.Now,
	}
}

// hit reports whether tokenHash has a live cached verification matching
// token. tokenHash must be the hash freshly read from the channel's settings
// for THIS request — a hash nobody has verified yet (never cached, or
// rotated away from) is always a miss, never an error.
func (c *TokenCache) hit(tokenHash, token string) bool {
	c.mu.RLock()
	entry, ok := c.entries[tokenHash]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	if c.now().After(entry.expires) {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], entry.digest[:]) == 1
}

// put records a successful verification of token against tokenHash. Callers
// must only call this AFTER bcryptCompare has actually succeeded — put never
// re-verifies.
func (c *TokenCache) put(tokenHash, token string) {
	entry := tokenCacheEntry{
		digest:  sha256.Sum256([]byte(token)),
		expires: c.now().Add(c.ttl),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[tokenHash]; !exists && len(c.entries) >= c.maxSize {
		c.evictLocked()
	}
	c.entries[tokenHash] = entry
}

// evictLocked drops expired entries; if none were expired (cache still full
// of live entries) it drops one arbitrary entry to bound memory. Callers
// must hold c.mu for writing.
func (c *TokenCache) evictLocked() {
	now := c.now()
	removedExpired := false
	for hash, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, hash)
			removedExpired = true
		}
	}
	if removedExpired || len(c.entries) < c.maxSize {
		return
	}
	for hash := range c.entries {
		delete(c.entries, hash)
		return
	}
}

// verifyGatewayToken is the ONLY path that may run a bcrypt compare for a
// Bil24 gateway credential. Both authenticateCommand and validateGatewayToken
// call it after their existing "channel disabled" / "no hash configured"
// gates, passing the hash they just read from the channel's settings.
//
// Returns true when token verifies against tokenHash, using the cache to
// skip the bcrypt cost on a repeat request and singleflight to coalesce
// concurrent first-requests for the same (hash, token) onto one bcrypt call.
func (h *Handler) verifyGatewayToken(tokenHash, token string) bool {
	cache := h.tokenCache
	if cache == nil {
		// Handlers built as bare struct literals in unit tests have no
		// cache: verify without caching. The field is deliberately not
		// assigned here, since concurrent requests would race on it.
		// Production wiring always goes through New().
		cache = NewTokenCache(defaultTokenCacheTTL)
	}

	if cache.hit(tokenHash, token) {
		return true
	}

	digest := sha256.Sum256([]byte(token))
	key := tokenHash + "|" + hex.EncodeToString(digest[:])
	v, _, _ := cache.sf.Do(key, func() (interface{}, error) {
		ok := bcryptCompare([]byte(tokenHash), []byte(token)) == nil
		if ok {
			cache.put(tokenHash, token)
		}
		return ok, nil
	})
	ok, _ := v.(bool)
	return ok
}

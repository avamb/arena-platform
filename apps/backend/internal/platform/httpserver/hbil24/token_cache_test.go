// token_cache_test.go — unit tests for the Bil24 gateway token verification
// cache (perf fix: every /compat/bil24/json command previously ran a fresh
// bcrypt compare, measured at ~50-190ms CPU, with no caching at all).
//
// These tests instrument the package-level bcryptCompare var (see
// token_cache.go) to count real bcrypt invocations instead of paying the
// real cost on every scenario. They run sequentially (no t.Parallel) so
// swapping that global var for the duration of a test is safe — the only
// other test in this package using t.Parallel (poster_535_test.go) never
// touches bcryptCompare.
package hbil24

import (
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// withCountingBcrypt overrides bcryptCompare for the duration of the test so
// assertions can count real bcrypt invocations. Restored via t.Cleanup.
func withCountingBcrypt(t *testing.T) *int64 {
	t.Helper()
	orig := bcryptCompare
	var count int64
	bcryptCompare = func(hashedPassword, password []byte) error {
		atomic.AddInt64(&count, 1)
		return orig(hashedPassword, password)
	}
	t.Cleanup(func() { bcryptCompare = orig })
	return &count
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache hit / miss behavior
// ─────────────────────────────────────────────────────────────────────────────

func TestTokenCache_HitSkipsBcrypt(t *testing.T) {
	count := withCountingBcrypt(t)
	h := newMinimalHandler()
	tokenHash := mustBcryptHash(t, "s3cr3t")

	for i := 0; i < 5; i++ {
		if !h.verifyGatewayToken(tokenHash, "s3cr3t") {
			t.Fatalf("iteration %d: expected verification to succeed", i)
		}
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Errorf("expected exactly 1 bcrypt compare across 5 identical requests, got %d", got)
	}
}

func TestTokenCache_WrongTokenAfterCachedSuccess_StillRunsBcrypt(t *testing.T) {
	count := withCountingBcrypt(t)
	h := newMinimalHandler()
	tokenHash := mustBcryptHash(t, "correct-token")

	if !h.verifyGatewayToken(tokenHash, "correct-token") {
		t.Fatalf("expected the correct token to verify")
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Fatalf("priming call: expected 1 bcrypt compare, got %d", got)
	}

	if h.verifyGatewayToken(tokenHash, "wrong-token") {
		t.Fatalf("expected the wrong token to fail even with a cached success for the same hash")
	}
	if got := atomic.LoadInt64(count); got != 2 {
		t.Errorf("a wrong guess must always re-run bcrypt: expected 2 compares, got %d", got)
	}

	// A second identical wrong guess must ALSO re-run bcrypt — failures are
	// never cached, or an attacker's repeat guesses would get cheaper.
	if h.verifyGatewayToken(tokenHash, "wrong-token") {
		t.Fatalf("expected the wrong token to keep failing")
	}
	if got := atomic.LoadInt64(count); got != 3 {
		t.Errorf("expected 3 compares after a second wrong guess, got %d", got)
	}
}

func TestTokenCache_Rotation_OldTokenFailsNewTokenVerifies(t *testing.T) {
	count := withCountingBcrypt(t)
	h := newMinimalHandler()

	oldHash := mustBcryptHash(t, "old-token")
	newHash := mustBcryptHash(t, "new-token")

	if !h.verifyGatewayToken(oldHash, "old-token") {
		t.Fatalf("expected old token to verify against old hash")
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Fatalf("expected 1 compare after priming old hash, got %d", got)
	}

	// Credential rotated: settings now carry newHash. The old token must
	// fail against it, paying a real bcrypt compare rather than a false hit
	// against the stale cache entry (which lives under the old hash key).
	if h.verifyGatewayToken(newHash, "old-token") {
		t.Fatalf("old token must not verify against the rotated hash")
	}
	if got := atomic.LoadInt64(count); got != 2 {
		t.Errorf("expected the rotated-hash check to run bcrypt, got %d compares", got)
	}

	// The new token verifies against the new hash — one more real compare.
	if !h.verifyGatewayToken(newHash, "new-token") {
		t.Fatalf("new token must verify against the rotated hash")
	}
	if got := atomic.LoadInt64(count); got != 3 {
		t.Errorf("expected 3 compares after the new token verifies, got %d", got)
	}

	// Repeating the new token now hits the cache.
	if !h.verifyGatewayToken(newHash, "new-token") {
		t.Fatalf("expected the new token to keep verifying")
	}
	if got := atomic.LoadInt64(count); got != 3 {
		t.Errorf("expected the repeat new-token check to hit the cache (still 3 compares), got %d", got)
	}
}

func TestTokenCache_Expiry_FakeClock(t *testing.T) {
	count := withCountingBcrypt(t)
	h := newMinimalHandler()

	cur := time.Now()
	h.tokenCache = &TokenCache{
		entries: make(map[string]tokenCacheEntry),
		ttl:     time.Minute,
		maxSize: maxTokenCacheEntries,
		now:     func() time.Time { return cur },
	}

	tokenHash := mustBcryptHash(t, "ttl-token")
	if !h.verifyGatewayToken(tokenHash, "ttl-token") {
		t.Fatalf("expected the token to verify")
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Fatalf("expected 1 compare priming the cache, got %d", got)
	}

	// Still within TTL: cache hit, no new bcrypt call.
	cur = cur.Add(30 * time.Second)
	if !h.verifyGatewayToken(tokenHash, "ttl-token") {
		t.Fatalf("expected the token to still verify within TTL")
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Errorf("expected the cache to still be warm within TTL, got %d compares", got)
	}

	// Past TTL: the entry must expire and bcrypt must run again.
	cur = cur.Add(time.Minute)
	if !h.verifyGatewayToken(tokenHash, "ttl-token") {
		t.Fatalf("expected the token to verify again after TTL expiry")
	}
	if got := atomic.LoadInt64(count); got != 2 {
		t.Errorf("expected TTL expiry to force a fresh bcrypt compare, got %d", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The "disabled channel" gate must run BEFORE the cache is ever consulted
// ─────────────────────────────────────────────────────────────────────────────

func TestTokenCache_DisabledChannelWithCachedEntry_Rejected(t *testing.T) {
	count := withCountingBcrypt(t)
	tokenHash := mustBcryptHash(t, "gate-token")

	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 77,
		Settings: gatewaySettingsBlob(t, true, tokenHash),
	}
	lookup := &fakeChannelLookup{
		byDisplayNumber: map[int64]gen.SalesChannelRow{ch.DisplayNumber: ch},
		byUUID:          map[uuid.UUID]gen.SalesChannelRow{ch.ID: ch},
	}
	h := newMinimalHandler().WithChannelLookup(lookup).WithRequireToken(true)

	got, ok := h.authenticateCommand(context.Background(), httptest.NewRecorder(),
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "77", Token: "gate-token"})
	if !ok || got.ID != ch.ID {
		t.Fatalf("expected the enabled channel to authenticate first")
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Fatalf("expected 1 compare priming the cache, got %d", got)
	}

	// Operator disables the channel; the stored hash is unchanged, so a
	// cache keyed only on the hash could in principle still "know" the
	// token verifies. The enabled gate in authenticateCommand runs BEFORE
	// the cache/bcrypt check, so this must be rejected without ever
	// touching bcrypt (or the cache) again.
	ch.Settings = gatewaySettingsBlob(t, false, tokenHash)
	lookup.byDisplayNumber[ch.DisplayNumber] = ch
	lookup.byUUID[ch.ID] = ch

	w := httptest.NewRecorder()
	_, ok = h.authenticateCommand(context.Background(), w,
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "77", Token: "gate-token"})
	if ok {
		t.Fatalf("expected the disabled channel to be rejected despite a cached success")
	}
	if code := mustResultCodeFromBytes(t, w.Body.Bytes()); code != ResultCodeUnauthorized {
		t.Errorf("expected -4 for a disabled channel, got %d", code)
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Errorf("the disabled-channel gate must short-circuit before bcrypt/cache, expected compares to stay at 1, got %d", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Concurrency: a cold-cache burst coalesces onto (a small number of) bcrypt
// calls via singleflight, and every caller still gets the correct answer.
// ─────────────────────────────────────────────────────────────────────────────

func TestTokenCache_Concurrency_ColdCache_SingleflightDedup(t *testing.T) {
	orig := bcryptCompare
	var count int64
	// Artificial delay so the 200 goroutines below reliably pile up on the
	// singleflight key before the leader finishes, instead of racing to
	// completion independently.
	bcryptCompare = func(hashedPassword, password []byte) error {
		atomic.AddInt64(&count, 1)
		time.Sleep(20 * time.Millisecond)
		return orig(hashedPassword, password)
	}
	t.Cleanup(func() { bcryptCompare = orig })

	h := newMinimalHandler()
	tokenHash := mustBcryptHash(t, "burst-token")

	const n = 200
	var wg sync.WaitGroup
	results := make([]bool, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = h.verifyGatewayToken(tokenHash, "burst-token")
		}(i)
	}
	wg.Wait()

	for i, ok := range results {
		if !ok {
			t.Errorf("goroutine %d: expected verification to succeed", i)
		}
	}
	// singleflight guarantees exact coalescing when every caller submits
	// before the leader completes (true here given the 20ms delay against
	// near-instant goroutine startup), but allow a small margin so the test
	// is not flaky under the CPU contention this host's AGENTS.md documents
	// for other suites.
	if got := atomic.LoadInt64(&count); got < 1 || got > 5 {
		t.Errorf("expected singleflight to coalesce 200 identical cold-cache requests onto a small number of bcrypt compares (got %d)", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Both call sites (authenticateCommand's inline check and the legacy
// validateGatewayToken helper) must route through the same cache.
// ─────────────────────────────────────────────────────────────────────────────

func TestVerifyGatewayToken_AuthenticateCommandPath_UsesCache(t *testing.T) {
	count := withCountingBcrypt(t)
	tokenHash := mustBcryptHash(t, "auth-cmd-token")
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 88,
		Settings: gatewaySettingsBlob(t, true, tokenHash),
	}
	h := buildAuthHandler(t, ch, true)

	for i := 0; i < 3; i++ {
		_, ok := h.authenticateCommand(context.Background(), httptest.NewRecorder(),
			bil24Request{Command: "GET_ALL_ACTIONS", FID: "88", Token: "auth-cmd-token"})
		if !ok {
			t.Fatalf("iteration %d: expected authenticateCommand to succeed", i)
		}
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Errorf("expected authenticateCommand to route through the token cache: want 1 compare, got %d", got)
	}
}

func TestVerifyGatewayToken_ValidateGatewayTokenPath_UsesCache(t *testing.T) {
	count := withCountingBcrypt(t)
	h := newMinimalHandler()
	tokenHash := mustBcryptHash(t, "legacy-path-token")
	settings := channelSettings(t, tokenHash)

	for i := 0; i < 3; i++ {
		ok := h.validateGatewayToken(httptest.NewRecorder(),
			bil24Request{Command: "RESERVATION", Token: "legacy-path-token"}, settings)
		if !ok {
			t.Fatalf("iteration %d: expected validateGatewayToken to succeed", i)
		}
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Errorf("expected validateGatewayToken to route through the token cache: want 1 compare, got %d", got)
	}
}

// TestTokenCache_SharedAcrossHandlers is the regression guard for the first
// version of this fix: the httpserver builds a fresh Handler per request, so a
// cache owned by one Handler never hit and the load test showed no change. Two
// Handlers given the same TokenCache must share the verification.
func TestTokenCache_SharedAcrossHandlers(t *testing.T) {
	count := withCountingBcrypt(t)
	tokenHash := mustBcryptHash(t, "shared-token")
	shared := NewTokenCache(time.Minute)

	for i := 0; i < 3; i++ {
		h := newMinimalHandler().WithTokenCache(shared)
		if !h.verifyGatewayToken(tokenHash, "shared-token") {
			t.Fatalf("request %d: expected verification to succeed", i)
		}
	}
	if got := atomic.LoadInt64(count); got != 1 {
		t.Errorf("per-request Handlers sharing one TokenCache: want 1 bcrypt compare, got %d", got)
	}

	// Without the shared cache every per-request Handler pays bcrypt again.
	if !newMinimalHandler().verifyGatewayToken(tokenHash, "shared-token") {
		t.Fatalf("expected verification with a private cache to succeed")
	}
	if got := atomic.LoadInt64(count); got != 2 {
		t.Errorf("a Handler with its own private cache must run bcrypt, want 2 compares, got %d", got)
	}
}

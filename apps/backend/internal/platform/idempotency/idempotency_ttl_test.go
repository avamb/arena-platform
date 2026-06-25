// Package idempotency_ttl_test verifies feature #47:
// "Idempotency key expires after TTL".
//
// Stored idempotency keys have expires_at = created_at + TTL (default 24h).
// After expiry the key is effectively reusable: the next request with the same
// key re-executes the handler and the old row is REPLACED (not duplicated).
//
// Step-by-step coverage:
//
//	Step 1: POST /v1/echo with Idempotency-Key: TTL_KEY_1
//	Step 2: Query idempotency_keys, capture expires_at
//	Step 3: Verify expires_at > now() + 23h
//	Step 4: Force expiry (SET expires_at = now() - 1s)
//	Step 5: POST /v1/echo with same Idempotency-Key: TTL_KEY_1, same body
//	Step 6: Verify HTTP 200 (re-execution allowed)
//	Step 7: Verify handler was called again (second audit event would be written)
//	Step 8: Verify idempotency_keys row was REPLACED (single row, new created_at)
package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// Step 1-3: First request stores row with expires_at > now()+23h
// ----------------------------------------------------------------------------

// TestIdemTTL_ExpiresAtIsGreaterThanNowPlus23h verifies steps 1-3:
// After the first POST, the stored row has expires_at > now()+23h (default
// TTL is 24h), ensuring keys stay valid for a full day by default.
func TestIdemTTL_ExpiresAtIsGreaterThanNowPlus23h(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	before := time.Now()

	// Step 1: POST with Idempotency-Key: TTL_KEY_1.
	resp := postEcho(t, ts, "TTL_KEY_1", `{"message":"ttl test"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 1: want 200, got %d", resp.StatusCode)
	}

	// Step 2: Query the stored row.
	entry, ok := store.get("TTL_KEY_1", testScope)
	if !ok {
		t.Fatal("step 2: no row found in store for TTL_KEY_1")
	}

	// Step 3: expires_at must be > now()+23h (default TTL=24h).
	threshold := before.Add(23 * time.Hour)
	if !entry.resp.ExpiresAt.After(threshold) {
		t.Errorf("step 3: expires_at %v is not after now()+23h (%v)",
			entry.resp.ExpiresAt, threshold)
	}
}

// TestIdemTTL_ExpiresAtApproximately24hFromNow verifies that the default TTL
// places expires_at within [now+23h50m, now+24h10m] — i.e. the 24h default,
// not some other value.
func TestIdemTTL_ExpiresAtApproximately24hFromNow(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	before := time.Now()

	resp := postEcho(t, ts, "TTL_KEY_APPROX", `{"message":"approx ttl"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	after := time.Now()

	entry, ok := store.get("TTL_KEY_APPROX", testScope)
	if !ok {
		t.Fatal("no row found for TTL_KEY_APPROX")
	}

	minExpiry := before.Add(24 * time.Hour)
	maxExpiry := after.Add(24 * time.Hour).Add(time.Second) // +1s tolerance

	if entry.resp.ExpiresAt.Before(minExpiry) {
		t.Errorf("expires_at %v is before expected minimum %v", entry.resp.ExpiresAt, minExpiry)
	}
	if entry.resp.ExpiresAt.After(maxExpiry) {
		t.Errorf("expires_at %v is after expected maximum %v", entry.resp.ExpiresAt, maxExpiry)
	}
}

// ----------------------------------------------------------------------------
// Step 4-6: Forced expiry → re-execution allowed (HTTP 200)
// ----------------------------------------------------------------------------

// TestIdemTTL_ExpiredKeyAllowsReexecution verifies steps 4-6:
// After forcing the key to expire, the next request with the same key+body
// returns HTTP 200 (re-execution, not a replay or 409).
func TestIdemTTL_ExpiredKeyAllowsReexecution(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_1"
	const body = `{"message":"reexec test"}`

	// Step 1: first POST.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", resp1.StatusCode)
	}
	if calls.Load() != 1 {
		t.Fatalf("after first request: want 1 handler call, got %d", calls.Load())
	}

	// Step 4: Force expiry (models: UPDATE SET expires_at = now() - 1s).
	store.forceExpire(key, testScope)

	// Confirm expiry was applied.
	entry, _ := store.get(key, testScope)
	if !time.Now().After(entry.resp.ExpiresAt) {
		t.Fatal("step 4: forceExpire did not set expires_at to the past")
	}

	// Step 5: POST with same key and body.
	resp2 := postEcho(t, ts, key, body)
	body2 := readBody(t, resp2)

	// Step 6: Verify HTTP 200 (re-execution allowed).
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("step 6: want 200 on re-execution after expiry, got %d (body: %s)",
			resp2.StatusCode, body2)
	}
}

// TestIdemTTL_ExpiredKeyDoesNotReplayOldResponse verifies that after expiry the
// Idempotent-Replay header is NOT set — the response is a fresh execution, not
// a cached replay of the original response.
func TestIdemTTL_ExpiredKeyDoesNotReplayOldResponse(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_NOREPLAY"
	const body = `{"message":"no replay after expiry"}`

	// First POST.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Force expiry.
	store.forceExpire(key, testScope)

	// Re-execute after expiry.
	resp2 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	// The re-execution must NOT set Idempotent-Replay (it's a fresh invocation).
	if got := resp2.Header.Get("Idempotent-Replay"); got == "true" {
		t.Errorf("step 6: Idempotent-Replay must be absent on re-execution after expiry, got %q", got)
	}
}

// ----------------------------------------------------------------------------
// Step 7: Handler called again after expiry → audit_events row would be created
// ----------------------------------------------------------------------------

// TestIdemTTL_HandlerCalledAgainAfterExpiry verifies step 7:
// The downstream handler is invoked a second time after the key expires.
// In production, this second invocation would write a new audit_events row.
// We verify it by asserting the handler call count is exactly 2.
func TestIdemTTL_HandlerCalledAgainAfterExpiry(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_AUDIT"
	const body = `{"message":"audit events test"}`

	// First request: handler called once.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Step 4: force expiry.
	store.forceExpire(key, testScope)

	// Step 5: re-execute after expiry.
	resp2 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	// Step 7: handler must have been called exactly twice (second audit event).
	if n := calls.Load(); n != 2 {
		t.Errorf("step 7: want handler called 2 times after expiry+re-execution, got %d", n)
	}
}

// TestIdemTTL_LiveKeyStillPreventsHandlerCallAfterExpiry is a regression guard:
// A live (non-expired) key must still replay the stored response and NOT call
// the handler again. This verifies the fix does not break normal deduplication.
func TestIdemTTL_LiveKeyStillPreventsHandlerCallAfterExpiry(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_LIVE"
	const body = `{"message":"live key"}`

	// First request.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Second request with same key — key is still live (not expired).
	resp2 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	// Handler must only be called once (replay for live key).
	if n := calls.Load(); n != 1 {
		t.Errorf("regression: live key must replay; want 1 handler call, got %d", n)
	}
	// Second response must carry Idempotent-Replay.
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("regression: live key must set Idempotent-Replay: true, got %q", got)
	}
}

// ----------------------------------------------------------------------------
// Step 8: Row REPLACED after expiry (single row, new created_at)
// ----------------------------------------------------------------------------

// TestIdemTTL_ExpiredRowIsReplaced verifies step 8:
// After the key expires and the handler re-executes, the store contains exactly
// ONE row (the old expired row was replaced, not kept alongside the new one)
// and the new row has a created_at >= the original created_at.
func TestIdemTTL_ExpiredRowIsReplaced(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_REPLACE"
	const body = `{"message":"row replace test"}`

	// First request.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Capture original created_at.
	origEntry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 8: no row after first request")
	}
	origCreatedAt := origEntry.resp.CreatedAt

	// Give the clock at least 1 nanosecond to advance.
	time.Sleep(time.Millisecond)

	// Step 4: force expiry.
	store.forceExpire(key, testScope)

	// Step 5: re-execute.
	resp2 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	// Step 8a: exactly ONE row in the store (replaced, not added).
	if n := store.count(); n != 1 {
		t.Errorf("step 8: want 1 row after replacement, got %d", n)
	}

	// Step 8b: the row's created_at must be >= the original (new row).
	newEntry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 8: row missing after re-execution")
	}
	if newEntry.resp.CreatedAt.Before(origCreatedAt) {
		t.Errorf("step 8: new created_at %v is before original %v — row was not replaced",
			newEntry.resp.CreatedAt, origCreatedAt)
	}
}

// TestIdemTTL_ReplacedRowHasNewExpiresAt verifies that after row replacement
// the new row's expires_at is in the future (not the old expired timestamp).
func TestIdemTTL_ReplacedRowHasNewExpiresAt(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_NEWEXPIRY"
	const body = `{"message":"new expiry test"}`

	// First request.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Force expiry.
	store.forceExpire(key, testScope)

	beforeReexec := time.Now()

	// Re-execute after expiry.
	resp2 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	// New row's expires_at must be > now (not the old expired value).
	newEntry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("no row after re-execution")
	}

	minExpiry := beforeReexec.Add(23 * time.Hour)
	if !newEntry.resp.ExpiresAt.After(minExpiry) {
		t.Errorf("new expires_at %v must be after %v (re-execution TTL not applied)",
			newEntry.resp.ExpiresAt, minExpiry)
	}
}

// TestIdemTTL_RowCountStaysOneAfterMultipleExpiries verifies that repeated
// expire+re-execute cycles never accumulate duplicate rows — the count stays 1.
func TestIdemTTL_RowCountStaysOneAfterMultipleExpiries(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_MULTICYCLE"
	const body = `{"message":"multi cycle"}`

	// Initial request.
	resp := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	for i := 0; i < 3; i++ {
		store.forceExpire(key, testScope)

		resp = postEcho(t, ts, key, body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if n := store.count(); n != 1 {
			t.Errorf("cycle %d: want 1 row, got %d", i+1, n)
		}
	}

	// Handler must have been called 4 times (1 initial + 3 re-executions).
	if n := calls.Load(); n != 4 {
		t.Errorf("want 4 handler calls over 3 expiry cycles, got %d", n)
	}
}

// ----------------------------------------------------------------------------
// Custom TTL
// ----------------------------------------------------------------------------

// TestIdemTTL_CustomTTLIsStored verifies that a non-default TTL (e.g. 2h) is
// correctly applied to expires_at = created_at + 2h.
func TestIdemTTL_CustomTTLIsStored(t *testing.T) {
	const customTTL = 2 * time.Hour

	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope, TTL: customTTL})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	before := time.Now()

	resp := postEcho(t, ts, "TTL_CUSTOM_KEY", `{"message":"custom ttl"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	after := time.Now()

	entry, ok := store.get("TTL_CUSTOM_KEY", testScope)
	if !ok {
		t.Fatal("no row for TTL_CUSTOM_KEY")
	}

	minExpiry := before.Add(customTTL)
	maxExpiry := after.Add(customTTL).Add(time.Second)

	if entry.resp.ExpiresAt.Before(minExpiry) {
		t.Errorf("custom TTL: expires_at %v is before %v", entry.resp.ExpiresAt, minExpiry)
	}
	if entry.resp.ExpiresAt.After(maxExpiry) {
		t.Errorf("custom TTL: expires_at %v is after %v", entry.resp.ExpiresAt, maxExpiry)
	}
}

// TestIdemTTL_ShortTTLExpires verifies that a very short TTL (1ms) expires
// almost immediately, letting the next request re-execute the handler.
func TestIdemTTL_ShortTTLExpires(t *testing.T) {
	const shortTTL = time.Millisecond

	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope, TTL: shortTTL})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_SHORT_KEY"
	const body = `{"message":"short ttl"}`

	// First request.
	resp1 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Wait for the key to expire naturally.
	time.Sleep(5 * time.Millisecond)

	// Re-execute — key should now be expired so Lookup returns MISS.
	resp2 := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("want 200 after short TTL expiry, got %d", resp2.StatusCode)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("want 2 handler calls (initial + re-exec after expiry), got %d", n)
	}
}

// ----------------------------------------------------------------------------
// Full verification sweep (all 8 steps in one test)
// ----------------------------------------------------------------------------

// TestIdemTTL_FullVerification runs all 8 feature steps in sequence, providing
// a single end-to-end verification of the idempotency TTL behaviour.
func TestIdemTTL_FullVerification(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_KEY_1"
	const body = `{"message":"full verification"}`

	// Step 1: POST /v1/echo with Idempotency-Key: TTL_KEY_1.
	resp1 := postEcho(t, ts, key, body)
	body1 := readBody(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("step 1: want 200, got %d (body: %s)", resp1.StatusCode, body1)
	}

	// Step 2: Query idempotency_keys — row must exist.
	entry1, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 2: no row found in store after first request")
	}

	// Step 3: expires_at > now()+23h.
	threshold := time.Now().Add(23 * time.Hour)
	if !entry1.resp.ExpiresAt.After(threshold) {
		t.Errorf("step 3: expires_at %v not after now()+23h %v",
			entry1.resp.ExpiresAt, threshold)
	}

	// Capture the original created_at for step 8.
	origCreatedAt := entry1.resp.CreatedAt
	time.Sleep(time.Millisecond) // ensure clock advances

	// Step 4: Force expiry (simulate: UPDATE SET expires_at = now() - 1s).
	store.forceExpire(key, testScope)

	// Verify the expiry was applied before continuing.
	if exp, _ := store.get(key, testScope); time.Now().Before(exp.resp.ExpiresAt) {
		t.Fatal("step 4: forceExpire did not set expires_at to the past")
	}

	// Step 5: POST with same Idempotency-Key: TTL_KEY_1, same body.
	resp2 := postEcho(t, ts, key, body)
	body2 := readBody(t, resp2)

	// Step 6: Verify HTTP 200 (re-execution allowed).
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("step 6: want 200 on re-execution, got %d (body: %s)", resp2.StatusCode, body2)
	}

	// Step 7: Verify handler was called again (second audit event would be written).
	if n := calls.Load(); n != 2 {
		t.Errorf("step 7: want 2 handler invocations after expiry+re-exec, got %d", n)
	}

	// Step 8a: idempotency_keys row was REPLACED — exactly 1 row.
	if n := store.count(); n != 1 {
		t.Errorf("step 8: want 1 row after replacement, got %d", n)
	}

	// Step 8b: new row has new created_at (replacement, not original row).
	entry2, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 8: row missing after re-execution")
	}
	if entry2.resp.CreatedAt.Before(origCreatedAt) {
		t.Errorf("step 8: new created_at %v is before original %v — row not replaced",
			entry2.resp.CreatedAt, origCreatedAt)
	}

	t.Logf("Full verification PASS: handler called %d times, row replaced with new created_at %v",
		calls.Load(), entry2.resp.CreatedAt)
}
